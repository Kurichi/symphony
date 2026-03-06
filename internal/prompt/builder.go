package prompt

import (
	"fmt"
	"time"

	"github.com/Kurichi/symphony/internal/linear"
	"github.com/osteele/liquid"
)

// BuildPrompt renders the Liquid prompt template with the given issue data.
func BuildPrompt(issue *linear.Issue, promptTemplate string) (string, error) {
	engine := liquid.NewEngine()

	bindings := map[string]any{
		"issue": IssueToMap(issue),
	}

	out, err := engine.ParseAndRenderString(promptTemplate, bindings)
	if err != nil {
		return "", fmt.Errorf("prompt template render: %w", err)
	}
	return out, nil
}

// IssueToMap converts an Issue struct to a map suitable for Liquid templates.
// Keys use the same names as the Elixir implementation to ensure template
// compatibility (snake_case fields match Liquid variable references).
func IssueToMap(issue *linear.Issue) map[string]any {
	m := map[string]any{
		"id":                 issue.ID,
		"identifier":         issue.Identifier,
		"title":              issue.Title,
		"description":        issue.Description,
		"state":              issue.State,
		"branch_name":        issue.BranchName,
		"url":                issue.URL,
		"assignee_id":        issue.AssigneeID,
		"labels":             issue.LabelNames(),
		"assigned_to_worker": issue.AssignedToWorker,
	}

	if issue.Priority != nil {
		m["priority"] = *issue.Priority
	}

	if issue.CreatedAt != nil {
		m["created_at"] = issue.CreatedAt.Format(time.RFC3339)
	}
	if issue.UpdatedAt != nil {
		m["updated_at"] = issue.UpdatedAt.Format(time.RFC3339)
	}

	blockers := make([]map[string]any, 0, len(issue.BlockedBy))
	for _, b := range issue.BlockedBy {
		blockers = append(blockers, map[string]any{
			"id":         b.ID,
			"identifier": b.Identifier,
			"state":      b.State,
		})
	}
	m["blocked_by"] = blockers

	return m
}
