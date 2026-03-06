package codex

import "encoding/json"

// JSON-RPC 2.0 message types for Codex app-server protocol.

// JSONRPCRequest is a JSON-RPC 2.0 request sent to the Codex process stdin.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	ID      any    `json:"id,omitempty"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response received from Codex stdout.
type JSONRPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// CodexEvent is a notification from the Codex app-server stdout stream.
type CodexEvent struct {
	Method string          `json:"method,omitempty"`
	ID     any             `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// Event method constants matching the Codex app-server protocol.
const (
	MethodInitialize                     = "initialize"
	MethodInitialized                    = "initialized"
	MethodThreadStart                    = "thread/start"
	MethodThreadStarted                  = "thread/started"
	MethodTurnStart                      = "turn/start"
	MethodTurnCompleted                  = "turn/completed"
	MethodTurnFailed                     = "turn/failed"
	MethodTurnCancelled                  = "turn/cancelled"
	MethodItemToolCall                   = "item/tool/call"
	MethodItemToolRequestUserInput       = "item/tool/requestUserInput"
	MethodItemCommandExecRequestApproval = "item/commandExecution/requestApproval"
	MethodExecCommandApproval            = "execCommandApproval"
	MethodApplyPatchApproval             = "applyPatchApproval"
	MethodItemFileChangeRequestApproval  = "item/fileChange/requestApproval"
	MethodSessionRateLimit               = "session/rate_limit"
)

// JSON-RPC request IDs used in the handshake sequence.
const (
	InitializeID  = 1
	ThreadStartID = 2
	TurnStartID   = 3
)
