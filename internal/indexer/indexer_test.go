package indexer

import (
	"context"
	"io"
	"log/slog"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zeemzo/evm-indexer/internal/chain"
	"github.com/zeemzo/evm-indexer/internal/config"
	"github.com/zeemzo/evm-indexer/internal/events"
	"github.com/zeemzo/evm-indexer/internal/metrics"
	"github.com/zeemzo/evm-indexer/internal/pubsub"
	"github.com/zeemzo/evm-indexer/internal/storage"
)

// fakeChain simulates an EVM node: a head height, deterministic headers,
// and a set of logs keyed by block number.
type fakeChain struct {
	mu   sync.Mutex
	head uint64
	// finalized is the block number returned for the `finalized` tag;
	// 0 means "track head" for tests that don't exercise finalized mode.
	finalized uint64
	logs      map[uint64][]types.Log
	// hashSalt lets tests change every block hash, simulating a reorg.
	hashSalt byte
}

func (f *fakeChain) BlockNumber(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}

func (f *fakeChain) FinalizedNumber(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finalized == 0 {
		return f.head, nil
	}
	return f.finalized, nil
}

func (f *fakeChain) HeaderByNumber(ctx context.Context, number uint64) (*types.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &types.Header{
		Number: new(big.Int).SetUint64(number),
		Time:   1_700_000_000 + number*2,
		Extra:  []byte{f.hashSalt},
	}, nil
}

func (f *fakeChain) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []types.Log
	for b := q.FromBlock.Uint64(); b <= q.ToBlock.Uint64(); b++ {
		out = append(out, f.logs[b]...)
	}
	return out, nil
}

func (f *fakeChain) Close() {}

// fakeStore records writes in memory.
type fakeStore struct {
	mu         sync.Mutex
	checkpoint *storage.Checkpoint
	rawLogs    []events.RawLog
	events     []events.Event
	rewinds    []uint64
}

func (f *fakeStore) GetCheckpoint(ctx context.Context, chainID uint64) (storage.Checkpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkpoint == nil {
		return storage.Checkpoint{ChainID: chainID}, storage.ErrNoCheckpoint
	}
	return *f.checkpoint, nil
}

func (f *fakeStore) WriteBatch(ctx context.Context, chainID uint64, rawLogs []events.RawLog, evts []events.Event, toBlock uint64, toBlockHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rawLogs = append(f.rawLogs, rawLogs...)
	f.events = append(f.events, evts...)
	f.checkpoint = &storage.Checkpoint{ChainID: chainID, LastBlock: toBlock, LastHash: toBlockHash, UpdatedAt: time.Now()}
	return nil
}

func (f *fakeStore) Rewind(ctx context.Context, chainID, toBlock uint64, toBlockHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rewinds = append(f.rewinds, toBlock)
	kept := f.events[:0]
	for _, e := range f.events {
		if e.BlockNumber() <= toBlock {
			kept = append(kept, e)
		}
	}
	f.events = kept
	f.checkpoint = &storage.Checkpoint{ChainID: chainID, LastBlock: toBlock, LastHash: toBlockHash, UpdatedAt: time.Now()}
	return nil
}

func testConfig() *config.Config {
	return &config.Config{
		ChainID:           137,
		StartBlock:        100,
		Confirmations:     10,
		BatchSize:         50,
		MaxBlocksPerCycle: 1000,
		FetchConcurrency:  2,
		ReorgRewind:       20,
		PollInterval:      time.Millisecond,
	}
}

func newTestIndexer(t *testing.T, cfg *config.Config, fc *fakeChain, fs *fakeStore) (*Indexer, *pubsub.Broker[events.Event]) {
	t.Helper()
	broker := pubsub.NewBroker[events.Event](64)
	t.Cleanup(broker.Close)
	m := metrics.New(prometheus.NewRegistry())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, fc, fs, broker, m, log), broker
}

func transferLog(block uint64, index uint) types.Log {
	value := common.LeftPadBytes(big.NewInt(1000).Bytes(), 32)
	return types.Log{
		Address: common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174"),
		Topics: []common.Hash{
			chain.TransferTopic,
			common.BytesToHash(common.HexToAddress("0x1111111111111111111111111111111111111111").Bytes()),
			common.BytesToHash(common.HexToAddress("0x2222222222222222222222222222222222222222").Bytes()),
		},
		Data:        value,
		BlockNumber: block,
		TxHash:      common.BytesToHash(new(big.Int).SetUint64(block*1000 + uint64(index)).Bytes()),
		Index:       index,
	}
}

func TestCycleIndexesUpToSafeHead(t *testing.T) {
	cfg := testConfig()
	fc := &fakeChain{
		head: 150,
		logs: map[uint64][]types.Log{
			110: {transferLog(110, 0), transferLog(110, 1)},
			135: {transferLog(135, 3)},
		},
	}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	caughtUp, err := ix.cycle(context.Background())
	require.NoError(t, err)
	assert.True(t, caughtUp)

	// safe head = 150 - 10 = 140; checkpoint must land exactly there.
	require.NotNil(t, fs.checkpoint)
	assert.Equal(t, uint64(140), fs.checkpoint.LastBlock)
	assert.NotEmpty(t, fs.checkpoint.LastHash)

	assert.Len(t, fs.rawLogs, 3)
	assert.Len(t, fs.events, 3)
	assert.Equal(t, events.TypeTransfer, fs.events[0].Type)

	// Events must arrive in chain order.
	assert.Equal(t, uint64(110), fs.events[0].BlockNumber())
	assert.Equal(t, uint64(135), fs.events[2].BlockNumber())
}

func TestCycleRespectsConfirmationDepth(t *testing.T) {
	cfg := testConfig()
	fc := &fakeChain{
		head: 150,
		logs: map[uint64][]types.Log{
			// In the unconfirmed window (141-150): must NOT be indexed.
			145: {transferLog(145, 0)},
		},
	}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	_, err := ix.cycle(context.Background())
	require.NoError(t, err)
	assert.Empty(t, fs.events, "events above the safe head must not be indexed yet")

	// Chain advances; the block is now confirmed and gets picked up.
	fc.mu.Lock()
	fc.head = 160
	fc.mu.Unlock()

	_, err = ix.cycle(context.Background())
	require.NoError(t, err)
	assert.Len(t, fs.events, 1)
}

func TestCycleCaughtUpWhenNoNewBlocks(t *testing.T) {
	cfg := testConfig()
	fc := &fakeChain{head: 150}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	_, err := ix.cycle(context.Background())
	require.NoError(t, err)
	first := *fs.checkpoint

	caughtUp, err := ix.cycle(context.Background())
	require.NoError(t, err)
	assert.True(t, caughtUp)
	assert.Equal(t, first.LastBlock, fs.checkpoint.LastBlock, "checkpoint must not move without new blocks")
}

func TestCycleDetectsReorgAndRewinds(t *testing.T) {
	cfg := testConfig()
	fc := &fakeChain{head: 150}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	_, err := ix.cycle(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(140), fs.checkpoint.LastBlock)

	// Simulate a reorg: every block hash on the chain changes, so the
	// stored checkpoint hash no longer matches.
	fc.mu.Lock()
	fc.hashSalt = 0xFF
	fc.head = 155
	fc.mu.Unlock()

	_, err = ix.cycle(context.Background())
	require.NoError(t, err)

	// Rewind target = 140 - ReorgRewind(20) = 120.
	require.Len(t, fs.rewinds, 1)
	assert.Equal(t, uint64(120), fs.rewinds[0])

	// After the rewind, the cycle re-indexed forward to the new safe head.
	assert.Equal(t, uint64(145), fs.checkpoint.LastBlock)
	assert.Equal(t, uint64(1), ix.Status().ReorgCount)

	// Next cycle must be clean — no further rewinds.
	_, err = ix.cycle(context.Background())
	require.NoError(t, err)
	assert.Len(t, fs.rewinds, 1)
}

func TestCycleFinalizedModeIndexesToFinalizedTag(t *testing.T) {
	cfg := testConfig()
	cfg.UseFinalized = true
	cfg.Confirmations = 0 // ignored in finalized mode
	fc := &fakeChain{
		head:      200,
		finalized: 160, // finalized lags head by 40
		logs: map[uint64][]types.Log{
			150: {transferLog(150, 0)},
			165: {transferLog(165, 0)}, // above finalized: must be skipped
		},
	}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	caughtUp, err := ix.cycle(context.Background())
	require.NoError(t, err)
	assert.True(t, caughtUp)

	// Safe head is the finalized tag (160), not head-minus-confirmations.
	require.NotNil(t, fs.checkpoint)
	assert.Equal(t, uint64(160), fs.checkpoint.LastBlock)
	require.Len(t, fs.events, 1, "the log above the finalized tag must not be indexed")
	assert.Equal(t, uint64(150), fs.events[0].BlockNumber())
}

func TestCyclePublishesToBroker(t *testing.T) {
	cfg := testConfig()
	fc := &fakeChain{
		head: 150,
		logs: map[uint64][]types.Log{120: {transferLog(120, 0)}},
	}
	fs := &fakeStore{}
	ix, broker := newTestIndexer(t, cfg, fc, fs)

	sub, cancel := broker.Subscribe()
	defer cancel()

	_, err := ix.cycle(context.Background())
	require.NoError(t, err)

	select {
	case evt := <-sub:
		assert.Equal(t, events.TypeTransfer, evt.Type)
		assert.Equal(t, uint64(120), evt.Transfer.BlockNumber)
	case <-time.After(time.Second):
		t.Fatal("expected event on subscription")
	}
}

func TestCycleHonorsMaxBlocksPerCycle(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBlocksPerCycle = 100
	fc := &fakeChain{head: 1000}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	caughtUp, err := ix.cycle(context.Background())
	require.NoError(t, err)
	assert.False(t, caughtUp, "still behind after a capped cycle")
	// First cycle: from=100 (StartBlock), capped at 100 blocks → 199.
	assert.Equal(t, uint64(199), fs.checkpoint.LastBlock)

	_, err = ix.cycle(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(299), fs.checkpoint.LastBlock)
}

func TestFreshStartWithoutStartBlockBeginsAtSafeHead(t *testing.T) {
	cfg := testConfig()
	cfg.StartBlock = 0
	fc := &fakeChain{
		head: 500,
		logs: map[uint64][]types.Log{
			// History that must be skipped.
			200: {transferLog(200, 0)},
		},
	}
	fs := &fakeStore{}
	ix, _ := newTestIndexer(t, cfg, fc, fs)

	_, err := ix.cycle(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(490), fs.checkpoint.LastBlock)
	assert.Empty(t, fs.events, "historical events must be skipped when no START_BLOCK is set")
}
