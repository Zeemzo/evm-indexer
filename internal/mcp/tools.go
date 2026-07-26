package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zeemzo/evm-indexer/internal/storage"
)

// toolHandler runs one tool. Raw is the JSON arguments object; the
// handler owns parsing so each tool declares exactly the args it wants.
type toolHandler func(ctx context.Context, s *Server, raw json.RawMessage) toolResult

var toolHandlers = map[string]toolHandler{
	"list_transfers": handleListTransfers,
	"list_swaps":     handleListSwaps,
	"get_stats":      handleGetStats,
}

// toolDefinitions is the tools/list payload: name, description, and JSON
// Schema for each tool's arguments so the host can validate calls.
func toolDefinitions() []tool {
	return []tool{
		{
			Name:        "list_transfers",
			Description: "List indexed ERC-20 Transfer events, most recent first. Filter by token contract, sender, or recipient address. Amounts are returned as decimal strings (uint256 does not fit a JSON number).",
			InputSchema: object(props{
				"token":       stringProp("ERC-20 token contract address (0x…), optional"),
				"from":        stringProp("Sender address (0x…), optional"),
				"to":          stringProp("Recipient address (0x…), optional"),
				"beforeBlock": numberProp("Return only transfers below this block number, for keyset pagination; optional"),
				"limit":       numberProp("Max rows to return (1-500, default 25)"),
			}),
		},
		{
			Name:        "list_swaps",
			Description: "List indexed Uniswap V2/V3 Swap events, most recent first. Filter by pool address or venue. V2 and V3 are normalized to one in/out amount model.",
			InputSchema: object(props{
				"pool":        stringProp("Liquidity pool address (0x…), optional"),
				"venue":       stringProp("Swap venue: 'v2' or 'v3', optional"),
				"beforeBlock": numberProp("Return only swaps below this block number; optional"),
				"limit":       numberProp("Max rows to return (1-500, default 25)"),
			}),
		},
		{
			Name:        "get_stats",
			Description: "Return indexer dataset statistics: total raw logs, total transfers, total swaps, and the highest block indexed so far for the configured chain.",
			InputSchema: object(props{}),
		},
	}
}

// --- handlers ---

type transferArgs struct {
	Token       string `json:"token"`
	From        string `json:"from"`
	To          string `json:"to"`
	BeforeBlock uint64 `json:"beforeBlock"`
	Limit       int    `json:"limit"`
}

func handleListTransfers(ctx context.Context, s *Server, raw json.RawMessage) toolResult {
	var a transferArgs
	if err := unmarshalArgs(raw, &a); err != nil {
		return errorResult(err.Error())
	}
	transfers, err := s.reader.Transfers(ctx, storage.TransferFilter{
		Token:       a.Token,
		From:        a.From,
		To:          a.To,
		BeforeBlock: a.BeforeBlock,
		Limit:       a.Limit,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("query failed: %v", err))
	}
	return textResult(toJSON(map[string]any{"count": len(transfers), "transfers": transfers}))
}

type swapArgs struct {
	Pool        string `json:"pool"`
	Venue       string `json:"venue"`
	BeforeBlock uint64 `json:"beforeBlock"`
	Limit       int    `json:"limit"`
}

func handleListSwaps(ctx context.Context, s *Server, raw json.RawMessage) toolResult {
	var a swapArgs
	if err := unmarshalArgs(raw, &a); err != nil {
		return errorResult(err.Error())
	}
	swaps, err := s.reader.Swaps(ctx, storage.SwapFilter{
		Pool:        a.Pool,
		Venue:       a.Venue,
		BeforeBlock: a.BeforeBlock,
		Limit:       a.Limit,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("query failed: %v", err))
	}
	return textResult(toJSON(map[string]any{"count": len(swaps), "swaps": swaps}))
}

func handleGetStats(ctx context.Context, s *Server, _ json.RawMessage) toolResult {
	stats, err := s.reader.Stats(ctx, s.chainID)
	if err != nil {
		return errorResult(fmt.Sprintf("query failed: %v", err))
	}
	return textResult(toJSON(stats))
}

// unmarshalArgs tolerates a null/empty arguments object (tools with no
// required args) and reports malformed JSON clearly.
func unmarshalArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// --- tiny JSON Schema builders, to keep toolDefinitions readable ---

type props map[string]map[string]any

func object(p props) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any(mapProps(p)),
	}
}

func mapProps(p props) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
