// Package indexer contains the polling loop that drives the pipeline:
// fetch logs → decode → store atomically → publish. It only indexes
// blocks at least Confirmations behind head, and verifies the stored
// checkpoint hash every cycle so a reorg deeper than the confirmation
// window is detected and rewound instead of silently corrupting data.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"golang.org/x/sync/errgroup"

	"github.com/zeemzo/evm-indexer/internal/chain"
	"github.com/zeemzo/evm-indexer/internal/config"
	"github.com/zeemzo/evm-indexer/internal/events"
	"github.com/zeemzo/evm-indexer/internal/metrics"
	"github.com/zeemzo/evm-indexer/internal/pubsub"
	"github.com/zeemzo/evm-indexer/internal/storage"
	"github.com/zeemzo/evm-indexer/internal/stream"
)

// Status is a point-in-time snapshot for the /status endpoint.
type Status struct {
	Running          bool      `json:"running"`
	CaughtUp         bool      `json:"caughtUp"`
	LastIndexedBlock uint64    `json:"lastIndexedBlock"`
	ChainHeadBlock   uint64    `json:"chainHeadBlock"`
	BlocksBehind     uint64    `json:"blocksBehind"`
	ErrorCount       uint64    `json:"consecutiveErrors"`
	ReorgCount       uint64    `json:"reorgsDetected"`
	StartedAt        time.Time `json:"startedAt"`
	UptimeSeconds    float64   `json:"uptimeSeconds"`
}

// Store is the persistence the indexer needs; *storage.Store satisfies
// it. Declared here (consumer side) so tests can substitute a fake.
type Store interface {
	GetCheckpoint(ctx context.Context, chainID uint64) (storage.Checkpoint, error)
	WriteBatch(ctx context.Context, chainID uint64, rawLogs []events.RawLog, evts []events.Event, toBlock uint64, toBlockHash string) error
	Rewind(ctx context.Context, chainID, toBlock uint64, toBlockHash string) error
}

type Indexer struct {
	cfg       *config.Config
	client    chain.Client
	store     Store
	broker    *pubsub.Broker[events.Event]
	publisher stream.Publisher
	metrics   *metrics.Metrics
	log       *slog.Logger

	mu     sync.RWMutex
	status Status
}

func New(cfg *config.Config, client chain.Client, store Store, broker *pubsub.Broker[events.Event], publisher stream.Publisher, m *metrics.Metrics, log *slog.Logger) *Indexer {
	if publisher == nil {
		publisher = stream.Noop{}
	}
	return &Indexer{
		cfg:       cfg,
		client:    client,
		store:     store,
		broker:    broker,
		publisher: publisher,
		metrics:   m,
		log:       log,
	}
}

// Run blocks until ctx is cancelled, driving poll cycles back to back
// while behind and easing off to PollInterval once caught up. After 10
// consecutive failed cycles it returns the last error, letting the
// process crash and the orchestrator (Docker, k8s) restart it clean.
func (ix *Indexer) Run(ctx context.Context) error {
	ix.setStatus(func(s *Status) {
		s.Running = true
		s.StartedAt = time.Now()
	})
	defer ix.setStatus(func(s *Status) { s.Running = false })

	const maxConsecutiveErrors = 10

	for {
		caughtUp, err := ix.cycle(ctx)
		switch {
		case errors.Is(err, context.Canceled):
			return nil
		case err != nil:
			ix.setStatus(func(s *Status) { s.ErrorCount++ })
			errCount := ix.Status().ErrorCount
			ix.log.Error("poll cycle failed", "error", err, "consecutiveErrors", errCount)
			if errCount >= maxConsecutiveErrors {
				return fmt.Errorf("aborting after %d consecutive failed cycles: %w", errCount, err)
			}
		default:
			ix.setStatus(func(s *Status) { s.ErrorCount = 0 })
		}

		// Behind and healthy: poll again immediately.
		if !caughtUp && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(ix.cfg.PollInterval):
		}
	}
}

// cycle runs one fetch→decode→store round. It returns caughtUp=true when
// there was nothing new to index.
func (ix *Indexer) cycle(ctx context.Context) (caughtUp bool, err error) {
	start := time.Now()

	head, err := ix.client.BlockNumber(ctx)
	if err != nil {
		return false, fmt.Errorf("get head: %w", err)
	}
	ix.metrics.ChainHeadBlock.Set(float64(head))

	safeHead, err := ix.safeHead(ctx, head)
	if err != nil {
		return false, err
	}
	if safeHead == 0 {
		return true, nil
	}

	from, err := ix.resolveFrom(ctx, safeHead)
	if err != nil {
		return false, err
	}
	ix.updateLag(safeHead)
	if from > safeHead {
		ix.setStatus(func(s *Status) { s.CaughtUp = true; s.ChainHeadBlock = head })
		return true, nil
	}

	to := min(safeHead, from+ix.cfg.MaxBlocksPerCycle-1)

	logs, err := ix.fetchLogs(ctx, from, to)
	if err != nil {
		return false, fmt.Errorf("fetch logs %d-%d: %w", from, to, err)
	}

	headers, err := ix.fetchHeaders(ctx, logs, to)
	if err != nil {
		return false, fmt.Errorf("fetch headers: %w", err)
	}

	rawLogs, decoded := ix.decodeAll(logs, headers)

	toHash := headers[to].Hash().Hex()
	if err := ix.store.WriteBatch(ctx, ix.cfg.ChainID, rawLogs, decoded, to, toHash); err != nil {
		return false, fmt.Errorf("write batch %d-%d: %w", from, to, err)
	}

	for _, e := range decoded {
		ix.metrics.EventsIndexed.WithLabelValues(string(e.Type)).Inc()
		ix.broker.Publish(e)
		ix.publishToStream(ctx, e)
	}

	ix.metrics.LastIndexedBlock.Set(float64(to))
	ix.metrics.CycleDuration.Observe(time.Since(start).Seconds())
	ix.setStatus(func(s *Status) {
		s.LastIndexedBlock = to
		s.ChainHeadBlock = head
		s.BlocksBehind = safeHead - to
		s.CaughtUp = to == safeHead
	})

	ix.log.Info("indexed range",
		"from", from, "to", to,
		"logs", len(rawLogs), "events", len(decoded),
		"behind", safeHead-to, "took", time.Since(start).Round(time.Millisecond))

	return to == safeHead, nil
}

// publishToStream best-effort publishes an event to the durable bus.
// Postgres is the source of truth, so a broker outage must never fail a
// cycle: we record the outcome and move on. A consumer that missed
// events can always backfill from the database.
func (ix *Indexer) publishToStream(ctx context.Context, e events.Event) {
	if err := ix.publisher.Publish(ctx, e); err != nil {
		ix.metrics.StreamPublished.WithLabelValues("error").Inc()
		ix.log.Warn("stream publish failed", "error", err, "block", e.BlockNumber())
		return
	}
	ix.metrics.StreamPublished.WithLabelValues("success").Inc()
}

// safeHead returns the highest block considered safe to index this
// cycle, or 0 when nothing is safe yet. In finalized mode it is the
// chain's `finalized` tag (unreorgable on a PoS chain); otherwise it is
// head minus the confirmation depth.
func (ix *Indexer) safeHead(ctx context.Context, head uint64) (uint64, error) {
	if ix.cfg.UseFinalized {
		finalized, err := ix.client.FinalizedNumber(ctx)
		if err != nil {
			return 0, fmt.Errorf("get finalized head: %w", err)
		}
		return finalized, nil
	}
	if head < ix.cfg.Confirmations {
		return 0, nil
	}
	return head - ix.cfg.Confirmations, nil
}

// resolveFrom determines the next block to index. On first run it seeds
// from config; otherwise it verifies the checkpoint hash against the
// chain and rewinds if the block we last indexed no longer exists.
func (ix *Indexer) resolveFrom(ctx context.Context, safeHead uint64) (uint64, error) {
	cp, err := ix.store.GetCheckpoint(ctx, ix.cfg.ChainID)
	if errors.Is(err, storage.ErrNoCheckpoint) {
		if ix.cfg.StartBlock > 0 {
			return ix.cfg.StartBlock, nil
		}
		// No history requested: start at the current safe head.
		return safeHead, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load checkpoint: %w", err)
	}

	header, err := ix.client.HeaderByNumber(ctx, cp.LastBlock)
	if err != nil {
		return 0, fmt.Errorf("verify checkpoint header %d: %w", cp.LastBlock, err)
	}
	if header.Hash().Hex() == cp.LastHash {
		return cp.LastBlock + 1, nil
	}

	// The block we indexed was reorged out. Rewind well past the fork
	// point and re-index; idempotent writes make the replay safe.
	return ix.rewind(ctx, cp)
}

func (ix *Indexer) rewind(ctx context.Context, cp storage.Checkpoint) (uint64, error) {
	target := uint64(0)
	if cp.LastBlock > ix.cfg.ReorgRewind {
		target = cp.LastBlock - ix.cfg.ReorgRewind
	}
	if target < ix.cfg.StartBlock {
		target = ix.cfg.StartBlock
	}

	header, err := ix.client.HeaderByNumber(ctx, target)
	if err != nil {
		return 0, fmt.Errorf("fetch rewind header %d: %w", target, err)
	}
	if err := ix.store.Rewind(ctx, ix.cfg.ChainID, target, header.Hash().Hex()); err != nil {
		return 0, fmt.Errorf("rewind storage to %d: %w", target, err)
	}

	ix.metrics.ReorgsDetected.Inc()
	ix.setStatus(func(s *Status) { s.ReorgCount++ })
	ix.log.Warn("reorg detected, rewound",
		"checkpointBlock", cp.LastBlock, "checkpointHash", cp.LastHash, "rewoundTo", target)
	return target + 1, nil
}

// fetchLogs splits [from, to] into BatchSize chunks and fetches them in
// parallel goroutines, bounded by FetchConcurrency. The RPC node filters
// by topic0 server-side, so only decodable logs cross the wire.
func (ix *Indexer) fetchLogs(ctx context.Context, from, to uint64) ([]types.Log, error) {
	type chunk struct{ from, to uint64 }
	var chunks []chunk
	for start := from; start <= to; start += ix.cfg.BatchSize {
		chunks = append(chunks, chunk{start, min(start+ix.cfg.BatchSize-1, to)})
	}

	results := make([][]types.Log, len(chunks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(ix.cfg.FetchConcurrency)
	for i, c := range chunks {
		g.Go(func() error {
			logs, err := ix.client.FilterLogs(gctx, ethereum.FilterQuery{
				FromBlock: new(big.Int).SetUint64(c.from),
				ToBlock:   new(big.Int).SetUint64(c.to),
				Topics:    chain.TopicFilter(),
			})
			if err != nil {
				return err
			}
			results[i] = logs
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var all []types.Log
	for _, logs := range results {
		for _, l := range logs {
			if l.Removed {
				continue
			}
			all = append(all, l)
		}
	}
	return all, nil
}

// fetchHeaders resolves timestamps for every block that has logs, plus
// the range-end block whose hash anchors the checkpoint. Fetches run in
// parallel with the same concurrency bound as log fetching.
func (ix *Indexer) fetchHeaders(ctx context.Context, logs []types.Log, to uint64) (map[uint64]*types.Header, error) {
	need := map[uint64]struct{}{to: {}}
	for _, l := range logs {
		need[l.BlockNumber] = struct{}{}
	}

	var mu sync.Mutex
	headers := make(map[uint64]*types.Header, len(need))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(ix.cfg.FetchConcurrency)
	for number := range need {
		g.Go(func() error {
			h, err := ix.client.HeaderByNumber(gctx, number)
			if err != nil {
				return fmt.Errorf("header %d: %w", number, err)
			}
			mu.Lock()
			headers[number] = h
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return headers, nil
}

// decodeAll converts raw logs into audit rows and typed events, ordered
// by (block, logIndex) so downstream consumers see chain order.
func (ix *Indexer) decodeAll(logs []types.Log, headers map[uint64]*types.Header) ([]events.RawLog, []events.Event) {
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].BlockNumber != logs[j].BlockNumber {
			return logs[i].BlockNumber < logs[j].BlockNumber
		}
		return logs[i].Index < logs[j].Index
	})

	rawLogs := make([]events.RawLog, 0, len(logs))
	decoded := make([]events.Event, 0, len(logs))
	for _, l := range logs {
		blockTime := time.Unix(int64(headers[l.BlockNumber].Time), 0).UTC()
		rawLogs = append(rawLogs, chain.ToRawLog(l, blockTime))
		if evt, ok := chain.Decode(l, blockTime); ok {
			decoded = append(decoded, evt)
		}
	}
	return rawLogs, decoded
}

func (ix *Indexer) Status() Status {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := ix.status
	if !s.StartedAt.IsZero() {
		s.UptimeSeconds = time.Since(s.StartedAt).Seconds()
	}
	return s
}

func (ix *Indexer) setStatus(mutate func(*Status)) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	mutate(&ix.status)
}

func (ix *Indexer) updateLag(safeHead uint64) {
	s := ix.Status()
	if safeHead > s.LastIndexedBlock {
		ix.metrics.BlocksBehind.Set(float64(safeHead - s.LastIndexedBlock))
	} else {
		ix.metrics.BlocksBehind.Set(0)
	}
}
