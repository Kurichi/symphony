package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// Server is an MCP (Model Context Protocol) stdio server that exposes the
// linear_graphql tool to Claude Code via --mcp-config.
type Server struct {
	graphqlFn func(query string, variables map[string]any) (map[string]any, error)
}

// NewServer creates an MCP server backed by the given GraphQL executor function.
func NewServer(graphqlFn func(string, map[string]any) (map[string]any, error)) *Server {
	return &Server{graphqlFn: graphqlFn}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      any             `json:"id,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

// Run reads JSON-RPC requests from stdin and writes responses to stdout.
// It blocks until the context is cancelled or stdin is closed.
func (s *Server) Run(ctx context.Context) error {
	reader := bufio.NewReaderSize(os.Stdin, 1024*1024)
	writer := os.Stdout

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read stdin: %w", err)
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			slog.Debug("ignoring non-JSON input", "error", err)
			continue
		}

		resp := s.handleRequest(req)
		if resp == nil {
			continue // notification, no response needed
		}

		data, err := json.Marshal(resp)
		if err != nil {
			slog.Error("marshal response failed", "error", err)
			continue
		}
		data = append(data, '\n')
		if _, err := writer.Write(data); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
	}
}

func (s *Server) handleRequest(req rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized":
		return nil // notification
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}

func (s *Server) handleInitialize(req rpcRequest) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "symphony-mcp",
				"version": "0.1.0",
			},
		},
	}
}

func (s *Server) handleToolsList(req rpcRequest) *rpcResponse {
	tools := []map[string]any{
		{
			"name":        "linear_graphql",
			"description": "Execute a raw GraphQL query or mutation against Linear using Symphony's configured auth.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "GraphQL query or mutation document to execute against Linear.",
					},
					"variables": map[string]any{
						"type":                 []string{"object", "null"},
						"description":          "Optional GraphQL variables object.",
						"additionalProperties": true,
					},
				},
			},
		},
	}

	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"tools": tools,
		},
	}
}

func (s *Server) handleToolsCall(req rpcRequest) *rpcResponse {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: map[string]any{
				"code":    -32602,
				"message": "invalid params: " + err.Error(),
			},
		}
	}

	switch params.Name {
	case "linear_graphql":
		return s.executeLinearGraphQL(req.ID, params.Arguments)
	default:
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": fmt.Sprintf("Unknown tool: %s", params.Name),
					},
				},
				"isError": true,
			},
		}
	}
}

func (s *Server) executeLinearGraphQL(id any, args map[string]any) *rpcResponse {
	query, _ := args["query"].(string)
	if query == "" {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "`linear_graphql` requires a non-empty `query` string."},
				},
				"isError": true,
			},
		}
	}

	variables, _ := args["variables"].(map[string]any)

	resp, err := s.graphqlFn(query, variables)
	if err != nil {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result: map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": fmt.Sprintf("Linear GraphQL error: %v", err)},
				},
				"isError": true,
			},
		}
	}

	text, _ := json.MarshalIndent(resp, "", "  ")

	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": string(text)},
			},
		},
	}
}
