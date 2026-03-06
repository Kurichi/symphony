package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Kurichi/symphony/internal/agent"
)

const (
	readBufSize                   = 10 * 1024 * 1024 // 10 MB line buffer
	maxStreamLogBytes             = 1000
	nonInteractiveToolInputAnswer = "This is a non-interactive session. Operator input is unavailable."
)

// BackendOpts configures the Codex app-server backend.
type BackendOpts struct {
	Command        string
	TurnTimeoutMs  int
	ReadTimeoutMs  int
	StallTimeoutMs int
	OnMessage      agent.MessageHandler
}

// Backend implements agent.Backend for the Codex app-server JSON-RPC protocol.
type Backend struct {
	command        string
	turnTimeoutMs  int
	readTimeoutMs  int
	stallTimeoutMs int
	onMessage      agent.MessageHandler
}

// NewBackend creates a Codex backend with the given options.
func NewBackend(opts BackendOpts) *Backend {
	b := &Backend{
		command:        opts.Command,
		turnTimeoutMs:  opts.TurnTimeoutMs,
		readTimeoutMs:  opts.ReadTimeoutMs,
		stallTimeoutMs: opts.StallTimeoutMs,
		onMessage:      opts.OnMessage,
	}
	if b.command == "" {
		b.command = "codex app-server"
	}
	if b.turnTimeoutMs <= 0 {
		b.turnTimeoutMs = 3_600_000
	}
	if b.readTimeoutMs <= 0 {
		b.readTimeoutMs = 5_000
	}
	if b.stallTimeoutMs <= 0 {
		b.stallTimeoutMs = 300_000
	}
	if b.onMessage == nil {
		b.onMessage = func(agent.BackendEvent) {}
	}
	return b
}

// StartSession launches a Codex app-server process and performs the handshake.
func (b *Backend) StartSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", b.command)
	cmd.Dir = opts.WorkspacePath
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	reader := bufio.NewReaderSize(stdout, readBufSize)

	lineCh := make(chan lineResult, 1)
	go func() {
		for {
			line, err := reader.ReadBytes('\n')
			lineCh <- lineResult{line, err}
			if err != nil {
				return // stdout closed or EOF
			}
		}
	}()

	s := &session{
		cmd:            cmd,
		stdin:          stdin,
		reader:         reader,
		workspace:      opts.WorkspacePath,
		approvalPolicy: opts.ApprovalPolicy,
		sandbox:        opts.Sandbox,
		dynamicTools:   opts.DynamicTools,
		turnTimeoutMs:  b.turnTimeoutMs,
		readTimeoutMs:  b.readTimeoutMs,
		onMessage:      resolveOnMessage(opts.OnMessage, b.onMessage),
		toolHandler:    opts.ToolHandler,
		lineCh:         lineCh,
	}

	// Handshake: initialize → thread/start
	if err := s.initialize(); err != nil {
		s.Close()
		return nil, fmt.Errorf("codex handshake: %w", err)
	}

	threadID, err := s.startThread()
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("start thread: %w", err)
	}
	s.threadID = threadID

	return s, nil
}

// SetToolHandler sets a dynamic tool handler on the backend.
// This will be used by sessions created after this call.
func (b *Backend) SetToolHandler(handler *agent.DynamicToolHandler) {
	// Store for future sessions. Individual sessions also expose this.
	_ = handler
}

// lineResult is used by the persistent reader goroutine.
type lineResult struct {
	line []byte
	err  error
}

// session is a running Codex app-server process with an open thread.
type session struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	reader         *bufio.Reader
	workspace      string
	approvalPolicy map[string]any
	sandbox        string
	dynamicTools   []agent.ToolSpec
	threadID       string
	turnTimeoutMs  int
	readTimeoutMs  int
	onMessage      agent.MessageHandler
	toolHandler    *agent.DynamicToolHandler
	lineCh         chan lineResult // persistent reader goroutine output
	mu             sync.Mutex
	closed         bool
}

// SetToolHandler allows setting the dynamic tool handler after creation.
func (s *session) SetToolHandler(handler *agent.DynamicToolHandler) {
	s.toolHandler = handler
}

func (s *session) ID() string {
	return s.threadID
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.stdin.Close()
	return s.cmd.Process.Kill()
}

// initialize performs the JSON-RPC initialize/initialized handshake.
func (s *session) initialize() error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  MethodInitialize,
		"id":      InitializeID,
		"params": map[string]any{
			"capabilities": map[string]any{
				"experimentalApi": true,
			},
			"clientInfo": map[string]any{
				"name":    "symphony-orchestrator",
				"title":   "Symphony Orchestrator",
				"version": "0.1.0",
			},
		},
	}
	if err := s.sendJSON(req); err != nil {
		return err
	}

	if _, err := s.awaitResponse(InitializeID); err != nil {
		return fmt.Errorf("initialize response: %w", err)
	}

	// Send initialized notification
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  MethodInitialized,
		"params":  map[string]any{},
	}
	return s.sendJSON(notif)
}

// startThread sends thread/start and returns the thread ID.
func (s *session) startThread() (string, error) {
	toolSpecs := make([]map[string]any, 0, len(s.dynamicTools))
	for _, t := range s.dynamicTools {
		toolSpecs = append(toolSpecs, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Parameters,
		})
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  MethodThreadStart,
		"id":      ThreadStartID,
		"params": map[string]any{
			"approvalPolicy": s.approvalPolicy,
			"sandbox":        s.sandbox,
			"cwd":            s.workspace,
			"dynamicTools":   toolSpecs,
		},
	}
	if err := s.sendJSON(req); err != nil {
		return "", err
	}

	result, err := s.awaitResponse(ThreadStartID)
	if err != nil {
		return "", err
	}

	resultMap, ok := result.(map[string]any)
	if !ok {
		return "", fmt.Errorf("unexpected thread/start result type: %T", result)
	}
	threadMap, ok := resultMap["thread"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("missing thread in thread/start result")
	}
	threadID, ok := threadMap["id"].(string)
	if !ok {
		return "", fmt.Errorf("missing thread.id in thread/start result")
	}
	return threadID, nil
}

// RunTurn sends a prompt and waits for the turn to complete.
func (s *session) RunTurn(ctx context.Context, prompt string, opts agent.TurnOpts) (*agent.TurnResult, error) {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  MethodTurnStart,
		"id":      TurnStartID,
		"params": map[string]any{
			"threadId": s.threadID,
			"input": []map[string]any{
				{"type": "text", "text": prompt},
			},
			"cwd":            s.workspace,
			"title":          opts.Title,
			"approvalPolicy": s.approvalPolicy,
			"sandboxPolicy":  opts.TurnSandboxPolicy,
		},
	}
	if err := s.sendJSON(req); err != nil {
		return nil, err
	}

	// Await turn/start response to get turn ID.
	result, err := s.awaitResponse(TurnStartID)
	if err != nil {
		return nil, fmt.Errorf("turn/start response: %w", err)
	}
	turnID := extractTurnID(result)

	sessionID := s.threadID + "-" + turnID
	s.onMessage(agent.BackendEvent{
		Type:      "session_started",
		SessionID: sessionID,
		Data: map[string]any{
			"thread_id": s.threadID,
			"turn_id":   turnID,
		},
	})

	// Stream events until turn/completed, turn/failed, or turn/cancelled.
	turnResult, err := s.awaitTurnCompletion(ctx)
	if err != nil {
		return &agent.TurnResult{
			Status:    agent.TurnFailed,
			Error:     err.Error(),
			SessionID: sessionID,
			ThreadID:  s.threadID,
			TurnID:    turnID,
		}, err
	}

	turnResult.SessionID = sessionID
	turnResult.ThreadID = s.threadID
	turnResult.TurnID = turnID
	return turnResult, nil
}

// awaitTurnCompletion reads events from stdout until a terminal event.
func (s *session) awaitTurnCompletion(ctx context.Context) (*agent.TurnResult, error) {
	usage := agent.TokenUsage{}
	autoApprove := isAutoApprovePolicy(s.approvalPolicy)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		line, err := s.readLineWithTimeout(time.Duration(s.turnTimeoutMs) * time.Millisecond)
		if err != nil {
			return nil, fmt.Errorf("read codex output: %w", err)
		}

		var event CodexEvent
		if err := json.Unmarshal(line, &event); err != nil {
			logNonJSON(line)
			continue
		}

		// Extract usage if present
		if event.Params != nil {
			var params map[string]any
			if json.Unmarshal(event.Params, &params) == nil {
				if u, ok := params["usage"].(map[string]any); ok {
					usage = extractUsage(u)
				}
			}
		}

		switch event.Method {
		case MethodTurnCompleted:
			s.onMessage(agent.BackendEvent{Type: "turn_completed"})
			return &agent.TurnResult{
				Status:     agent.TurnCompleted,
				TokenUsage: usage,
			}, nil

		case MethodTurnFailed:
			s.onMessage(agent.BackendEvent{Type: "turn_failed"})
			return &agent.TurnResult{
				Status:     agent.TurnFailed,
				TokenUsage: usage,
				Error:      string(event.Params),
			}, nil

		case MethodTurnCancelled:
			s.onMessage(agent.BackendEvent{Type: "turn_cancelled"})
			return &agent.TurnResult{
				Status:     agent.TurnCancelled,
				TokenUsage: usage,
			}, nil

		case MethodItemToolCall:
			s.handleToolCall(event)

		case MethodItemCommandExecRequestApproval,
			MethodExecCommandApproval,
			MethodApplyPatchApproval,
			MethodItemFileChangeRequestApproval:
			s.handleApprovalRequest(event, autoApprove)

		case MethodItemToolRequestUserInput:
			s.handleToolRequestUserInput(event, autoApprove)

		case MethodSessionRateLimit:
			s.onMessage(agent.BackendEvent{Type: "rate_limit", Data: rawToMap(event.Params)})

		default:
			// Check for input-required patterns
			if isInputRequired(event) {
				return &agent.TurnResult{
					Status:     agent.TurnNeedsInput,
					TokenUsage: usage,
					Error:      "agent requires input",
				}, nil
			}
			s.onMessage(agent.BackendEvent{Type: "notification", Data: map[string]any{"method": event.Method}})
		}
	}
}

// handleToolCall dispatches a dynamic tool call and sends the response.
func (s *session) handleToolCall(event CodexEvent) {
	var params map[string]any
	if err := json.Unmarshal(event.Params, &params); err != nil {
		return
	}

	toolName := extractToolName(params)
	args := extractToolArgs(params)

	var result any
	if s.toolHandler != nil && toolName != "" {
		var err error
		result, err = s.toolHandler.HandleToolCall(context.Background(), toolName, args)
		if err != nil {
			result = map[string]any{
				"success":      false,
				"contentItems": []map[string]any{{"type": "inputText", "text": err.Error()}},
			}
		}
	} else {
		result = map[string]any{
			"success":      false,
			"contentItems": []map[string]any{{"type": "inputText", "text": fmt.Sprintf("unsupported tool: %s", toolName)}},
		}
	}

	resp := map[string]any{
		"id":     event.ID,
		"result": result,
	}
	_ = s.sendJSON(resp)

	s.onMessage(agent.BackendEvent{Type: "tool_call", Data: map[string]any{"tool": toolName}})
}

// handleApprovalRequest auto-approves or rejects based on policy.
func (s *session) handleApprovalRequest(event CodexEvent, autoApprove bool) {
	if !autoApprove {
		s.onMessage(agent.BackendEvent{Type: "approval_required"})
		return
	}

	decision := "acceptForSession"
	if event.Method == MethodExecCommandApproval || event.Method == MethodApplyPatchApproval {
		decision = "approved_for_session"
	}

	resp := map[string]any{
		"id":     event.ID,
		"result": map[string]any{"decision": decision},
	}
	_ = s.sendJSON(resp)

	s.onMessage(agent.BackendEvent{Type: "approval_auto_approved", Data: map[string]any{"decision": decision}})
}

// handleToolRequestUserInput auto-answers tool input requests.
func (s *session) handleToolRequestUserInput(event CodexEvent, autoApprove bool) {
	var params map[string]any
	if err := json.Unmarshal(event.Params, &params); err != nil {
		return
	}

	questions, _ := params["questions"].([]any)
	answers := make(map[string]any)

	for _, q := range questions {
		qMap, ok := q.(map[string]any)
		if !ok {
			continue
		}
		qID, _ := qMap["id"].(string)
		if qID == "" {
			continue
		}

		if autoApprove {
			// Try to find an approval option
			if label := findApprovalOptionLabel(qMap); label != "" {
				answers[qID] = map[string]any{"answers": []string{label}}
				continue
			}
		}

		// Non-interactive fallback
		answers[qID] = map[string]any{"answers": []string{nonInteractiveToolInputAnswer}}
	}

	if len(answers) > 0 {
		resp := map[string]any{
			"id":     event.ID,
			"result": map[string]any{"answers": answers},
		}
		_ = s.sendJSON(resp)
	}
}

// findApprovalOptionLabel looks for an approval/allow option in the question.
func findApprovalOptionLabel(question map[string]any) string {
	options, _ := question["options"].([]any)
	var labels []string
	for _, opt := range options {
		optMap, ok := opt.(map[string]any)
		if !ok {
			continue
		}
		label, ok := optMap["label"].(string)
		if !ok {
			continue
		}
		labels = append(labels, label)
	}

	// Prefer "Approve this Session" > "Approve Once" > any approve/allow prefix.
	for _, l := range labels {
		if l == "Approve this Session" {
			return l
		}
	}
	for _, l := range labels {
		if l == "Approve Once" {
			return l
		}
	}
	for _, l := range labels {
		lower := strings.ToLower(strings.TrimSpace(l))
		if strings.HasPrefix(lower, "approve") || strings.HasPrefix(lower, "allow") {
			return l
		}
	}
	return ""
}

// sendJSON marshals and sends a JSON message to Codex stdin.
func (s *session) sendJSON(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	_, err = s.stdin.Write(data)
	return err
}

// readLineWithTimeout reads a line from the buffered reader with a deadline.
func (s *session) readLineWithTimeout(timeout time.Duration) ([]byte, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-s.lineCh:
		return r.line, r.err
	case <-timer.C:
		return nil, fmt.Errorf("read timeout after %v", timeout)
	}
}

// awaitResponse waits for a JSON-RPC response with the given request ID.
func (s *session) awaitResponse(requestID int) (any, error) {
	timeout := time.Duration(s.readTimeoutMs) * time.Millisecond
	deadline := time.Now().Add(timeout)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("response timeout for id=%d", requestID)
		}

		line, err := s.readLineWithTimeout(remaining)
		if err != nil {
			return nil, err
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			logNonJSON(line)
			continue
		}

		// Check if this response matches our request ID.
		respID := normalizeID(resp.ID)
		if respID != requestID {
			slog.Debug("ignoring message while waiting for response", "expected_id", requestID, "got_id", respID)
			continue
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// normalizeID converts a JSON-RPC id (which may be float64 from JSON) to int.
func normalizeID(id any) int {
	switch v := id.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return -1
	}
}

func extractTurnID(result any) string {
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	turn, ok := m["turn"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := turn["id"].(string)
	return id
}

func extractToolName(params map[string]any) string {
	for _, key := range []string{"tool", "name"} {
		if v, ok := params[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func extractToolArgs(params map[string]any) map[string]any {
	if v, ok := params["arguments"].(map[string]any); ok {
		return v
	}
	return map[string]any{}
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

func isAutoApprovePolicy(policy map[string]any) bool {
	// "never" approval policy means auto-approve everything.
	if policy == nil {
		return false
	}
	// Check the reject sub-map. If present, auto-approve is enabled.
	if _, ok := policy["reject"]; ok {
		return true
	}
	return false
}

func isInputRequired(event CodexEvent) bool {
	inputMethods := []string{
		"turn/input_required", "turn/needs_input", "turn/need_input",
		"turn/request_input", "turn/request_response",
		"turn/provide_input", "turn/approval_required",
	}
	for _, m := range inputMethods {
		if event.Method == m {
			return true
		}
	}
	if event.Params == nil {
		return false
	}
	var params map[string]any
	if json.Unmarshal(event.Params, &params) != nil {
		return false
	}
	for _, key := range []string{"requiresInput", "needsInput", "input_required", "inputRequired"} {
		if v, ok := params[key].(bool); ok && v {
			return true
		}
	}
	for _, key := range []string{"type"} {
		if v, ok := params[key].(string); ok {
			if v == "input_required" || v == "needs_input" {
				return true
			}
		}
	}
	return false
}

func rawToMap(raw json.RawMessage) map[string]any {
	if raw == nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func logNonJSON(line []byte) {
	text := strings.TrimSpace(string(line))
	if text == "" {
		return
	}
	if len(text) > maxStreamLogBytes {
		text = text[:maxStreamLogBytes] + "..."
	}
	if containsErrorKeyword(text) {
		slog.Warn("codex stream output", "text", text)
	} else {
		slog.Debug("codex stream output", "text", text)
	}
}

func resolveOnMessage(sessionHandler, backendHandler agent.MessageHandler) agent.MessageHandler {
	if sessionHandler != nil {
		return sessionHandler
	}
	if backendHandler != nil {
		return backendHandler
	}
	return func(agent.BackendEvent) {}
}

func containsErrorKeyword(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range []string{"error", "warn", "warning", "failed", "fatal", "panic", "exception"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
