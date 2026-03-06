package linear

// ExecuteGraphQL is a passthrough for dynamic GraphQL operations (e.g. linear_graphql tool).
// This method, together with FetchCandidateIssues, FetchIssueStatesByIDs, and FetchIssuesByStates,
// satisfies the tracker.Tracker interface.
func (c *Client) ExecuteGraphQL(query string, variables map[string]any) (map[string]any, error) {
	return c.GraphQL(query, variables)
}
