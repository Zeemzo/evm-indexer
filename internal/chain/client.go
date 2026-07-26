// Package chain wraps go-ethereum's ethclient with retries, per-call
// timeouts, and Prometheus instrumentation. The indexer talks to the
// chain exclusively through this interface, which keeps the poller
// trivially mockable in tests.
package chain

import (
	"context"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	gethrpc "github.com/ethereum/go-ethereum/rpc"

	"github.com/zeemzo/evm-indexer/internal/metrics"
	"github.com/zeemzo/evm-indexer/pkg/retry"
)

// Client is the subset of chain access the indexer needs.
type Client interface {
	BlockNumber(ctx context.Context) (uint64, error)
	// FinalizedNumber returns the number of the chain's `finalized`
	// block tag — the highest block guaranteed not to be reorged on a
	// PoS chain. Used when UseFinalized is set.
	FinalizedNumber(ctx context.Context) (uint64, error)
	HeaderByNumber(ctx context.Context, number uint64) (*types.Header, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	Close()
}

type client struct {
	eth     *ethclient.Client
	timeout time.Duration
	retry   retry.Options
	metrics *metrics.Metrics
	log     *slog.Logger
}

func Dial(ctx context.Context, rpcURL string, timeout time.Duration, m *metrics.Metrics, log *slog.Logger) (Client, error) {
	eth, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, err
	}
	opts := retry.DefaultOptions()
	opts.OnRetry = func(attempt int, err error) {
		log.Warn("rpc call retrying", "attempt", attempt, "error", err)
	}
	return &client{eth: eth, timeout: timeout, retry: opts, metrics: m, log: log}, nil
}

// call wraps an RPC invocation with timeout, retry, and metrics.
func call[T any](ctx context.Context, c *client, method string, fn func(ctx context.Context) (T, error)) (T, error) {
	return retry.Do(ctx, c.retry, func(ctx context.Context) (T, error) {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()

		start := time.Now()
		result, err := fn(callCtx)
		c.metrics.RPCDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
		outcome := "success"
		if err != nil {
			outcome = "error"
		}
		c.metrics.RPCCalls.WithLabelValues(method, outcome).Inc()
		return result, err
	})
}

func (c *client) BlockNumber(ctx context.Context) (uint64, error) {
	return call(ctx, c, "eth_blockNumber", c.eth.BlockNumber)
}

func (c *client) FinalizedNumber(ctx context.Context) (uint64, error) {
	return call(ctx, c, "eth_getBlockByNumber[finalized]", func(ctx context.Context) (uint64, error) {
		// A negative big.Int is encoded by ethclient as a named block
		// tag; FinalizedBlockNumber (-3) maps to "finalized".
		h, err := c.eth.HeaderByNumber(ctx, big.NewInt(int64(gethrpc.FinalizedBlockNumber)))
		if err != nil {
			return 0, err
		}
		return h.Number.Uint64(), nil
	})
}

func (c *client) HeaderByNumber(ctx context.Context, number uint64) (*types.Header, error) {
	return call(ctx, c, "eth_getBlockByNumber", func(ctx context.Context) (*types.Header, error) {
		return c.eth.HeaderByNumber(ctx, new(big.Int).SetUint64(number))
	})
}

func (c *client) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	return call(ctx, c, "eth_getLogs", func(ctx context.Context) ([]types.Log, error) {
		return c.eth.FilterLogs(ctx, q)
	})
}

func (c *client) Close() {
	c.eth.Close()
}

// TopicFilter returns the topic0 filter for all event signatures the
// decoder understands, so the RPC node filters server-side and we never
// ship irrelevant logs over the wire.
func TopicFilter() [][]common.Hash {
	return [][]common.Hash{{
		TransferTopic,
		SwapV2Topic,
		SwapV3Topic,
	}}
}
