// Package events defines the decoded event model shared by the decoder,
// storage, API, and streaming layers. Numeric token amounts are kept as
// decimal strings: uint256 does not fit in any native type, and strings
// survive JSON encoding without precision loss.
package events

import "time"

type Type string

const (
	TypeTransfer Type = "erc20_transfer"
	TypeSwap     Type = "swap"
)

// Venue distinguishes the two swap event layouts we decode.
type Venue string

const (
	VenueV2 Venue = "v2"
	VenueV3 Venue = "v3"
)

// RawLog is the untouched on-chain log, persisted as an audit trail so
// new event types can be backfilled later without re-fetching the chain.
type RawLog struct {
	BlockNumber uint64    `json:"blockNumber"`
	BlockTime   time.Time `json:"blockTime"`
	TxHash      string    `json:"txHash"`
	LogIndex    uint32    `json:"logIndex"`
	Address     string    `json:"address"`
	Topics      []string  `json:"topics"`
	Data        []byte    `json:"-"`
}

type Transfer struct {
	BlockNumber uint64    `json:"blockNumber"`
	BlockTime   time.Time `json:"blockTime"`
	TxHash      string    `json:"txHash"`
	LogIndex    uint32    `json:"logIndex"`
	Token       string    `json:"tokenAddress"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Value       string    `json:"value"`
}

type Swap struct {
	BlockNumber uint64    `json:"blockNumber"`
	BlockTime   time.Time `json:"blockTime"`
	TxHash      string    `json:"txHash"`
	LogIndex    uint32    `json:"logIndex"`
	Pool        string    `json:"poolAddress"`
	Sender      string    `json:"sender"`
	Recipient   string    `json:"recipient"`
	Amount0In   string    `json:"amount0In"`
	Amount1In   string    `json:"amount1In"`
	Amount0Out  string    `json:"amount0Out"`
	Amount1Out  string    `json:"amount1Out"`
	Venue       Venue     `json:"venue"`
}

// Event is a tagged union over the decoded event kinds. Exactly one of
// Transfer/Swap is non-nil, matching Type.
type Event struct {
	Type     Type      `json:"type"`
	Transfer *Transfer `json:"transfer,omitempty"`
	Swap     *Swap     `json:"swap,omitempty"`
}

// BlockNumber returns the block the underlying event was emitted in.
func (e Event) BlockNumber() uint64 {
	switch e.Type {
	case TypeTransfer:
		return e.Transfer.BlockNumber
	case TypeSwap:
		return e.Swap.BlockNumber
	}
	return 0
}
