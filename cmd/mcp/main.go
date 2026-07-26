// Command mcp runs a Model Context Protocol server over stdio, exposing
// the indexer's on-chain data (transfers, swaps, stats) as tools an AI
// agent can call. Point Claude Desktop / Claude Code / Cursor at this
// binary with DATABASE_URL set to the indexer's database.
//
// Logs go to stderr so they never corrupt the JSON-RPC stream on stdout.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zeemzo/evm-indexer/internal/config"
	"github.com/zeemzo/evm-indexer/internal/mcp"
	"github.com/zeemzo/evm-indexer/internal/storage"
)

const version = "1.0.0"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := storage.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := storage.NewStore(pool)
	srv := mcp.NewServer(store, cfg.ChainID, "evm-indexer", version, log)

	log.Info("mcp server ready on stdio", "chain", cfg.Chain, "chainId", cfg.ChainID)
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}
