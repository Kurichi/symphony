package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Kurichi/symphony/internal/agent"
)

// BackendOpts configures the Claude Code CLI backend.
type BackendOpts struct {
	Command      string
	AllowedTools []string
	OnMessage    agent.MessageHandler
	// WorkflowPath is the absolute path to WORKFLOW.md, passed to mcp-tools subcommand.
	WorkflowPath string
}

// Backend implements agent.Backend for the Claude Code CLI.
type Backend struct {
	command      string
	allowedTools []string
	onMessage    agent.MessageHandler
	workflowPath string
}

// NewBackend creates a Claude Code CLI backend.
func NewBackend(opts BackendOpts) *Backend {
	b := &Backend{
		command:      opts.Command,
		allowedTools: opts.AllowedTools,
		onMessage:    opts.OnMessage,
		workflowPath: opts.WorkflowPath,
	}
	if b.command == "" {
		b.command = "claude"
	}
	if b.onMessage == nil {
		b.onMessage = func(agent.BackendEvent) {}
	}
	return b
}

// StartSession creates a new Claude Code session. Each session tracks a
// session ID so subsequent turns can use --resume.
func (b *Backend) StartSession(_ context.Context, opts agent.SessionOpts) (agent.Session, error) {
	onMsg := opts.OnMessage
	if onMsg == nil {
		onMsg = b.onMessage
	}
	return &session{
		command:       b.command,
		allowedTools:  b.allowedTools,
		workspacePath: opts.WorkspacePath,
		dynamicTools:  opts.DynamicTools,
		onMessage:     onMsg,
		workflowPath:  b.workflowPath,
	}, nil
}

// session tracks state across Claude Code CLI invocations for multi-turn usage.
type session struct {
	command       string
	allowedTools  []string
	workspacePath string
	dynamicTools  []agent.ToolSpec
	onMessage     agent.MessageHandler
	workflowPath  string
	sessionID     string // set after first turn
}

func (s *session) ID() string {
	return s.sessionID
}

func (s *session) Close() error {
	return nil
}

// RunTurn spawns a single Claude Code CLI invocation. Uses --resume for
// subsequent turns within the same session.
func (s *session) RunTurn(ctx context.Context, prompt string, _ agent.TurnOpts) (*agent.TurnResult, error) {
	args := s.buildArgs(prompt)

	slog.Debug("starting claude code turn", "args", args, "cwd", s.workspacePath)

	cmd := exec.CommandContext(ctx, s.command, args...)
	cmd.Dir = s.workspacePath
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	result := s.processStreamOutput(stdout)

	if err := cmd.Wait(); err != nil {
		result.Status = agent.TurnFailed
		if result.Error == "" {
			result.Error = err.Error()
		}
	}

	// Capture session ID from the first turn for subsequent --resume calls.
	if result.SessionID != "" && s.sessionID == "" {
		s.sessionID = result.SessionID
	}

	return result, nil
}

// buildArgs constructs the CLI arguments for a Claude Code invocation.
func (s *session) buildArgs(prompt string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--max-turns", "1",
	}

	if s.workspacePath != "" {
		args = append(args, "--cwd", s.workspacePath)
	}

	if len(s.allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(s.allowedTools, ","))
	}

	// If we have dynamic tools, generate an MCP config for them.
	if len(s.dynamicTools) > 0 {
		mcpPath := s.writeMCPConfig()
		if mcpPath != "" {
			args = append(args, "--mcp-config", mcpPath)
		}
	}

	// Resume existing session for multi-turn.
	if s.sessionID != "" {
		args = append(args, "--resume", s.sessionID)
	}

	return args
}

// writeMCPConfig generates a temporary MCP config file that points Claude Code
// to this Symphony process's MCP server for dynamic tools.
func (s *session) writeMCPConfig() string {
	selfExe, err := os.Executable()
	if err != nil {
		slog.Warn("cannot resolve self executable for MCP config", "error", err)
		return ""
	}

	config := map[string]any{
		"mcpServers": map[string]any{
			"symphony": map[string]any{
				"command": selfExe,
				"args":    []string{"mcp-tools", s.workflowPath},
			},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		return ""
	}

	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("symphony-mcp-%s.json", s.sessionID))
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		slog.Warn("cannot write MCP config", "error", err)
		return ""
	}
	return tmpFile
}

// processStreamOutput reads newline-delimited JSON from Claude Code's
// stream-json output and extracts events.
func (s *session) processStreamOutput(r io.Reader) *agent.TurnResult {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	result := &agent.TurnResult{Status: agent.TurnCompleted}
	var usage agent.TokenUsage

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "system":
			if sid, ok := event["session_id"].(string); ok {
				result.SessionID = sid
			}

		case "result":
			if u, ok := event["usage"].(map[string]any); ok {
				usage = extractUsage(u)
			}
			result.TokenUsage = usage
			s.onMessage(agent.BackendEvent{Type: "turn_completed", SessionID: result.SessionID})

		case "error":
			errMsg, _ := event["error"].(string)
			if errMsg == "" {
				errMsg = "unknown error"
			}
			result.Status = agent.TurnFailed
			result.Error = errMsg
			s.onMessage(agent.BackendEvent{Type: "error", Data: map[string]any{"error": errMsg}})

		case "tool_use":
			toolName, _ := event["tool"].(string)
			s.onMessage(agent.BackendEvent{Type: "tool_call", Data: map[string]any{"tool": toolName}})

		case "message":
			s.onMessage(agent.BackendEvent{Type: "message", Data: event})

		default:
			s.onMessage(agent.BackendEvent{Type: eventType, Data: event})
		}
	}

	return result
}

func extractUsage(u map[string]any) agent.TokenUsage {
	return agent.TokenUsage{
		InputTokens:  int64(floatVal(u, "input_tokens")),
		OutputTokens: int64(floatVal(u, "output_tokens")),
		TotalTokens:  int64(floatVal(u, "total_tokens")),
	}
}

func floatVal(m map[string]any, key string) float64 {
	v, ok := m[key].(float64)
	if ok {
		return v
	}
	return 0
}
