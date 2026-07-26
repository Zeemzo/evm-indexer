package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zeemzo/evm-indexer/internal/events"
	"github.com/zeemzo/evm-indexer/internal/storage"
)

// fakeReader is a scripted Reader; each method returns canned data and
// records the filter it was called with.
type fakeReader struct {
	transfers   []events.Transfer
	swaps       []events.Swap
	stats       storage.Stats
	err         error
	lastXfer    storage.TransferFilter
	lastSwap    storage.SwapFilter
	lastStatsID uint64
}

func (f *fakeReader) Transfers(_ context.Context, flt storage.TransferFilter) ([]events.Transfer, error) {
	f.lastXfer = flt
	return f.transfers, f.err
}

func (f *fakeReader) Swaps(_ context.Context, flt storage.SwapFilter) ([]events.Swap, error) {
	f.lastSwap = flt
	return f.swaps, f.err
}

func (f *fakeReader) Stats(_ context.Context, chainID uint64) (storage.Stats, error) {
	f.lastStatsID = chainID
	return f.stats, f.err
}

// run feeds newline-delimited JSON requests through the server and
// returns the decoded responses in order.
func run(t *testing.T, reader Reader, chainID uint64, requests ...string) []rpcResponse {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewServer(reader, chainID, "evm-indexer", "test", log)

	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out strings.Builder
	require.NoError(t, srv.Serve(context.Background(), in, &out))

	var responses []rpcResponse
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for dec.More() {
		var r rpcResponse
		require.NoError(t, dec.Decode(&r))
		responses = append(responses, r)
	}
	return responses
}

// callResult extracts the tools/call toolResult from an rpcResponse.
func callResult(t *testing.T, resp rpcResponse) toolResult {
	t.Helper()
	require.Nil(t, resp.Error, "unexpected rpc error: %+v", resp.Error)
	b, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var tr toolResult
	require.NoError(t, json.Unmarshal(b, &tr))
	return tr
}

func TestInitializeHandshake(t *testing.T) {
	resp := run(t, &fakeReader{}, 1,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	require.Len(t, resp, 1)
	require.Nil(t, resp[0].Error)

	b, _ := json.Marshal(resp[0].Result)
	var res initializeResult
	require.NoError(t, json.Unmarshal(b, &res))
	assert.Equal(t, protocolVersion, res.ProtocolVersion)
	assert.Equal(t, "evm-indexer", res.ServerInfo.Name)
	assert.Contains(t, res.Capabilities, "tools")
}

func TestNotificationProducesNoResponse(t *testing.T) {
	// notifications/initialized has no id → server must stay silent.
	resp := run(t, &fakeReader{}, 1,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	assert.Empty(t, resp, "notifications must not be answered")
}

func TestToolsListAdvertisesAllTools(t *testing.T) {
	resp := run(t, &fakeReader{}, 1,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	require.Len(t, resp, 1)

	b, _ := json.Marshal(resp[0].Result)
	var res toolsListResult
	require.NoError(t, json.Unmarshal(b, &res))

	names := map[string]bool{}
	for _, tl := range res.Tools {
		names[tl.Name] = true
		assert.NotEmpty(t, tl.Description, "tool %s missing description", tl.Name)
		assert.Equal(t, "object", tl.InputSchema["type"], "tool %s schema must be an object", tl.Name)
	}
	assert.True(t, names["list_transfers"])
	assert.True(t, names["list_swaps"])
	assert.True(t, names["get_stats"])
}

func TestToolCallListTransfersPassesFilterAndReturnsData(t *testing.T) {
	reader := &fakeReader{transfers: []events.Transfer{
		{BlockNumber: 100, Token: "0xTok", From: "0xA", To: "0xB", Value: "42"},
	}}
	resp := run(t, reader, 137,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_transfers","arguments":{"token":"0xTok","to":"0xB","limit":5}}}`)
	require.Len(t, resp, 1)

	// Filter was threaded through to storage.
	assert.Equal(t, "0xTok", reader.lastXfer.Token)
	assert.Equal(t, "0xB", reader.lastXfer.To)
	assert.Equal(t, 5, reader.lastXfer.Limit)

	tr := callResult(t, resp[0])
	assert.False(t, tr.IsError)
	require.Len(t, tr.Content, 1)
	assert.Contains(t, tr.Content[0].Text, `"count": 1`)
	assert.Contains(t, tr.Content[0].Text, `"value": "42"`)
}

func TestToolCallGetStatsUsesConfiguredChainID(t *testing.T) {
	reader := &fakeReader{stats: storage.Stats{TotalTransfers: 9, LastBlock: 555}}
	resp := run(t, reader, 42161,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_stats","arguments":{}}}`)
	require.Len(t, resp, 1)

	assert.Equal(t, uint64(42161), reader.lastStatsID, "stats must query the server's configured chain id")
	tr := callResult(t, resp[0])
	assert.Contains(t, tr.Content[0].Text, `"lastIndexedBlock": 555`)
}

func TestToolCallReportsQueryErrorAsToolError(t *testing.T) {
	reader := &fakeReader{err: assertAnErr{}}
	resp := run(t, reader, 1,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_swaps","arguments":{}}}`)
	require.Len(t, resp, 1)

	// A backend failure is a tool-level error, not a protocol error:
	// the RPC itself succeeds and carries isError=true.
	require.Nil(t, resp[0].Error)
	tr := callResult(t, resp[0])
	assert.True(t, tr.IsError)
	assert.Contains(t, tr.Content[0].Text, "query failed")
}

func TestUnknownToolIsInvalidParams(t *testing.T) {
	resp := run(t, &fakeReader{}, 1,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`)
	require.Len(t, resp, 1)
	require.NotNil(t, resp[0].Error)
	assert.Equal(t, codeInvalidParams, resp[0].Error.Code)
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	resp := run(t, &fakeReader{}, 1,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	require.Len(t, resp, 1)
	require.NotNil(t, resp[0].Error)
	assert.Equal(t, codeMethodNotFound, resp[0].Error.Code)
}

func TestMalformedJSONIsParseError(t *testing.T) {
	resp := run(t, &fakeReader{}, 1, `{not valid json`)
	require.Len(t, resp, 1)
	require.NotNil(t, resp[0].Error)
	assert.Equal(t, codeParseError, resp[0].Error.Code)
}

type assertAnErr struct{}

func (assertAnErr) Error() string { return "boom" }
