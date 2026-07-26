// Package config loads and validates all runtime configuration from the
// environment. Selecting a chain with CHAIN=<name> seeds sensible
// per-network defaults (see chains.go); every field can still be
// overridden by its own env var, so the service boots with nothing but a
// CHAIN (or the built-in default) and a DATABASE_URL.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// defaultChain is used when CHAIN is unset. This repo began life as a
// Polygon indexer, so Polygon stays the default for backward compatibility.
const defaultChain = "polygon"

type Config struct {
	// Chain is the selected preset name (ethereum, polygon, ...).
	Chain string
	// DatabaseURL is the Postgres connection string.
	DatabaseURL string
	// RPCURL is the JSON-RPC endpoint for the selected chain.
	RPCURL string
	// ChainID is used as the checkpoint key so one database can serve
	// multiple chains without collisions (Ethereum = 1, Polygon = 137).
	ChainID uint64
	// StartBlock is where indexing begins when no checkpoint exists yet.
	StartBlock uint64
	// Confirmations is how many blocks behind head we index when
	// UseFinalized is false. Staying N blocks back makes most reorgs
	// invisible to us, and the checkpoint hash check catches the rest.
	Confirmations uint64
	// UseFinalized indexes only up to the chain's `finalized` block tag
	// instead of head-minus-Confirmations. On PoS chains that exposes
	// true finality: indexed blocks can never be reorged.
	UseFinalized bool
	// BatchSize is the max block span per eth_getLogs call.
	BatchSize uint64
	// MaxBlocksPerCycle caps how far one poll cycle advances, bounding
	// memory and transaction size during backfill.
	MaxBlocksPerCycle uint64
	// FetchConcurrency is how many getLogs chunks are fetched in parallel.
	FetchConcurrency int
	// ReorgRewind is how many blocks we rewind when a reorg is detected.
	ReorgRewind uint64
	// PollInterval is the delay between poll cycles once caught up.
	PollInterval time.Duration
	// RPCTimeout bounds every individual RPC call.
	RPCTimeout time.Duration
	// ListenAddr is the HTTP listen address for API + metrics.
	ListenAddr string
	// LogLevel is one of debug|info|warn|error.
	LogLevel string
}

func Load() (*Config, error) {
	// Pick the chain preset first; its fields become the defaults that
	// individual env vars below can still override.
	preset, err := lookupPreset(os.Getenv("CHAIN"))
	if err != nil {
		return nil, err
	}

	batchDefault := uint64(500)
	if preset.BatchSize > 0 {
		batchDefault = preset.BatchSize
	}

	cfg := &Config{
		Chain:             preset.Name,
		DatabaseURL:       getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/polygon_indexer?sslmode=disable"),
		RPCURL:            getEnv("RPC_URL", preset.RPCURL),
		ChainID:           getEnvUint("CHAIN_ID", preset.ChainID),
		StartBlock:        getEnvUint("START_BLOCK", 0),
		Confirmations:     getEnvUint("CONFIRMATIONS", preset.Confirmations),
		UseFinalized:      getEnvBool("USE_FINALIZED", preset.UseFinalized),
		BatchSize:         getEnvUint("BATCH_SIZE", batchDefault),
		MaxBlocksPerCycle: getEnvUint("MAX_BLOCKS_PER_CYCLE", 5000),
		FetchConcurrency:  int(getEnvUint("FETCH_CONCURRENCY", 4)),
		ReorgRewind:       getEnvUint("REORG_REWIND", 256),
		PollInterval:      getEnvDuration("POLL_INTERVAL", preset.PollInterval),
		RPCTimeout:        getEnvDuration("RPC_TIMEOUT", 30*time.Second),
		ListenAddr:        getEnv("LISTEN_ADDR", ":8080"),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
	}

	if cfg.BatchSize == 0 {
		return nil, fmt.Errorf("BATCH_SIZE must be > 0")
	}
	if cfg.FetchConcurrency < 1 {
		return nil, fmt.Errorf("FETCH_CONCURRENCY must be >= 1")
	}
	if cfg.MaxBlocksPerCycle < cfg.BatchSize {
		return nil, fmt.Errorf("MAX_BLOCKS_PER_CYCLE (%d) must be >= BATCH_SIZE (%d)", cfg.MaxBlocksPerCycle, cfg.BatchSize)
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvUint(key string, fallback uint64) uint64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
