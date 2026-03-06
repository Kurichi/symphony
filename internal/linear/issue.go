package linear

import "time"

// Issue is the normalized Linear issue representation used by the orchestrator.
// SPEC §4.1.1
type Issue struct {
	ID          string       `json:"id"`
	Identifier  string       `json:"identifier"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Priority    *int         `json:"priority,omitempty"`
	State       string       `json:"state"`
	BranchName  string       `json:"branch_name,omitempty"`
	URL         string       `json:"url,omitempty"`
	AssigneeID  string       `json:"assignee_id,omitempty"`
	Labels      []string     `json:"labels"`
	BlockedBy   []BlockerRef `json:"blocked_by"`
	// AssignedToWorker indicates whether the issue's assignee matches the configured routing filter.
	AssignedToWorker bool       `json:"assigned_to_worker"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
	UpdatedAt        *time.Time `json:"updated_at,omitempty"`
}

// BlockerRef is a reference to a blocking issue (inverse relation of type "blocks").
type BlockerRef struct {
	ID         string `json:"id,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	State      string `json:"state,omitempty"`
}

// LabelNames returns the label list for template rendering.
func (i *Issue) LabelNames() []string {
	if i.Labels == nil {
		return []string{}
	}
	return i.Labels
}
