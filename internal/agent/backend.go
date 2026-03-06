package agent

import "context"

// TurnStatus represents the outcome of a single agent turn.
type TurnStatus int

const (
	TurnCompleted  TurnStatus = iota
	TurnFailed
	TurnCancelled
	TurnNeedsInput
)

func (s TurnStatus) String() string {
	switch s {
	case TurnCompleted:
		return "completed"
	case TurnFailed:
		return "failed"
	case TurnCancelled:
		return "cancelled"
	case TurnNeedsInput:
		return "needs_input"
	default:
		return "unknown"
	}
}

// TokenUsage tracks token consumption for a turn.
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

// SessionOpts configures a new agent session.
type SessionOpts struct {
	WorkspacePath  string
	ApprovalPolicy map[string]any
	Sandbox        string
	DynamicTools   []ToolSpec
	OnMessage      MessageHandler
	ToolHandler    *DynamicToolHandler
}

// TurnOpts configures a single turn within a session.
type TurnOpts struct {
	TurnSandboxPolicy map[string]any
	Title             string
}

// TurnResult holds the outcome of a completed turn.
type TurnResult struct {
	Status     TurnStatus
	TokenUsage TokenUsage
	Error      string
	SessionID  string
	ThreadID   string
	TurnID     string
}

// ToolSpec describes a dynamic tool advertised to the agent.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// BackendEvent is emitted by agent backends to report progress to the orchestrator.
type BackendEvent struct {
	Type      string         `json:"type"` // session_started, turn_started, turn_completed, tool_call, message, error, rate_limit
	SessionID string         `json:"session_id,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	// AgentPID is the OS process ID of the agent subprocess, if available.
	AgentPID string `json:"agent_pid,omitempty"`
	// TokenUsage from this event, if reported.
	TokenUsage *TokenUsage `json:"token_usage,omitempty"`
}

// MessageHandler receives backend events from the agent.
type MessageHandler func(event BackendEvent)

// Backend is the pluggable agent backend abstraction.
// Both Codex and Claude Code implement this interface.
type Backend interface {
	StartSession(ctx context.Context, opts SessionOpts) (Session, error)
}

// Session represents an active agent session.
type Session interface {
	RunTurn(ctx context.Context, prompt string, opts TurnOpts) (*TurnResult, error)
	ID() string
	Close() error
}
