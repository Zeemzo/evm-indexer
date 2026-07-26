package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ChainPreset bundles the per-chain defaults that genuinely differ
// between EVM networks. Selecting a preset (via the CHAIN env var) seeds
// these values; any individual env var still overrides its field, so a
// preset is a convenient starting point, never a straitjacket.
//
// The two "safe head" strategies encode a real correctness trade-off:
//
//   - UseFinalized: index only up to the chain's `finalized` block tag.
//     Post-Merge Ethereum and its L2s expose cheap, fast PoS finality, so
//     finalized blocks can never be reorged — the strongest guarantee.
//
//   - Confirmations: index N blocks behind head. Used where a practical
//     finalized tag lags too far or isn't reliable (Polygon's checkpoint
//     finality can trail by minutes). The checkpoint hash-verification +
//     rewind path is the safety net that catches anything within N.
type ChainPreset struct {
	Name          string
	ChainID       uint64
	RPCURL        string
	Confirmations uint64
	PollInterval  time.Duration
	UseFinalized  bool
	// BatchSize overrides the global getLogs block-span default. Denser
	// chains (Ethereum L1) use a smaller span to stay under public-RPC
	// result caps; 0 keeps the global default.
	BatchSize uint64
}

// presets are keyed by the lowercase CHAIN value. All are EVM chains the
// indexer runs against unchanged — only these defaults vary. "ethereum"
// and "polygon" are the live-verified pair; the L2 presets are provided
// for convenience and share the identical code path.
var presets = map[string]ChainPreset{
	"ethereum": {
		Name:          "ethereum",
		ChainID:       1,
		RPCURL:        "https://eth.drpc.org",
		Confirmations: 32,
		PollInterval:  12 * time.Second,
		UseFinalized:  true,
		BatchSize:     200,
	},
	"polygon": {
		Name:          "polygon",
		ChainID:       137,
		RPCURL:        "https://polygon.drpc.org",
		Confirmations: 64,
		PollInterval:  2 * time.Second,
		UseFinalized:  false,
		BatchSize:     500,
	},
	"arbitrum": {
		Name:          "arbitrum",
		ChainID:       42161,
		RPCURL:        "https://arbitrum.drpc.org",
		Confirmations: 20,
		PollInterval:  time.Second,
		UseFinalized:  true,
		BatchSize:     500,
	},
	"optimism": {
		Name:          "optimism",
		ChainID:       10,
		RPCURL:        "https://optimism.drpc.org",
		Confirmations: 20,
		PollInterval:  2 * time.Second,
		UseFinalized:  true,
		BatchSize:     500,
	},
	"base": {
		Name:          "base",
		ChainID:       8453,
		RPCURL:        "https://base.drpc.org",
		Confirmations: 20,
		PollInterval:  2 * time.Second,
		UseFinalized:  true,
		BatchSize:     500,
	},
}

// lookupPreset returns the preset for name (case-insensitive). The empty
// string selects the default so an unconfigured deploy keeps working.
func lookupPreset(name string) (ChainPreset, error) {
	if name == "" {
		name = defaultChain
	}
	p, ok := presets[strings.ToLower(name)]
	if !ok {
		return ChainPreset{}, fmt.Errorf("unknown CHAIN %q; known chains: %s", name, knownChains())
	}
	return p, nil
}

func knownChains() string {
	names := make([]string, 0, len(presets))
	for k := range presets {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
