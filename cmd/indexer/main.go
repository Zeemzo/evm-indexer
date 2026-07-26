// Command indexer runs the Polygon event indexer: a block poller that
// decodes ERC-20 transfers and Uniswap V2/V3 swaps into Postgres, and an
// HTTP server exposing REST queries, WebSocket streaming, and metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"golang.org/x/sync/errgroup"

	"github.com/zeemzo/evm-indexer/internal/api"
	"github.com/zeemzo/evm-indexer/internal/chain"
	"github.com/zeemzo/evm-indexer/internal/config"
	"github.com/zeemzo/evm-indexer/internal/events"
	"github.com/zeemzo/evm-indexer/internal/indexer"
	"github.com/zeemzo/evm-indexer/internal/metrics"
	"github.com/zeemzo/evm-indexer/internal/pubsub"
	"github.com/zeemzo/evm-indexer/internal/storage"
	"github.com/zeemzo/evm-indexer/internal/stream"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	log.Info("starting evm indexer",
		"chain", cfg.Chain, "chainId", cfg.ChainID, "rpc", cfg.RPCURL,
		"safeHead", safeHeadMode(cfg), "batchSize", cfg.BatchSize)

	// Root context cancelled by SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := metrics.New(registry)

	pool, err := storage.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := storage.Migrate(ctx, pool, log); err != nil {
		return err
	}
	store := storage.NewStore(pool)

	client, err := chain.Dial(ctx, cfg.RPCURL, cfg.RPCTimeout, m, log)
	if err != nil {
		return err
	}
	defer client.Close()

	broker := pubsub.NewBroker[events.Event](256)
	defer broker.Close()

	// Event streaming is optional: with REDIS_URL set, decoded events are
	// also published to a Redis Stream for downstream consumers; without
	// it, the indexer runs on its in-process WebSocket fan-out alone.
	publisher, err := newPublisher(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer publisher.Close()

	ix := indexer.New(cfg, client, store, broker, publisher, m, log)
	server := api.NewServer(cfg.ListenAddr, store, ix, broker, m, registry, cfg.ChainID, log)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return ix.Run(gctx)
	})

	g.Go(func() error {
		log.Info("http server listening", "addr", cfg.ListenAddr)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	// Shut the HTTP server down gracefully once anything cancels the group.
	g.Go(func() error {
		<-gctx.Done()
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// newPublisher builds the event-stream publisher, or a no-op when no
// REDIS_URL is configured.
func newPublisher(ctx context.Context, cfg *config.Config, log *slog.Logger) (stream.Publisher, error) {
	if cfg.RedisURL == "" {
		log.Info("event streaming disabled (no REDIS_URL)")
		return stream.Noop{}, nil
	}
	pub, err := stream.NewRedisPublisher(ctx, cfg.RedisURL, cfg.StreamName, 1_000_000)
	if err != nil {
		return nil, err
	}
	log.Info("event streaming enabled", "stream", cfg.StreamName)
	return pub, nil
}

// safeHeadMode describes, for the startup log, how the safe head is
// derived: the `finalized` tag or a fixed confirmation depth.
func safeHeadMode(cfg *config.Config) string {
	if cfg.UseFinalized {
		return "finalized-tag"
	}
	return fmt.Sprintf("head-%d", cfg.Confirmations)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
