package tracker

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Kurichi/symphony/internal/linear"
)

// Compile-time check: Client must implement Tracker.
var _ Tracker = (*linear.Client)(nil)

// MemoryTracker is an in-memory Tracker implementation for testing.
type MemoryTracker struct {
	issues []*linear.Issue
	mu     sync.RWMutex
}

var _ Tracker = (*MemoryTracker)(nil)

// NewMemoryTracker creates a MemoryTracker seeded with the given issues.
func NewMemoryTracker(issues []*linear.Issue) *MemoryTracker {
	copied := make([]*linear.Issue, len(issues))
	copy(copied, issues)
	return &MemoryTracker{issues: copied}
}

func (m *MemoryTracker) FetchCandidateIssues() ([]*linear.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*linear.Issue, len(m.issues))
	copy(out, m.issues)
	return out, nil
}

func (m *MemoryTracker) FetchIssueStatesByIDs(ids []string) ([]*linear.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}

	var out []*linear.Issue
	for _, issue := range m.issues {
		if _, ok := idSet[issue.ID]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (m *MemoryTracker) FetchIssuesByStates(states []string) ([]*linear.Issue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stateSet := make(map[string]struct{}, len(states))
	for _, s := range states {
		stateSet[strings.ToLower(s)] = struct{}{}
	}

	var out []*linear.Issue
	for _, issue := range m.issues {
		if _, ok := stateSet[strings.ToLower(issue.State)]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (m *MemoryTracker) ExecuteGraphQL(query string, variables map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("MemoryTracker does not support ExecuteGraphQL")
}

// SetIssues replaces all tracked issues.
func (m *MemoryTracker) SetIssues(issues []*linear.Issue) {
	m.mu.Lock()
	defer m.mu.Unlock()

	copied := make([]*linear.Issue, len(issues))
	copy(copied, issues)
	m.issues = copied
}

// UpdateIssueState changes the state of the issue with the given ID.
func (m *MemoryTracker) UpdateIssueState(issueID, newState string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, issue := range m.issues {
		if issue.ID == issueID {
			issue.State = newState
			return
		}
	}
}
