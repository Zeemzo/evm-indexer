package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/zeemzo/evm-indexer/internal/events"
	"github.com/zeemzo/evm-indexer/internal/storage"
)

// Reader is the read-only slice of storage the MCP tools expose.
// *storage.Store satisfies it. Defined here (consumer side) so the
// server can be tested against a fake without a database.
type Reader interface {
	Transfers(ctx context.Context, f storage.TransferFilter) ([]events.Transfer, error)
	Swaps(ctx context.Context, f storage.SwapFilter) ([]events.Swap, error)
	Stats(ctx context.Context, chainID uint64) (storage.Stats, error)
}

type Server struct {
	reader  Reader
	chainID uint64
	name    string
	version string
	log     *slog.Logger

	// out serializes writes so a response and a future concurrent
	// notification can never interleave on the wire.
	mu  sync.Mutex
	out *json.Encoder
}

func NewServer(reader Reader, chainID uint64, name, version string, log *slog.Logger) *Server {
	return &Server{reader: reader, chainID: chainID, name: name, version: version, log: log}
}

// Serve runs the stdio JSON-RPC loop until in is exhausted or ctx is
// cancelled. Messages are newline-delimited JSON, one per line, per the
// MCP stdio transport.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = json.NewEncoder(out)
	scanner := bufio.NewScanner(in)
	// On-chain result payloads can be large; lift the line cap to 4 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, codeParseError, "parse error", err.Error())
			continue
		}
		s.dispatch(ctx, &req)
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, req *rpcRequest) {
	// A request with no id is a notification: handle its side effects but
	// never reply (replying to a notification violates JSON-RPC).
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, initializeResult{
			ProtocolVersion: protocolVersion,
			// Only the tools capability; no resources/prompts.
			Capabilities: map[string]any{"tools": map[string]any{}},
			ServerInfo:   serverInfo{Name: s.name, Version: s.version},
		})
	case "notifications/initialized":
		// Client handshake ack; nothing to do.
	case "ping":
		if !isNotification {
			s.writeResult(req.ID, map[string]any{})
		}
	case "tools/list":
		s.writeResult(req.ID, toolsListResult{Tools: toolDefinitions()})
	case "tools/call":
		s.handleToolCall(ctx, req)
	default:
		if !isNotification {
			s.writeError(req.ID, codeMethodNotFound, "method not found", req.Method)
		}
	}
}

func (s *Server) handleToolCall(ctx context.Context, req *rpcRequest) {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, codeInvalidParams, "invalid params", err.Error())
		return
	}

	handler, ok := toolHandlers[params.Name]
	if !ok {
		s.writeError(req.ID, codeInvalidParams, "unknown tool", params.Name)
		return
	}

	// A tool failure is reported as a successful RPC carrying an error
	// result (isError=true), so the model can read and react to it —
	// only protocol-level faults use the JSON-RPC error channel.
	result := handler(ctx, s, params.Arguments)
	s.writeResult(req.ID, result)
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, msg string, data any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}

func (s *Server) write(resp rpcResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.out.Encode(resp); err != nil {
		s.log.Error("mcp write failed", "error", err)
	}
}

// toJSON renders v as indented JSON for a text content block, falling
// back to an error string it never realistically hits.
func toJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("failed to encode result: %v", err)
	}
	return string(b)
}
