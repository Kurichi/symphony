package config

import (
	"os"
	"path/filepath"
)

// Default constants derived from SPEC §6.4 and the Elixir reference implementation.

var (
	DefaultActiveStates   = []string{"Todo", "In Progress"}
	DefaultTerminalStates = []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}
)

const (
	DefaultLinearEndpoint = "https://api.linear.app/graphql"
	DefaultPromptTemplate = `You are working on a Linear issue.

Identifier: {{ issue.identifier }}
Title: {{ issue.title }}

Body:
{% if issue.description %}
{{ issue.description }}
{% else %}
No description provided.
{% endif %}
`
	DefaultPollIntervalMs       = 30_000
	DefaultHookTimeoutMs        = 60_000
	DefaultMaxConcurrentAgents  = 10
	DefaultAgentMaxTurns        = 20
	DefaultMaxRetryBackoffMs    = 300_000
	DefaultCodexCommand         = "codex app-server"
	DefaultCodexTurnTimeoutMs   = 3_600_000
	DefaultCodexReadTimeoutMs   = 5_000
	DefaultCodexStallTimeoutMs  = 300_000
	DefaultCodexThreadSandbox   = "workspace-write"
	DefaultObservabilityEnabled = true
	DefaultObservabilityRefresh = 1_000
	DefaultObservabilityRender  = 16
	DefaultServerHost           = "127.0.0.1"

	// Claude Code backend defaults
	DefaultClaudeCodeCommand = "claude"

	// Retry constants (SPEC §11.3)
	ContinuationRetryDelayMs = 1_000
	FailureRetryBaseMs       = 10_000
)

// DefaultWorkspaceRoot returns the default workspace root directory.
func DefaultWorkspaceRoot() string {
	return filepath.Join(os.TempDir(), "symphony_workspaces")
}

// DefaultCodexApprovalPolicy returns the default approval policy map.
func DefaultCodexApprovalPolicy() map[string]any {
	return map[string]any{
		"reject": map[string]any{
			"sandbox_approval":  true,
			"rules":             true,
			"mcp_elicitations":  true,
		},
	}
}

// DefaultClaudeCodeAllowedTools returns the default set of allowed tools for Claude Code.
func DefaultClaudeCodeAllowedTools() []string {
	return []string{"Bash", "Read", "Edit", "Write", "Glob", "Grep"}
}
