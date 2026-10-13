// Integration tests for the storage layer, exercising the real SQL the
// correctness guarantees depend on: atomic batch writes, idempotent
// replays, checkpoint tracking, and reorg rewinds.
//
// They need a Postgres to talk to and skip when DATABASE_URL is unset
// (CI always sets it). Each test creates its own throwaway database and
// drops it afterwards, so tests never touch data in the configured
// database itself and are safe to run against any server.
package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zeemzo/evm-indexer/internal/events"
)

const testChainID = 137

// uint256 max: proves NUMERIC(78,0) holds any token amount losslessly.
const maxUint256 = "115792089237316195423570985008687907853269984665640564039457584007913129639935"

func setupStore(t *testing.T) *Store {
	t.Helper()
	baseURL := os.Getenv("DATABASE_URL")
	if baseURL == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres integration tests")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, baseURL)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	require.NoError(t, admin.Ping(ctx), "postgres not reachable at DATABASE_URL")

	// A fresh database per test isolates state and guarantees we never
	// mutate whatever DATABASE_URL points at.
	name := fmt.Sprintf("evm_indexer_test_%d", time.Now().UnixNano())
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)

	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	u.Path = "/" + name

	pool, err := Connect(ctx, u.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	require.NoError(t, Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	return NewStore(pool)
}

// addr returns a deterministic EIP-55 checksummed address, matching how
// the decoder renders addresses (common.Address.Hex()).
func addr(n int) string {
	return common.HexToAddress(fmt.Sprintf("0x%040x", n)).Hex()
}

func txHash(n int) string {
	return fmt.Sprintf("0x%064x", n)
}

func blockHash(block uint64) string {
	return fmt.Sprintf("0x%064x", block+0xffff)
}

var testTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func rawLog(block uint64, idx uint32) events.RawLog {
	return events.RawLog{
		BlockNumber: block,
		BlockTime:   testTime,
		TxHash:      txHash(int(block)*1000 + int(idx)),
		LogIndex:    idx,
		Address:     addr(1),
		Topics:      []string{txHash(0xaaaa)},
		Data:        []byte{0x01},
	}
}

func transferEvent(block uint64, idx uint32, token, from, to, value string) events.Event {
	return events.Event{
		Type: events.TypeTransfer,
		Transfer: &events.Transfer{
			BlockNumber: block,
			BlockTime:   testTime,
			TxHash:      txHash(int(block)*1000 + int(idx)),
			LogIndex:    idx,
			Token:       token,
			From:        from,
			To:          to,
			Value:       value,
		},
	}
}

func swapEvent(block uint64, idx uint32, pool string, venue events.Venue) events.Event {
	return events.Event{
		Type: events.TypeSwap,
		Swap: &events.Swap{
			BlockNumber: block,
			BlockTime:   testTime,
			TxHash:      txHash(int(block)*1000 + int(idx)),
			LogIndex:    idx,
			Pool:        pool,
			Sender:      addr(2),
			Recipient:   addr(3),
			Amount0In:   "1000",
			Amount1In:   "0",
			Amount0Out:  "0",
			Amount1Out:  "995",
			Venue:       venue,
		},
	}
}

func TestCheckpointLifecycle(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	_, err := s.GetCheckpoint(ctx, testChainID)
	require.ErrorIs(t, err, ErrNoCheckpoint, "fresh chain must report no checkpoint")

	// First batch creates the checkpoint; later batches advance it.
	require.NoError(t, s.WriteBatch(ctx, testChainID, nil, nil, 100, blockHash(100)))
	cp, err := s.GetCheckpoint(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, uint64(100), cp.LastBlock)
	assert.Equal(t, blockHash(100), strings.TrimSpace(cp.LastHash))

	require.NoError(t, s.WriteBatch(ctx, testChainID, nil, nil, 110, blockHash(110)))
	cp, err = s.GetCheckpoint(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, uint64(110), cp.LastBlock)
	assert.Equal(t, blockHash(110), strings.TrimSpace(cp.LastHash))
}

func TestWriteBatchAtomicAndIdempotent(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	raw := []events.RawLog{rawLog(100, 0), rawLog(101, 0)}
	evts := []events.Event{
		transferEvent(100, 0, addr(10), addr(11), addr(12), maxUint256),
		swapEvent(101, 0, addr(20), events.VenueV2),
	}

	require.NoError(t, s.WriteBatch(ctx, testChainID, raw, evts, 101, blockHash(101)))
	st, err := s.Stats(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, Stats{TotalLogs: 2, TotalTransfers: 1, TotalSwaps: 1, LastBlock: 101}, st)

	// Replaying the exact same batch (crash recovery, post-rewind
	// re-index) must not duplicate rows — ON CONFLICT DO NOTHING.
	require.NoError(t, s.WriteBatch(ctx, testChainID, raw, evts, 101, blockHash(101)))
	st, err = s.Stats(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, Stats{TotalLogs: 2, TotalTransfers: 1, TotalSwaps: 1, LastBlock: 101}, st)

	// uint256 max survives the NUMERIC(78,0) round trip losslessly.
	transfers, err := s.Transfers(ctx, TransferFilter{Token: addr(10)})
	require.NoError(t, err)
	require.Len(t, transfers, 1)
	assert.Equal(t, maxUint256, transfers[0].Value)
}

func TestRewind(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	// Index blocks 100-105, one transfer + one raw log per block.
	for block := uint64(100); block <= 105; block++ {
		require.NoError(t, s.WriteBatch(ctx, testChainID,
			[]events.RawLog{rawLog(block, 0)},
			[]events.Event{
				transferEvent(block, 0, addr(10), addr(11), addr(12), "1"),
				swapEvent(block, 1, addr(20), events.VenueV3),
			},
			block, blockHash(block)))
	}

	// Reorg detected: rewind to 102. Everything above must vanish and
	// the checkpoint must point at the rewind target.
	require.NoError(t, s.Rewind(ctx, testChainID, 102, blockHash(102)))

	st, err := s.Stats(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, Stats{TotalLogs: 3, TotalTransfers: 3, TotalSwaps: 3, LastBlock: 102}, st)

	cp, err := s.GetCheckpoint(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, uint64(102), cp.LastBlock)
	assert.Equal(t, blockHash(102), strings.TrimSpace(cp.LastHash))

	// Re-indexing the rewound range replays cleanly (idempotent writes).
	for block := uint64(103); block <= 105; block++ {
		require.NoError(t, s.WriteBatch(ctx, testChainID,
			[]events.RawLog{rawLog(block, 0)},
			[]events.Event{transferEvent(block, 0, addr(10), addr(11), addr(12), "1")},
			block, blockHash(block)))
	}
	st, err = s.Stats(ctx, testChainID)
	require.NoError(t, err)
	assert.Equal(t, Stats{TotalLogs: 6, TotalTransfers: 6, TotalSwaps: 3, LastBlock: 105}, st)
}

func TestAddressFiltersAreCaseInsensitive(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	// Real checksummed addresses as the decoder would store them.
	token := common.HexToAddress("0xdac17f958d2ee523a2206206994597c13d831ec7").Hex() // mixed case
	from := addr(11)
	pool := common.HexToAddress("0x88e6a0c2ddd26feeb64f039a2c41296fcb3f5640").Hex()

	require.NoError(t, s.WriteBatch(ctx, testChainID, nil,
		[]events.Event{
			transferEvent(100, 0, token, from, addr(12), "42"),
			swapEvent(100, 1, pool, events.VenueV3),
		},
		100, blockHash(100)))

	// Lowercase input (as copied from an explorer URL) must still match
	// the checksummed stored form.
	transfers, err := s.Transfers(ctx, TransferFilter{Token: strings.ToLower(token)})
	require.NoError(t, err)
	assert.Len(t, transfers, 1, "lowercase token filter must match checksummed storage")

	// Unprefixed hex is a valid address form (go-ethereum semantics).
	transfers, err = s.Transfers(ctx, TransferFilter{From: strings.ToUpper(strings.TrimPrefix(from, "0x"))})
	require.NoError(t, err)
	assert.Len(t, transfers, 1, "unprefixed hex address is normalized and matches")

	// True garbage passes through unchanged: matches nothing, no error.
	transfers, err = s.Transfers(ctx, TransferFilter{From: "not-an-address"})
	require.NoError(t, err)
	assert.Empty(t, transfers)

	transfers, err = s.Transfers(ctx, TransferFilter{From: strings.ToLower(from)})
	require.NoError(t, err)
	assert.Len(t, transfers, 1)

	swaps, err := s.Swaps(ctx, SwapFilter{Pool: strings.ToLower(pool)})
	require.NoError(t, err)
	assert.Len(t, swaps, 1, "lowercase pool filter must match checksummed storage")
}

func TestQueryFiltersAndPagination(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	for block := uint64(100); block <= 102; block++ {
		require.NoError(t, s.WriteBatch(ctx, testChainID, nil,
			[]events.Event{
				transferEvent(block, 0, addr(10), addr(11), addr(12), "1"),
				swapEvent(block, 1, addr(20), events.VenueV2),
			},
			block, blockHash(block)))
	}
	require.NoError(t, s.WriteBatch(ctx, testChainID, nil,
		[]events.Event{swapEvent(103, 0, addr(20), events.VenueV3)},
		103, blockHash(103)))

	// Newest first.
	transfers, err := s.Transfers(ctx, TransferFilter{})
	require.NoError(t, err)
	require.Len(t, transfers, 3)
	assert.Equal(t, uint64(102), transfers[0].BlockNumber)
	assert.Equal(t, uint64(100), transfers[2].BlockNumber)

	// beforeBlock is an exclusive cursor for keyset pagination.
	transfers, err = s.Transfers(ctx, TransferFilter{BeforeBlock: 102})
	require.NoError(t, err)
	require.Len(t, transfers, 2)
	assert.Equal(t, uint64(101), transfers[0].BlockNumber)

	// Limit caps the page size.
	transfers, err = s.Transfers(ctx, TransferFilter{Limit: 1})
	require.NoError(t, err)
	require.Len(t, transfers, 1)
	assert.Equal(t, uint64(102), transfers[0].BlockNumber)

	// Venue filter separates V2 from V3.
	swaps, err := s.Swaps(ctx, SwapFilter{Venue: "v3"})
	require.NoError(t, err)
	require.Len(t, swaps, 1)
	assert.Equal(t, events.VenueV3, swaps[0].Venue)

	swaps, err = s.Swaps(ctx, SwapFilter{Venue: "v2"})
	require.NoError(t, err)
	assert.Len(t, swaps, 3)
}
