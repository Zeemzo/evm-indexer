package chain

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zeemzo/evm-indexer/internal/events"
)

var (
	testTime  = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	addrToken = common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174") // USDC on Polygon
	addrFrom  = common.HexToAddress("0x1111111111111111111111111111111111111111")
	addrTo    = common.HexToAddress("0x2222222222222222222222222222222222222222")
	txHash    = common.HexToHash("0xabc0000000000000000000000000000000000000000000000000000000000001")
)

func addressTopic(a common.Address) common.Hash {
	return common.BytesToHash(a.Bytes())
}

func uintWord(v *big.Int) []byte {
	return common.LeftPadBytes(v.Bytes(), 32)
}

// intWord encodes a signed value as a two's-complement int256 word.
func intWord(v *big.Int) []byte {
	if v.Sign() >= 0 {
		return uintWord(v)
	}
	twoTo256 := new(big.Int).Lsh(big.NewInt(1), 256)
	return uintWord(new(big.Int).Add(twoTo256, v))
}

func baseLog(topic0 common.Hash, topics []common.Hash, data []byte) types.Log {
	return types.Log{
		Address:     addrToken,
		Topics:      append([]common.Hash{topic0}, topics...),
		Data:        data,
		BlockNumber: 62_000_000,
		TxHash:      txHash,
		Index:       7,
	}
}

func TestDecodeERC20Transfer(t *testing.T) {
	value := big.NewInt(1_500_000) // 1.5 USDC
	log := baseLog(TransferTopic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		uintWord(value))

	evt, ok := Decode(log, testTime)
	require.True(t, ok)
	require.Equal(t, events.TypeTransfer, evt.Type)
	require.NotNil(t, evt.Transfer)

	tr := evt.Transfer
	assert.Equal(t, addrToken.Hex(), tr.Token)
	assert.Equal(t, addrFrom.Hex(), tr.From)
	assert.Equal(t, addrTo.Hex(), tr.To)
	assert.Equal(t, "1500000", tr.Value)
	assert.Equal(t, uint64(62_000_000), tr.BlockNumber)
	assert.Equal(t, uint32(7), tr.LogIndex)
	assert.Equal(t, testTime, tr.BlockTime)
}

func TestDecodeERC20TransferMaxUint256(t *testing.T) {
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	log := baseLog(TransferTopic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		uintWord(maxUint256))

	evt, ok := Decode(log, testTime)
	require.True(t, ok)
	assert.Equal(t, maxUint256.String(), evt.Transfer.Value)
}

func TestDecodeSkipsERC721Transfer(t *testing.T) {
	// ERC-721 shares topic0 with ERC-20 but indexes tokenId as a 3rd
	// topic and carries no data.
	log := baseLog(TransferTopic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo), common.HexToHash("0x01")},
		nil)

	_, ok := Decode(log, testTime)
	assert.False(t, ok)
}

func TestDecodeSwapV2(t *testing.T) {
	data := append(uintWord(big.NewInt(1000)), uintWord(big.NewInt(0))...)
	data = append(data, uintWord(big.NewInt(0))...)
	data = append(data, uintWord(big.NewInt(995))...)
	log := baseLog(SwapV2Topic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		data)

	evt, ok := Decode(log, testTime)
	require.True(t, ok)
	require.Equal(t, events.TypeSwap, evt.Type)

	sw := evt.Swap
	assert.Equal(t, events.VenueV2, sw.Venue)
	assert.Equal(t, addrToken.Hex(), sw.Pool)
	assert.Equal(t, addrFrom.Hex(), sw.Sender)
	assert.Equal(t, addrTo.Hex(), sw.Recipient)
	assert.Equal(t, "1000", sw.Amount0In)
	assert.Equal(t, "0", sw.Amount1In)
	assert.Equal(t, "0", sw.Amount0Out)
	assert.Equal(t, "995", sw.Amount1Out)
}

func TestDecodeSwapV3SignedAmounts(t *testing.T) {
	// Pool receives 1000 of token0 (positive), sends 500 of token1
	// (negative) — user swapped token0 for token1.
	data := intWord(big.NewInt(1000))
	data = append(data, intWord(big.NewInt(-500))...)
	data = append(data, uintWord(big.NewInt(1))...)   // sqrtPriceX96
	data = append(data, uintWord(big.NewInt(1))...)   // liquidity
	data = append(data, intWord(big.NewInt(-100))...) // tick
	log := baseLog(SwapV3Topic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		data)

	evt, ok := Decode(log, testTime)
	require.True(t, ok)
	require.Equal(t, events.TypeSwap, evt.Type)

	sw := evt.Swap
	assert.Equal(t, events.VenueV3, sw.Venue)
	assert.Equal(t, "1000", sw.Amount0In)
	assert.Equal(t, "0", sw.Amount0Out)
	assert.Equal(t, "0", sw.Amount1In)
	assert.Equal(t, "500", sw.Amount1Out)
}

func TestDecodeSwapV3OppositeDirection(t *testing.T) {
	data := intWord(big.NewInt(-42))
	data = append(data, intWord(big.NewInt(84))...)
	data = append(data, uintWord(big.NewInt(1))...)
	data = append(data, uintWord(big.NewInt(1))...)
	data = append(data, intWord(big.NewInt(0))...)
	log := baseLog(SwapV3Topic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		data)

	evt, ok := Decode(log, testTime)
	require.True(t, ok)
	sw := evt.Swap
	assert.Equal(t, "0", sw.Amount0In)
	assert.Equal(t, "42", sw.Amount0Out)
	assert.Equal(t, "84", sw.Amount1In)
	assert.Equal(t, "0", sw.Amount1Out)
}

func TestDecodeUnknownTopic(t *testing.T) {
	log := baseLog(common.HexToHash("0xdead"), nil, nil)
	_, ok := Decode(log, testTime)
	assert.False(t, ok)
}

func TestDecodeNoTopics(t *testing.T) {
	log := types.Log{Data: uintWord(big.NewInt(1))}
	_, ok := Decode(log, testTime)
	assert.False(t, ok)
}

func TestDecodeMalformedData(t *testing.T) {
	// Right topic, truncated data: must be rejected, not panic.
	log := baseLog(TransferTopic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		[]byte{0x01, 0x02})
	_, ok := Decode(log, testTime)
	assert.False(t, ok)

	log = baseLog(SwapV2Topic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		uintWord(big.NewInt(1)))
	_, ok = Decode(log, testTime)
	assert.False(t, ok)
}

func TestToRawLog(t *testing.T) {
	log := baseLog(TransferTopic,
		[]common.Hash{addressTopic(addrFrom), addressTopic(addrTo)},
		uintWord(big.NewInt(5)))

	raw := ToRawLog(log, testTime)
	assert.Equal(t, uint64(62_000_000), raw.BlockNumber)
	assert.Equal(t, txHash.Hex(), raw.TxHash)
	assert.Equal(t, addrToken.Hex(), raw.Address)
	assert.Len(t, raw.Topics, 3)
	assert.Equal(t, TransferTopic.Hex(), raw.Topics[0])
}

func TestTopicHashesMatchKnownSignatures(t *testing.T) {
	// Pin the well-known signature hashes so a typo in the signature
	// strings can never slip through.
	assert.Equal(t, "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", TransferTopic.Hex())
	assert.Equal(t, "0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822", SwapV2Topic.Hex())
	assert.Equal(t, "0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67", SwapV3Topic.Hex())
}
