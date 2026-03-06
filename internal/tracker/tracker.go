package tracker

import "github.com/Kurichi/symphony/internal/linear"

// Tracker abstracts issue tracking backends (e.g. Linear).
// The orchestrator depends on this interface rather than concrete clients.
type Tracker interface {
	FetchCandidateIssues() ([]*linear.Issue, error)
	FetchIssueStatesByIDs(ids []string) ([]*linear.Issue, error)
	FetchIssuesByStates(states []string) ([]*linear.Issue, error)
	ExecuteGraphQL(query string, variables map[string]any) (map[string]any, error)
}
