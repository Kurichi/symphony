package orchestrator

import (
	"time"

	"github.com/Kurichi/symphony/internal/linear"
)

// RunningEntry tracks a single running agent session. SPEC §4.1.5 + §4.1.6.
type RunningEntry struct {
	IssueID         string
	IssueIdentifier string
	Issue           *linear.Issue
	Attempt         *int // nil for first run, >=1 for retries
	WorkspacePath   string
	StartedAt       time.Time
	Status          string
	Error           string

	// Live session metadata (SPEC §4.1.6)
	SessionID               string
	ThreadID                string
	TurnID                  string
	AgentPID                string
	LastAgentEvent          string
	LastAgentTimestamp       *time.Time
	LastAgentMessage        string
	AgentInputTokens        int64
	AgentOutputTokens       int64
	AgentTotalTokens        int64
	LastReportedInputTokens int64
	LastReportedOutputTokens int64
	LastReportedTotalTokens  int64
	TurnCount               int

	// Cancel function for the goroutine running this agent.
	Cancel func()
	// Done is closed when the goroutine exits.
	Done <-chan struct{}
}

// RetryEntry represents a scheduled retry for an issue. SPEC §4.1.7.
type RetryEntry struct {
	IssueID    string
	Identifier string
	Attempt    int // 1-based
	DueAtMs    int64
	Error      string

	// Timer is the handle to cancel the scheduled retry callback.
	Timer interface{ Stop() bool }
}

// AgentTotals aggregates token usage and runtime seconds across sessions.
type AgentTotals struct {
	InputTokens    int64
	OutputTokens   int64
	TotalTokens    int64
	SecondsRunning float64
}

// RateLimits holds the latest rate-limit snapshot from agent events.
type RateLimits struct {
	RequestsRemaining int
	RequestsLimit     int
	TokensRemaining   int
	TokensLimit       int
}

// State is the single authoritative in-memory orchestrator state. SPEC §4.1.8.
type State struct {
	PollIntervalMs     int
	MaxConcurrentAgents int

	// Running maps issue ID → RunningEntry
	Running map[string]*RunningEntry
	// Claimed is the set of issue IDs that are reserved/running/retrying
	Claimed map[string]struct{}
	// RetryAttempts maps issue ID → RetryEntry
	RetryAttempts map[string]*RetryEntry
	// Completed is the set of issue IDs that have finished (bookkeeping only)
	Completed map[string]struct{}

	AgentTotals *AgentTotals
	RateLimits  *RateLimits
}

// NewState creates a zeroed-out orchestrator state.
func NewState(pollIntervalMs, maxConcurrentAgents int) *State {
	return &State{
		PollIntervalMs:     pollIntervalMs,
		MaxConcurrentAgents: maxConcurrentAgents,
		Running:             make(map[string]*RunningEntry),
		Claimed:             make(map[string]struct{}),
		RetryAttempts:       make(map[string]*RetryEntry),
		Completed:           make(map[string]struct{}),
		AgentTotals:         &AgentTotals{},
	}
}

// RunningCount returns the number of active agent sessions.
func (s *State) RunningCount() int {
	return len(s.Running)
}

// IsClaimed returns true if the issue ID is in the claimed set.
func (s *State) IsClaimed(issueID string) bool {
	_, ok := s.Claimed[issueID]
	return ok
}

// Claim adds an issue ID to the claimed set.
func (s *State) Claim(issueID string) {
	s.Claimed[issueID] = struct{}{}
}

// Release removes an issue ID from the claimed set.
func (s *State) Release(issueID string) {
	delete(s.Claimed, issueID)
}
