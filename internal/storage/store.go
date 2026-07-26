package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zeemzo/evm-indexer/internal/events"
)

// Checkpoint records the highest durably indexed block for a chain,
// together with its hash so reorgs can be detected on restart.
type Checkpoint struct {
	ChainID   uint64
	LastBlock uint64
	LastHash  string
	UpdatedAt time.Time
}

// ErrNoCheckpoint is returned when a chain has never been indexed.
var ErrNoCheckpoint = errors.New("no checkpoint for chain")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) GetCheckpoint(ctx context.Context, chainID uint64) (Checkpoint, error) {
	cp := Checkpoint{ChainID: chainID}
	err := s.pool.QueryRow(ctx,
		`SELECT last_block, last_block_hash, updated_at FROM checkpoints WHERE chain_id = $1`,
		chainID,
	).Scan(&cp.LastBlock, &cp.LastHash, &cp.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return cp, ErrNoCheckpoint
	}
	return cp, err
}

// WriteBatch persists a fully processed block range atomically: raw logs,
// typed rows, and the checkpoint advance commit together or not at all.
// All inserts are ON CONFLICT DO NOTHING on (tx_hash, log_index), so
// replaying a range after a crash or rewind is safe.
func (s *Store) WriteBatch(ctx context.Context, chainID uint64, rawLogs []events.RawLog, evts []events.Event, toBlock uint64, toBlockHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	batch := &pgx.Batch{}
	for _, l := range rawLogs {
		batch.Queue(`
			INSERT INTO event_logs (block_number, block_time, tx_hash, log_index, address, topics, data)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tx_hash, log_index) DO NOTHING`,
			l.BlockNumber, l.BlockTime, l.TxHash, l.LogIndex, l.Address, l.Topics, l.Data)
	}
	for _, e := range evts {
		switch e.Type {
		case events.TypeTransfer:
			t := e.Transfer
			batch.Queue(`
				INSERT INTO erc20_transfers (block_number, block_time, tx_hash, log_index, token, from_addr, to_addr, value)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric)
				ON CONFLICT (tx_hash, log_index) DO NOTHING`,
				t.BlockNumber, t.BlockTime, t.TxHash, t.LogIndex, t.Token, t.From, t.To, t.Value)
		case events.TypeSwap:
			w := e.Swap
			batch.Queue(`
				INSERT INTO swaps (block_number, block_time, tx_hash, log_index, pool, sender, recipient, amount0_in, amount1_in, amount0_out, amount1_out, venue)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12)
				ON CONFLICT (tx_hash, log_index) DO NOTHING`,
				w.BlockNumber, w.BlockTime, w.TxHash, w.LogIndex, w.Pool, w.Sender, w.Recipient,
				w.Amount0In, w.Amount1In, w.Amount0Out, w.Amount1Out, string(w.Venue))
		}
	}
	batch.Queue(`
		INSERT INTO checkpoints (chain_id, last_block, last_block_hash, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (chain_id) DO UPDATE
		SET last_block = EXCLUDED.last_block, last_block_hash = EXCLUDED.last_block_hash, updated_at = now()`,
		chainID, toBlock, toBlockHash)

	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	return tx.Commit(ctx)
}

// Rewind deletes everything above the given block and resets the
// checkpoint, in one transaction. Used for reorg recovery.
func (s *Store) Rewind(ctx context.Context, chainID, toBlock uint64, toBlockHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, table := range []string{"event_logs", "erc20_transfers", "swaps"} {
		if _, err := tx.Exec(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE block_number > $1`, table), toBlock); err != nil {
			return fmt.Errorf("rewind %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE checkpoints SET last_block = $2, last_block_hash = $3, updated_at = now()
		WHERE chain_id = $1`, chainID, toBlock, toBlockHash); err != nil {
		return fmt.Errorf("rewind checkpoint: %w", err)
	}
	return tx.Commit(ctx)
}

// TransferFilter narrows the transfers query; zero values mean "any".
type TransferFilter struct {
	Token       string
	From        string
	To          string
	BeforeBlock uint64
	Limit       int
}

func (s *Store) Transfers(ctx context.Context, f TransferFilter) ([]events.Transfer, error) {
	query := `
		SELECT block_number, block_time, tx_hash, log_index, token, from_addr, to_addr, value::text
		FROM erc20_transfers
		WHERE ($1 = '' OR token = $1)
		  AND ($2 = '' OR from_addr = $2)
		  AND ($3 = '' OR to_addr = $3)
		  AND ($4 = 0 OR block_number < $4)
		ORDER BY block_number DESC, log_index DESC
		LIMIT $5`
	rows, err := s.pool.Query(ctx, query, f.Token, f.From, f.To, f.BeforeBlock, clampLimit(f.Limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []events.Transfer
	for rows.Next() {
		var t events.Transfer
		if err := rows.Scan(&t.BlockNumber, &t.BlockTime, &t.TxHash, &t.LogIndex, &t.Token, &t.From, &t.To, &t.Value); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SwapFilter narrows the swaps query; zero values mean "any".
type SwapFilter struct {
	Pool        string
	Venue       string
	BeforeBlock uint64
	Limit       int
}

func (s *Store) Swaps(ctx context.Context, f SwapFilter) ([]events.Swap, error) {
	query := `
		SELECT block_number, block_time, tx_hash, log_index, pool, sender, recipient,
		       amount0_in::text, amount1_in::text, amount0_out::text, amount1_out::text, venue
		FROM swaps
		WHERE ($1 = '' OR pool = $1)
		  AND ($2 = '' OR venue = $2)
		  AND ($3 = 0 OR block_number < $3)
		ORDER BY block_number DESC, log_index DESC
		LIMIT $4`
	rows, err := s.pool.Query(ctx, query, f.Pool, f.Venue, f.BeforeBlock, clampLimit(f.Limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []events.Swap
	for rows.Next() {
		var w events.Swap
		var venue string
		if err := rows.Scan(&w.BlockNumber, &w.BlockTime, &w.TxHash, &w.LogIndex, &w.Pool, &w.Sender, &w.Recipient,
			&w.Amount0In, &w.Amount1In, &w.Amount0Out, &w.Amount1Out, &venue); err != nil {
			return nil, err
		}
		w.Venue = events.Venue(venue)
		out = append(out, w)
	}
	return out, rows.Err()
}

// Stats summarizes the indexed dataset for the /stats endpoint.
type Stats struct {
	TotalLogs      uint64 `json:"totalLogs"`
	TotalTransfers uint64 `json:"totalTransfers"`
	TotalSwaps     uint64 `json:"totalSwaps"`
	LastBlock      uint64 `json:"lastIndexedBlock"`
}

func (s *Store) Stats(ctx context.Context, chainID uint64) (Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM event_logs),
			(SELECT count(*) FROM erc20_transfers),
			(SELECT count(*) FROM swaps),
			COALESCE((SELECT last_block FROM checkpoints WHERE chain_id = $1), 0)`,
		chainID,
	).Scan(&st.TotalLogs, &st.TotalTransfers, &st.TotalSwaps, &st.LastBlock)
	return st, err
}

// Ping verifies database connectivity, used by the health endpoint.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func clampLimit(limit int) int {
	const defaultLimit, maxLimit = 25, 500
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
