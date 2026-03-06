package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DynamicToolHandler routes dynamic tool calls from agent backends.
// Currently supports the linear_graphql tool.
type DynamicToolHandler struct {
	graphqlFn func(query string, variables map[string]any) (map[string]any, error)
}

// NewDynamicToolHandler creates a handler with the given GraphQL execution function.
func NewDynamicToolHandler(graphqlFn func(string, map[string]any) (map[string]any, error)) *DynamicToolHandler {
	return &DynamicToolHandler{graphqlFn: graphqlFn}
}

// HandleToolCall dispatches a tool call by name. Returns the result or an error.
func (h *DynamicToolHandler) HandleToolCall(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "linear_graphql":
		return h.handleLinearGraphQL(ctx, args)
	default:
		return map[string]any{
			"success": false,
			"error":   "unsupported_tool_call",
			"message": fmt.Sprintf("Tool %q is not supported", name),
		}, nil
	}
}

func (h *DynamicToolHandler) handleLinearGraphQL(_ context.Context, args map[string]any) (any, error) {
	if h.graphqlFn == nil {
		return map[string]any{
			"success": false,
			"error":   "linear_graphql_unavailable",
			"message": "Linear GraphQL is not configured",
		}, nil
	}

	// Extract query - support both structured and raw string input
	var query string
	var variables map[string]any

	switch v := args["query"].(type) {
	case string:
		query = strings.TrimSpace(v)
	default:
		// Try raw string input
		if raw, ok := args["raw"].(string); ok {
			query = strings.TrimSpace(raw)
		}
	}

	if query == "" {
		return map[string]any{
			"success": false,
			"error":   "invalid_input",
			"message": "query must be a non-empty string",
		}, nil
	}

	// Extract variables
	if vars, ok := args["variables"].(map[string]any); ok {
		variables = vars
	} else {
		variables = map[string]any{}
	}

	// Execute
	result, err := h.graphqlFn(query, variables)
	if err != nil {
		return map[string]any{
			"success": false,
			"error":   "graphql_execution_error",
			"message": err.Error(),
		}, nil
	}

	// Check for GraphQL errors
	if _, hasErrors := result["errors"]; hasErrors {
		resultJSON, _ := json.Marshal(result)
		return map[string]any{
			"success":  false,
			"error":    "graphql_errors",
			"response": json.RawMessage(resultJSON),
		}, nil
	}

	return map[string]any{
		"success":  true,
		"response": result,
	}, nil
}

// LinearGraphQLToolSpec returns the tool specification for the linear_graphql tool.
func LinearGraphQLToolSpec() ToolSpec {
	return ToolSpec{
		Name:        "linear_graphql",
		Description: "Execute a GraphQL query or mutation against the Linear API using Symphony's configured authentication. Use this to read or update issues, comments, projects, and other Linear resources.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "A single GraphQL query or mutation document",
				},
				"variables": map[string]any{
					"type":        "object",
					"description": "Optional GraphQL variables object",
				},
			},
			"required": []string{"query"},
		},
	}
}
