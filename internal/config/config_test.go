package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearEnv unsets every variable Load reads so a test starts from a clean
// slate regardless of the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CHAIN", "DATABASE_URL", "RPC_URL", "CHAIN_ID", "START_BLOCK",
		"CONFIRMATIONS", "USE_FINALIZED", "BATCH_SIZE", "MAX_BLOCKS_PER_CYCLE",
		"FETCH_CONCURRENCY", "REORG_REWIND", "POLL_INTERVAL", "RPC_TIMEOUT",
		"LISTEN_ADDR", "LOG_LEVEL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaultsToPolygon(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "polygon", cfg.Chain)
	assert.Equal(t, uint64(137), cfg.ChainID)
	assert.False(t, cfg.UseFinalized)
	assert.Equal(t, uint64(64), cfg.Confirmations)
}

func TestLoadEthereumPreset(t *testing.T) {
	clearEnv(t)
	t.Setenv("CHAIN", "ethereum")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "ethereum", cfg.Chain)
	assert.Equal(t, uint64(1), cfg.ChainID)
	assert.True(t, cfg.UseFinalized, "ethereum preset must default to finalized-tag indexing")
	assert.Equal(t, "https://eth.drpc.org", cfg.RPCURL)
	assert.Equal(t, 12*time.Second, cfg.PollInterval)
	assert.Equal(t, uint64(200), cfg.BatchSize)
}

func TestLoadCaseInsensitiveChain(t *testing.T) {
	clearEnv(t)
	t.Setenv("CHAIN", "Ethereum")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, uint64(1), cfg.ChainID)
}

func TestLoadEnvOverridesPreset(t *testing.T) {
	clearEnv(t)
	t.Setenv("CHAIN", "ethereum")
	t.Setenv("RPC_URL", "https://my-private-node.example")
	t.Setenv("USE_FINALIZED", "false")
	t.Setenv("CONFIRMATIONS", "12")
	t.Setenv("BATCH_SIZE", "1000")
	cfg, err := Load()
	require.NoError(t, err)

	// Preset identity is preserved, but overridden fields win.
	assert.Equal(t, "ethereum", cfg.Chain)
	assert.Equal(t, uint64(1), cfg.ChainID, "unoverridden preset fields remain")
	assert.Equal(t, "https://my-private-node.example", cfg.RPCURL)
	assert.False(t, cfg.UseFinalized)
	assert.Equal(t, uint64(12), cfg.Confirmations)
	assert.Equal(t, uint64(1000), cfg.BatchSize)
}

func TestLoadUnknownChainErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("CHAIN", "dogecoin")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown CHAIN")
}

func TestLoadValidatesBatchVsCycle(t *testing.T) {
	clearEnv(t)
	t.Setenv("BATCH_SIZE", "6000")
	t.Setenv("MAX_BLOCKS_PER_CYCLE", "5000")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MAX_BLOCKS_PER_CYCLE")
}
