package linear

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// ClientConfig holds configuration for the Linear GraphQL client.
type ClientConfig struct {
	Endpoint       string
	APIToken       string
	ProjectSlug    string
	Assignee       string // "me" resolves viewer ID via QueryViewer
	ActiveStates   []string
	TerminalStates []string
	HTTPTimeout    time.Duration
}

// Client is a Linear GraphQL HTTP client that implements polling, pagination,
// and issue normalization.
type Client struct {
	config     ClientConfig
	httpClient *http.Client

	// resolvedAssigneeIDs is populated lazily when Assignee == "me".
	resolvedAssigneeIDs map[string]struct{}
	assigneeResolved    bool
}

// NewClient creates a new Linear client with the given configuration.
func NewClient(cfg ClientConfig) *Client {
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 30 * time.Second
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.linear.app/graphql"
	}
	return &Client{
		config: cfg,
		httpClient: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
	}
}

// FetchCandidateIssues fetches issues matching active states with cursor-based pagination.
func (c *Client) FetchCandidateIssues() ([]*Issue, error) {
	if c.config.APIToken == "" {
		return nil, fmt.Errorf("missing linear API token")
	}
	if c.config.ProjectSlug == "" {
		return nil, fmt.Errorf("missing linear project slug")
	}

	assigneeFilter, err := c.routingAssigneeFilter()
	if err != nil {
		return nil, fmt.Errorf("resolving assignee filter: %w", err)
	}

	return c.fetchByStates(c.config.ProjectSlug, c.config.ActiveStates, assigneeFilter)
}

// FetchIssueStatesByIDs fetches issues by their IDs (single page, no cursor pagination).
func (c *Client) FetchIssueStatesByIDs(ids []string) ([]*Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if c.config.APIToken == "" {
		return nil, fmt.Errorf("missing linear API token")
	}

	assigneeFilter, err := c.routingAssigneeFilter()
	if err != nil {
		return nil, fmt.Errorf("resolving assignee filter: %w", err)
	}

	uniqueIDs := dedupStrings(ids)
	first := len(uniqueIDs)
	if first > IssuePageSize {
		first = IssuePageSize
	}

	body, err := c.GraphQL(QueryIssuesByIDs, map[string]any{
		"ids":           uniqueIDs,
		"first":         first,
		"relationFirst": IssuePageSize,
	})
	if err != nil {
		return nil, err
	}

	return decodeLinearResponse(body, assigneeFilter)
}

// FetchIssuesByStates fetches issues matching the given state names.
func (c *Client) FetchIssuesByStates(states []string) ([]*Issue, error) {
	if len(states) == 0 {
		return nil, nil
	}
	if c.config.APIToken == "" {
		return nil, fmt.Errorf("missing linear API token")
	}
	if c.config.ProjectSlug == "" {
		return nil, fmt.Errorf("missing linear project slug")
	}

	normalized := dedupStrings(states)
	return c.fetchByStates(c.config.ProjectSlug, normalized, nil)
}

// GraphQL executes a raw GraphQL query against the Linear API.
func (c *Client) GraphQL(query string, variables map[string]any) (map[string]any, error) {
	payload := map[string]any{
		"query":     query,
		"variables": variables,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling GraphQL payload: %w", err)
	}

	respBody, err := c.doRequestWithRetry(body)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling GraphQL response: %w", err)
	}

	return result, nil
}

// fetchByStates performs cursor-based pagination over issues filtered by states.
func (c *Client) fetchByStates(projectSlug string, stateNames []string, assigneeFilter map[string]struct{}) ([]*Issue, error) {
	var allIssues []*Issue
	var afterCursor *string

	for {
		vars := map[string]any{
			"projectSlug":   projectSlug,
			"stateNames":    stateNames,
			"first":         IssuePageSize,
			"relationFirst": IssuePageSize,
		}
		if afterCursor != nil {
			vars["after"] = *afterCursor
		}

		body, err := c.GraphQL(QueryCandidateIssues, vars)
		if err != nil {
			return nil, err
		}

		issues, pageInfo, err := decodeLinearPageResponse(body, assigneeFilter)
		if err != nil {
			return nil, err
		}

		allIssues = append(allIssues, issues...)

		cursor, done, err := nextPageCursor(pageInfo)
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
		afterCursor = &cursor
	}

	return allIssues, nil
}

// doRequestWithRetry sends a POST request with exponential backoff retry on 429/5xx.
func (c *Client) doRequestWithRetry(body []byte) ([]byte, error) {
	const maxRetries = 3
	const baseDelay = 1 * time.Second
	const maxDelay = 30 * time.Second

	var lastErr error
	for attempt := range maxRetries + 1 {
		if attempt > 0 {
			delay := time.Duration(math.Pow(2, float64(attempt-1))) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}
			jitter := time.Duration(rand.Int64N(int64(delay / 2)))
			time.Sleep(delay + jitter)
		}

		req, err := http.NewRequest(http.MethodPost, c.config.Endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating HTTP request: %w", err)
		}
		req.Header.Set("Authorization", c.config.APIToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w", err)
			slog.Warn("Linear API request failed, retrying", "attempt", attempt+1, "error", err)
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("reading response body: %w", readErr)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			return respBody, nil
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			truncated := truncateBody(string(respBody), 1000)
			lastErr = fmt.Errorf("Linear API status=%d body=%s", resp.StatusCode, truncated)
			slog.Warn("Linear API retryable error", "status", resp.StatusCode, "attempt", attempt+1)
			continue
		}

		truncated := truncateBody(string(respBody), 1000)
		return nil, fmt.Errorf("Linear API status=%d body=%s", resp.StatusCode, truncated)
	}

	return nil, fmt.Errorf("Linear API request failed after %d retries: %w", maxRetries, lastErr)
}

// routingAssigneeFilter builds the assignee match set for issue routing.
func (c *Client) routingAssigneeFilter() (map[string]struct{}, error) {
	assignee := strings.TrimSpace(c.config.Assignee)
	if assignee == "" {
		return nil, nil
	}

	if strings.ToLower(assignee) == "me" {
		return c.resolveViewerAssigneeFilter()
	}

	return map[string]struct{}{assignee: {}}, nil
}

// resolveViewerAssigneeFilter queries the Linear API for the authenticated user's ID.
func (c *Client) resolveViewerAssigneeFilter() (map[string]struct{}, error) {
	if c.assigneeResolved {
		return c.resolvedAssigneeIDs, nil
	}

	body, err := c.GraphQL(QueryViewer, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("resolving viewer identity: %w", err)
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing data in viewer response")
	}
	viewer, ok := data["viewer"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing viewer in response")
	}
	viewerID, ok := viewer["id"].(string)
	if !ok || strings.TrimSpace(viewerID) == "" {
		return nil, fmt.Errorf("missing viewer identity")
	}

	c.resolvedAssigneeIDs = map[string]struct{}{viewerID: {}}
	c.assigneeResolved = true
	return c.resolvedAssigneeIDs, nil
}

// pageInfo holds pagination metadata from Linear.
type pageInfo struct {
	hasNextPage bool
	endCursor   string
}

// decodeLinearPageResponse extracts issues and pagination info from a paginated response.
func decodeLinearPageResponse(body map[string]any, assigneeFilter map[string]struct{}) ([]*Issue, *pageInfo, error) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, nil, decodeGraphQLErrors(body)
	}
	issuesObj, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("missing issues in response")
	}

	nodes, _ := issuesObj["nodes"].([]any)
	issues := normalizeNodes(nodes, assigneeFilter)

	pi := &pageInfo{}
	if piObj, ok := issuesObj["pageInfo"].(map[string]any); ok {
		pi.hasNextPage, _ = piObj["hasNextPage"].(bool)
		pi.endCursor, _ = piObj["endCursor"].(string)
	}

	return issues, pi, nil
}

// decodeLinearResponse extracts issues from a non-paginated response.
func decodeLinearResponse(body map[string]any, assigneeFilter map[string]struct{}) ([]*Issue, error) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, decodeGraphQLErrors(body)
	}
	issuesObj, ok := data["issues"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing issues in response")
	}
	nodes, _ := issuesObj["nodes"].([]any)
	return normalizeNodes(nodes, assigneeFilter), nil
}

// decodeGraphQLErrors extracts error information from a GraphQL error response.
func decodeGraphQLErrors(body map[string]any) error {
	if errs, ok := body["errors"]; ok {
		return fmt.Errorf("Linear GraphQL errors: %v", errs)
	}
	return fmt.Errorf("unknown Linear API response")
}

// nextPageCursor returns the next cursor, or done=true if no more pages.
func nextPageCursor(pi *pageInfo) (cursor string, done bool, err error) {
	if !pi.hasNextPage {
		return "", true, nil
	}
	if pi.endCursor == "" {
		return "", false, fmt.Errorf("Linear API: hasNextPage=true but missing endCursor")
	}
	return pi.endCursor, false, nil
}

// normalizeNodes converts raw JSON node objects into Issue structs.
func normalizeNodes(nodes []any, assigneeFilter map[string]struct{}) []*Issue {
	var issues []*Issue
	for _, node := range nodes {
		m, ok := node.(map[string]any)
		if !ok {
			continue
		}
		issue := normalizeIssue(m, assigneeFilter)
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues
}

// normalizeIssue converts a raw JSON issue object into an Issue struct.
func normalizeIssue(raw map[string]any, assigneeFilter map[string]struct{}) *Issue {
	if raw == nil {
		return nil
	}

	issue := &Issue{
		ID:          strField(raw, "id"),
		Identifier:  strField(raw, "identifier"),
		Title:       strField(raw, "title"),
		Description: strField(raw, "description"),
		Priority:    intPtrField(raw, "priority"),
		BranchName:  strField(raw, "branchName"),
		URL:         strField(raw, "url"),
	}

	// state.name
	if stateObj, ok := raw["state"].(map[string]any); ok {
		issue.State, _ = stateObj["name"].(string)
	}

	// assignee.id
	if assigneeObj, ok := raw["assignee"].(map[string]any); ok {
		issue.AssigneeID, _ = assigneeObj["id"].(string)
	}

	// labels: lowercase
	issue.Labels = extractLabels(raw)

	// blockers: inverseRelations with type "blocks"
	issue.BlockedBy = extractBlockers(raw)

	// assigned_to_worker
	issue.AssignedToWorker = isAssignedToWorker(issue.AssigneeID, assigneeFilter)

	// timestamps
	issue.CreatedAt = parseTimestamp(raw, "createdAt")
	issue.UpdatedAt = parseTimestamp(raw, "updatedAt")

	return issue
}

// extractLabels extracts and lowercases label names from labels.nodes[].name.
func extractLabels(raw map[string]any) []string {
	labelsObj, ok := raw["labels"].(map[string]any)
	if !ok {
		return []string{}
	}
	nodes, ok := labelsObj["nodes"].([]any)
	if !ok {
		return []string{}
	}

	var labels []string
	for _, node := range nodes {
		if m, ok := node.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				labels = append(labels, strings.ToLower(name))
			}
		}
	}
	if labels == nil {
		return []string{}
	}
	return labels
}

// extractBlockers extracts blocker references from inverseRelations where type == "blocks".
func extractBlockers(raw map[string]any) []BlockerRef {
	invObj, ok := raw["inverseRelations"].(map[string]any)
	if !ok {
		return nil
	}
	nodes, ok := invObj["nodes"].([]any)
	if !ok {
		return nil
	}

	var blockers []BlockerRef
	for _, node := range nodes {
		rel, ok := node.(map[string]any)
		if !ok {
			continue
		}
		relType, _ := rel["type"].(string)
		if strings.ToLower(strings.TrimSpace(relType)) != "blocks" {
			continue
		}
		blockerIssue, ok := rel["issue"].(map[string]any)
		if !ok {
			continue
		}

		ref := BlockerRef{
			ID:         strField(blockerIssue, "id"),
			Identifier: strField(blockerIssue, "identifier"),
		}
		if stateObj, ok := blockerIssue["state"].(map[string]any); ok {
			ref.State, _ = stateObj["name"].(string)
		}
		blockers = append(blockers, ref)
	}
	return blockers
}

// isAssignedToWorker checks whether the issue's assignee matches the configured filter.
// If assigneeFilter is nil (no routing configured), all issues are considered assigned.
func isAssignedToWorker(assigneeID string, assigneeFilter map[string]struct{}) bool {
	if assigneeFilter == nil {
		return true
	}
	assigneeID = strings.TrimSpace(assigneeID)
	if assigneeID == "" {
		return false
	}
	_, ok := assigneeFilter[assigneeID]
	return ok
}

// Helper functions for extracting typed fields from map[string]any.

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func intPtrField(m map[string]any, key string) *int {
	switch v := m[key].(type) {
	case float64:
		i := int(v)
		return &i
	case json.Number:
		if n, err := v.Int64(); err == nil {
			i := int(n)
			return &i
		}
	}
	return nil
}

func parseTimestamp(m map[string]any, key string) *time.Time {
	raw, ok := m[key].(string)
	if !ok || raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}

func truncateBody(body string, maxLen int) string {
	if len(body) > maxLen {
		return body[:maxLen] + "...<truncated>"
	}
	return body
}

func dedupStrings(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	var out []string
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
