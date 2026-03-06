package orchestrator

import (
	"log/slog"
	"time"

	"github.com/Kurichi/symphony/internal/linear"
)

// ReconcileResult captures the actions taken during reconciliation.
type ReconcileResult struct {
	Stalled    []string // issue IDs detected as stalled
	Terminal   []string // issue IDs in terminal state
	NonActive  []string // issue IDs no longer in active state
	Refreshed  []string // issue IDs with updated state
	Unroutable []string // issue IDs no longer routable to worker
}

// ReconcileStalledIssues checks all running issues for stall timeout. SPEC §8.5 Part A.
func ReconcileStalledIssues(state *State, stallTimeoutMs int, now time.Time) ([]string, *State) {
	if stallTimeoutMs <= 0 || len(state.Running) == 0 {
		return nil, state
	}

	var stalled []string
	for issueID, entry := range state.Running {
		elapsed := stallElapsedMs(entry, now)
		if elapsed > int64(stallTimeoutMs) {
			slog.Warn("Issue stalled",
				"issue_id", issueID,
				"issue_identifier", entry.IssueIdentifier,
				"session_id", entry.SessionID,
				"elapsed_ms", elapsed,
			)
			stalled = append(stalled, issueID)
		}
	}

	return stalled, state
}

func stallElapsedMs(entry *RunningEntry, now time.Time) int64 {
	var ref time.Time
	if entry.LastAgentTimestamp != nil {
		ref = *entry.LastAgentTimestamp
	} else {
		ref = entry.StartedAt
	}
	if ref.IsZero() {
		return 0
	}
	elapsed := now.Sub(ref).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

// ReconcileIssueStates processes fetched issue states against running entries. SPEC §8.5 Part B.
func ReconcileIssueStates(
	fetchedIssues []*linear.Issue,
	state *State,
	filter *CandidateFilter,
) *ReconcileResult {
	result := &ReconcileResult{}
	fetchedByID := make(map[string]*linear.Issue, len(fetchedIssues))
	for _, issue := range fetchedIssues {
		fetchedByID[issue.ID] = issue
	}

	for issueID, entry := range state.Running {
		fetched, found := fetchedByID[issueID]
		if !found {
			continue
		}

		if isTerminalIssueState(fetched.State, filter.TerminalStates) {
			slog.Info("Issue moved to terminal state",
				"issue_id", issueID,
				"issue_identifier", entry.IssueIdentifier,
				"state", fetched.State,
			)
			result.Terminal = append(result.Terminal, issueID)
			continue
		}

		if !fetched.AssignedToWorker {
			slog.Info("Issue no longer routed to this worker",
				"issue_id", issueID,
				"issue_identifier", entry.IssueIdentifier,
				"assignee", fetched.AssigneeID,
			)
			result.Unroutable = append(result.Unroutable, issueID)
			continue
		}

		if isActiveIssueState(fetched.State, filter.ActiveStates) {
			// Refresh the stored issue snapshot
			entry.Issue = fetched
			result.Refreshed = append(result.Refreshed, issueID)
			continue
		}

		// Non-active, non-terminal
		slog.Info("Issue moved to non-active state",
			"issue_id", issueID,
			"issue_identifier", entry.IssueIdentifier,
			"state", fetched.State,
		)
		result.NonActive = append(result.NonActive, issueID)
	}

	return result
}
