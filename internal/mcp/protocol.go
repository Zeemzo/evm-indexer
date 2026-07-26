// Package mcp implements a Model Context Protocol server over stdio,
// exposing the indexer's on-chain data as tools an AI agent (Claude
// Desktop, Claude Code, Cursor, ...) can call directly. It speaks the
// MCP JSON-RPC 2.0 framing itself rather than pulling in an SDK, so the
// wire behaviour is explicit and dependency-free.
//
// Supported methods: initialize, notifications/initialized, tools/list,
// tools/call, ping. That is the minimal surface a host needs to
// discover and invoke tools.
package mcp

import "encoding/json"

// protocolVersion is the MCP revision we advertise in the initialize
// response. Hosts negotiate against this string.
const protocolVersion = "2024-11-05"

// rpcRequest is an incoming JSON-RPC 2.0 message. ID is absent for
// notifications; we detect that via hasID during decode.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC standard error codes we use.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// --- initialize ---

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// --- tools/list ---

// tool is one entry in tools/list. InputSchema is a JSON Schema object
// describing the tool's arguments.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []tool `json:"tools"`
}

// --- tools/call ---

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolResult is the tools/call return. Content is a list of content
// blocks; we only emit text blocks. IsError flags a tool-level failure
// (distinct from a protocol error) so the model can read the message.
type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(s string) toolResult {
	return toolResult{Content: []textContent{{Type: "text", Text: s}}}
}

func errorResult(s string) toolResult {
	return toolResult{Content: []textContent{{Type: "text", Text: s}}, IsError: true}
}
