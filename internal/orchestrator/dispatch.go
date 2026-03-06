package orchestrator

import (
	"sort"
	"strings"
	"time"

	"github.com/Kurichi/symphony/internal/linear"
)

// SortIssuesForDispatch sorts issues by priority ASC → created_at oldest → identifier lexicographic.
// SPEC §8.2
func SortIssuesForDispatch(issues []*linear.Issue) []*linear.Issue {
	sorted := make([]*linear.Issue, len(issues))
	copy(sorted, issues)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi := priorityRank(sorted[i].Priority)
		pj := priorityRank(sorted[j].Priority)
		if pi != pj {
			return pi < pj
		}
		ci := createdAtSortKey(sorted[i])
		cj := createdAtSortKey(sorted[j])
		if ci != cj {
			return ci < cj
		}
		return issueIdentifier(sorted[i]) < issueIdentifier(sorted[j])
	})
	return sorted
}

func priorityRank(p *int) int {
	if p == nil {
		return 5
	}
	v := *p
	if v >= 1 && v <= 4 {
		return v
	}
	return 5
}

func createdAtSortKey(issue *linear.Issue) int64 {
	if issue.CreatedAt == nil {
		return 1<<63 - 1 // max int64
	}
	return issue.CreatedAt.UnixMicro()
}

func issueIdentifier(issue *linear.Issue) string {
	if issue.Identifier != "" {
		return issue.Identifier
	}
	return issue.ID
}

// CandidateFilter holds the state sets needed for dispatch eligibility checks.
type CandidateFilter struct {
	ActiveStates   map[string]struct{}
	TerminalStates map[string]struct{}
}

// NewCandidateFilter builds normalized state sets.
func NewCandidateFilter(activeStates, terminalStates []string) *CandidateFilter {
	return &CandidateFilter{
		ActiveStates:   normalizeStateSet(activeStates),
		TerminalStates: normalizeStateSet(terminalStates),
	}
}

func normalizeStateSet(states []string) map[string]struct{} {
	set := make(map[string]struct{}, len(states))
	for _, s := range states {
		n := strings.ToLower(strings.TrimSpace(s))
		if n != "" {
			set[n] = struct{}{}
		}
	}
	return set
}

func normalizeIssueState(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func isActiveIssueState(state string, activeStates map[string]struct{}) bool {
	_, ok := activeStates[normalizeIssueState(state)]
	return ok
}

func isTerminalIssueState(state string, terminalStates map[string]struct{}) bool {
	_, ok := terminalStates[normalizeIssueState(state)]
	return ok
}

// ShouldDispatchIssue checks if an issue is eligible for dispatch. SPEC §8.2.
func ShouldDispatchIssue(issue *linear.Issue, state *State, filter *CandidateFilter, maxConcurrentForState func(string) int) bool {
	if !isCandidateIssue(issue, filter) {
		return false
	}
	if todoBlockedByNonTerminal(issue, filter.TerminalStates) {
		return false
	}
	if state.IsClaimed(issue.ID) {
		return false
	}
	if _, running := state.Running[issue.ID]; running {
		return false
	}
	if AvailableSlots(state) <= 0 {
		return false
	}
	if !stateSlotsAvailable(issue, state, maxConcurrentForState) {
		return false
	}
	return true
}

func isCandidateIssue(issue *linear.Issue, filter *CandidateFilter) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" || issue.State == "" {
		return false
	}
	if !issue.AssignedToWorker {
		return false
	}
	if !isActiveIssueState(issue.State, filter.ActiveStates) {
		return false
	}
	if isTerminalIssueState(issue.State, filter.TerminalStates) {
		return false
	}
	return true
}

// todoBlockedByNonTerminal checks the blocker rule: if issue is "todo", don't dispatch when any blocker is non-terminal.
func todoBlockedByNonTerminal(issue *linear.Issue, terminalStates map[string]struct{}) bool {
	if normalizeIssueState(issue.State) != "todo" {
		return false
	}
	for _, blocker := range issue.BlockedBy {
		if blocker.State == "" {
			return true // unknown state → assume non-terminal
		}
		if !isTerminalIssueState(blocker.State, terminalStates) {
			return true
		}
	}
	return false
}

// AvailableSlots returns the number of dispatch slots remaining.
func AvailableSlots(state *State) int {
	slots := state.MaxConcurrentAgents - state.RunningCount()
	if slots < 0 {
		return 0
	}
	return slots
}

func stateSlotsAvailable(issue *linear.Issue, state *State, maxForState func(string) int) bool {
	limit := maxForState(issue.State)
	used := runningCountForState(state.Running, issue.State)
	return limit > used
}

func runningCountForState(running map[string]*RunningEntry, issueState string) int {
	normalized := normalizeIssueState(issueState)
	count := 0
	for _, entry := range running {
		if entry.Issue != nil && normalizeIssueState(entry.Issue.State) == normalized {
			count++
		}
	}
	return count
}

// RevalidateIssueForDispatch refreshes an issue from the tracker before dispatch.
// Returns (refreshed issue, should dispatch, error).
func RevalidateIssueForDispatch(
	issue *linear.Issue,
	fetcher func([]string) ([]*linear.Issue, error),
	filter *CandidateFilter,
) (*linear.Issue, bool, error) {
	if issue.ID == "" {
		return issue, true, nil
	}
	issues, err := fetcher([]string{issue.ID})
	if err != nil {
		return nil, false, err
	}
	if len(issues) == 0 {
		return nil, false, nil
	}
	refreshed := issues[0]
	if isRetryCandidateIssue(refreshed, filter) {
		return refreshed, true, nil
	}
	return refreshed, false, nil
}

func isRetryCandidateIssue(issue *linear.Issue, filter *CandidateFilter) bool {
	return isCandidateIssue(issue, filter) && !todoBlockedByNonTerminal(issue, filter.TerminalStates)
}

// RunningSeconds computes the elapsed seconds since startedAt.
func RunningSeconds(startedAt time.Time) float64 {
	return time.Since(startedAt).Seconds()
}
