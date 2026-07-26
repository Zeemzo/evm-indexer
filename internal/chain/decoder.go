package chain

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/zeemzo/evm-indexer/internal/events"
)

// Event signature hashes (topic0). Computed from the canonical signature
// strings so they can never drift from the ABI they encode.
var (
	TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
	SwapV2Topic   = crypto.Keccak256Hash([]byte("Swap(address,uint256,uint256,uint256,uint256,address)"))
	SwapV3Topic   = crypto.Keccak256Hash([]byte("Swap(address,address,int256,int256,uint160,uint128,int24)"))
)

const wordSize = 32

// Decode turns a raw log into a typed event, or returns ok=false for
// logs we recognize by topic but whose shape doesn't match (e.g. ERC-721
// Transfer shares topic0 with ERC-20 but carries an extra indexed topic).
func Decode(log types.Log, blockTime time.Time) (events.Event, bool) {
	if len(log.Topics) == 0 {
		return events.Event{}, false
	}
	switch log.Topics[0] {
	case TransferTopic:
		return decodeTransfer(log, blockTime)
	case SwapV2Topic:
		return decodeSwapV2(log, blockTime)
	case SwapV3Topic:
		return decodeSwapV3(log, blockTime)
	}
	return events.Event{}, false
}

// decodeTransfer handles ERC-20 Transfer(from indexed, to indexed, value).
// ERC-721 emits the same topic0 with tokenId as a third indexed topic and
// empty data; we deliberately skip those.
func decodeTransfer(log types.Log, blockTime time.Time) (events.Event, bool) {
	if len(log.Topics) != 3 || len(log.Data) != wordSize {
		return events.Event{}, false
	}
	return events.Event{
		Type: events.TypeTransfer,
		Transfer: &events.Transfer{
			BlockNumber: log.BlockNumber,
			BlockTime:   blockTime,
			TxHash:      log.TxHash.Hex(),
			LogIndex:    uint32(log.Index),
			Token:       log.Address.Hex(),
			From:        topicToAddress(log.Topics[1]),
			To:          topicToAddress(log.Topics[2]),
			Value:       word(log.Data, 0).String(),
		},
	}, true
}

// decodeSwapV2 handles Uniswap V2-style
// Swap(sender indexed, amount0In, amount1In, amount0Out, amount1Out, to indexed).
func decodeSwapV2(log types.Log, blockTime time.Time) (events.Event, bool) {
	if len(log.Topics) != 3 || len(log.Data) != 4*wordSize {
		return events.Event{}, false
	}
	return events.Event{
		Type: events.TypeSwap,
		Swap: &events.Swap{
			BlockNumber: log.BlockNumber,
			BlockTime:   blockTime,
			TxHash:      log.TxHash.Hex(),
			LogIndex:    uint32(log.Index),
			Pool:        log.Address.Hex(),
			Sender:      topicToAddress(log.Topics[1]),
			Recipient:   topicToAddress(log.Topics[2]),
			Amount0In:   word(log.Data, 0).String(),
			Amount1In:   word(log.Data, 1).String(),
			Amount0Out:  word(log.Data, 2).String(),
			Amount1Out:  word(log.Data, 3).String(),
			Venue:       events.VenueV2,
		},
	}, true
}

// decodeSwapV3 handles Uniswap V3-style
// Swap(sender indexed, recipient indexed, amount0 int256, amount1 int256,
// sqrtPriceX96, liquidity, tick). V3 reports signed deltas from the
// pool's perspective: positive means the pool received (user paid in),
// negative means the pool sent out. We normalize to the V2 in/out shape
// so consumers see one swap model.
func decodeSwapV3(log types.Log, blockTime time.Time) (events.Event, bool) {
	if len(log.Topics) != 3 || len(log.Data) != 5*wordSize {
		return events.Event{}, false
	}
	amount0 := signedWord(log.Data, 0)
	amount1 := signedWord(log.Data, 1)

	swap := &events.Swap{
		BlockNumber: log.BlockNumber,
		BlockTime:   blockTime,
		TxHash:      log.TxHash.Hex(),
		LogIndex:    uint32(log.Index),
		Pool:        log.Address.Hex(),
		Sender:      topicToAddress(log.Topics[1]),
		Recipient:   topicToAddress(log.Topics[2]),
		Amount0In:   "0",
		Amount1In:   "0",
		Amount0Out:  "0",
		Amount1Out:  "0",
		Venue:       events.VenueV3,
	}
	if amount0.Sign() > 0 {
		swap.Amount0In = amount0.String()
	} else if amount0.Sign() < 0 {
		swap.Amount0Out = new(big.Int).Neg(amount0).String()
	}
	if amount1.Sign() > 0 {
		swap.Amount1In = amount1.String()
	} else if amount1.Sign() < 0 {
		swap.Amount1Out = new(big.Int).Neg(amount1).String()
	}
	return events.Event{Type: events.TypeSwap, Swap: swap}, true
}

// ToRawLog converts a go-ethereum log into the storage audit-trail shape.
func ToRawLog(log types.Log, blockTime time.Time) events.RawLog {
	topics := make([]string, len(log.Topics))
	for i, t := range log.Topics {
		topics[i] = t.Hex()
	}
	return events.RawLog{
		BlockNumber: log.BlockNumber,
		BlockTime:   blockTime,
		TxHash:      log.TxHash.Hex(),
		LogIndex:    uint32(log.Index),
		Address:     log.Address.Hex(),
		Topics:      topics,
		Data:        log.Data,
	}
}

// word reads the i-th 32-byte ABI word as an unsigned big.Int.
func word(data []byte, i int) *big.Int {
	return new(big.Int).SetBytes(data[i*wordSize : (i+1)*wordSize])
}

// signedWord reads the i-th word as a two's-complement int256.
func signedWord(data []byte, i int) *big.Int {
	v := word(data, i)
	if v.Bit(255) == 1 {
		// Negative: subtract 2^256.
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	return v
}

func topicToAddress(t common.Hash) string {
	return common.BytesToAddress(t.Bytes()).Hex()
}
