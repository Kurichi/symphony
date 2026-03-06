package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Kurichi/symphony/internal/agent"
	"github.com/Kurichi/symphony/internal/linear"
)

// Deps holds the orchestrator's external dependencies.
type Deps struct {
	// Config accessors (re-read on each tick for dynamic reload)
	PollIntervalMs         func() int
	MaxConcurrentAgents    func() int
	MaxConcurrentForState  func(string) int
	MaxRetryBackoffMs      func() int
	ActiveStates           func() []string
	TerminalStates         func() []string
	StallTimeoutMs         func() int
	Validate               func() error
	WorkflowPrompt         func() string
	WorkspaceRoot          func() string

	// Tracker operations
	FetchCandidateIssues   func() ([]*linear.Issue, error)
	FetchIssueStatesByIDs  func([]string) ([]*linear.Issue, error)
	FetchIssuesByStates    func([]string) ([]*linear.Issue, error)

	// Agent runner
	RunAgent func(ctx context.Context, issue *linear.Issue, attempt *int, onMessage agent.MessageHandler) error

	// Workspace cleanup
	RemoveIssueWorkspace func(identifier string) error

	// Snapshot consumers
	OnStateChange func()
}

// WorkerExitResult is sent when a worker goroutine finishes.
type WorkerExitResult struct {
	IssueID string
	Normal  bool
	Error   error
}

// AgentUpdateMsg is forwarded from a running worker to the orchestrator.
type AgentUpdateMsg struct {
	IssueID string
	Event   agent.BackendEvent
}

// RetryMsg fires when a retry timer expires.
type RetryMsg struct {
	IssueID string
}

// Orchestrator is the central state machine for polling and dispatching. SPEC §7.
type Orchestrator struct {
	deps  Deps
	state *State

	workerExitCh  chan WorkerExitResult
	agentUpdateCh chan AgentUpdateMsg
	retryCh       chan RetryMsg
	refreshCh     chan chan *Snapshot
	forcePollCh   chan struct{}

	mu sync.Mutex // protects state during snapshot
}

// Snapshot is a point-in-time view of orchestrator state for the status surface.
type Snapshot struct {
	Running  []RunningSnapshot  `json:"running"`
	Retrying []RetrySnapshot    `json:"retrying"`
	Totals   *AgentTotals       `json:"totals"`
	Polling  PollingSnapshot    `json:"polling"`
}

// RunningSnapshot is a serializable view of a running entry.
type RunningSnapshot struct {
	IssueID           string     `json:"issue_id"`
	Identifier        string     `json:"identifier"`
	State             string     `json:"state"`
	SessionID         string     `json:"session_id"`
	AgentPID          string     `json:"agent_pid"`
	InputTokens       int64      `json:"input_tokens"`
	OutputTokens      int64      `json:"output_tokens"`
	TotalTokens       int64      `json:"total_tokens"`
	TurnCount         int        `json:"turn_count"`
	StartedAt         time.Time  `json:"started_at"`
	LastEvent         string     `json:"last_event,omitempty"`
	LastTimestamp      *time.Time `json:"last_timestamp,omitempty"`
	LastMessage       string     `json:"last_message,omitempty"`
	RuntimeSeconds    float64    `json:"runtime_seconds"`
}

// RetrySnapshot is a serializable view of a retry entry.
type RetrySnapshot struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Attempt    int    `json:"attempt"`
	DueInMs    int64  `json:"due_in_ms"`
	Error      string `json:"error,omitempty"`
}

// PollingSnapshot shows the current polling state.
type PollingSnapshot struct {
	Checking       bool  `json:"checking"`
	NextPollInMs   int64 `json:"next_poll_in_ms"`
	PollIntervalMs int   `json:"poll_interval_ms"`
}

// New creates a new Orchestrator with the given dependencies.
func New(deps Deps) *Orchestrator {
	return &Orchestrator{
		deps:          deps,
		workerExitCh:  make(chan WorkerExitResult, 64),
		agentUpdateCh: make(chan AgentUpdateMsg, 256),
		retryCh:       make(chan RetryMsg, 64),
		refreshCh:     make(chan chan *Snapshot, 4),
		forcePollCh:   make(chan struct{}, 1),
	}
}

// Run starts the orchestrator main loop. Blocks until context is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	// Initialize state
	o.state = NewState(
		o.deps.PollIntervalMs(),
		o.deps.MaxConcurrentAgents(),
	)

	// Run startup terminal workspace cleanup
	o.runTerminalWorkspaceCleanup()

	// Schedule first tick immediately
	ticker := time.NewTimer(0)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.shutdownWorkers()
			return ctx.Err()

		case <-ticker.C:
			o.refreshRuntimeConfig()
			o.runPollCycle(ctx)
			ticker.Reset(time.Duration(o.state.PollIntervalMs) * time.Millisecond)
			o.notifyStateChange()

		case result := <-o.workerExitCh:
			o.handleWorkerExit(ctx, result)
			o.notifyStateChange()

		case update := <-o.agentUpdateCh:
			o.handleAgentUpdate(update)
			o.notifyStateChange()

		case msg := <-o.retryCh:
			o.handleRetryFired(ctx, msg)
			o.notifyStateChange()

		case <-o.forcePollCh:
			o.refreshRuntimeConfig()
			o.runPollCycle(ctx)
			ticker.Reset(time.Duration(o.state.PollIntervalMs) * time.Millisecond)
			o.notifyStateChange()

		case replyCh := <-o.refreshCh:
			o.refreshRuntimeConfig()
			replyCh <- o.buildSnapshot()
		}
	}
}

// RequestSnapshot requests a point-in-time snapshot of orchestrator state.
func (o *Orchestrator) RequestSnapshot(timeout time.Duration) (*Snapshot, error) {
	replyCh := make(chan *Snapshot, 1)
	select {
	case o.refreshCh <- replyCh:
	case <-time.After(timeout):
		return nil, fmt.Errorf("snapshot request timeout")
	}
	select {
	case snap := <-replyCh:
		return snap, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("snapshot response timeout")
	}
}

// RequestRefresh triggers an immediate poll cycle.
func (o *Orchestrator) RequestRefresh() {
	select {
	case o.forcePollCh <- struct{}{}:
	default:
		// Already queued
	}
}

func (o *Orchestrator) runPollCycle(ctx context.Context) {
	// Part 1: Reconcile
	o.reconcileRunningIssues(ctx)

	// Part 2: Validate config
	if err := o.deps.Validate(); err != nil {
		slog.Error("Config validation failed, skipping dispatch", "error", err)
		return
	}

	// Part 3: Fetch candidates
	issues, err := o.deps.FetchCandidateIssues()
	if err != nil {
		slog.Error("Failed to fetch candidate issues", "error", err)
		return
	}

	// Part 4: Dispatch
	if AvailableSlots(o.state) <= 0 {
		return
	}
	o.chooseAndDispatch(ctx, issues)
}

func (o *Orchestrator) chooseAndDispatch(ctx context.Context, issues []*linear.Issue) {
	filter := NewCandidateFilter(o.deps.ActiveStates(), o.deps.TerminalStates())
	sorted := SortIssuesForDispatch(issues)

	for _, issue := range sorted {
		if !ShouldDispatchIssue(issue, o.state, filter, o.deps.MaxConcurrentForState) {
			continue
		}
		// Revalidate before dispatch
		refreshed, ok, err := RevalidateIssueForDispatch(issue, o.deps.FetchIssueStatesByIDs, filter)
		if err != nil {
			slog.Warn("Issue refresh failed, skipping", "issue_id", issue.ID, "error", err)
			continue
		}
		if !ok {
			slog.Info("Issue no longer eligible after refresh", "issue_id", issue.ID)
			continue
		}
		o.dispatchIssue(ctx, refreshed, nil)
		if AvailableSlots(o.state) <= 0 {
			break
		}
	}
}

func (o *Orchestrator) dispatchIssue(ctx context.Context, issue *linear.Issue, attempt *int) {
	slog.Info("Dispatching issue to agent",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"attempt", attempt,
	)

	// Claim the issue
	o.state.Claim(issue.ID)
	delete(o.state.RetryAttempts, issue.ID)

	// Create done channel and cancellable context
	runCtx, cancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})

	entry := &RunningEntry{
		IssueID:         issue.ID,
		IssueIdentifier: issue.Identifier,
		Issue:           issue,
		Attempt:         attempt,
		StartedAt:       time.Now().UTC(),
		Status:          "running",
		Cancel:          cancel,
		Done:            doneCh,
	}
	o.state.Running[issue.ID] = entry

	// Launch worker goroutine
	issueID := issue.ID
	go func() {
		defer close(doneCh)
		onMessage := func(event agent.BackendEvent) {
			o.agentUpdateCh <- AgentUpdateMsg{IssueID: issueID, Event: event}
		}
		err := o.deps.RunAgent(runCtx, issue, attempt, onMessage)
		normal := err == nil
		o.workerExitCh <- WorkerExitResult{
			IssueID: issueID,
			Normal:  normal,
			Error:   err,
		}
	}()
}

func (o *Orchestrator) handleWorkerExit(ctx context.Context, result WorkerExitResult) {
	entry, ok := o.state.Running[result.IssueID]
	if !ok {
		return
	}

	// Remove from running
	delete(o.state.Running, result.IssueID)

	// Record completion totals
	o.recordSessionTotals(entry)

	if result.Normal {
		slog.Info("Agent task completed",
			"issue_id", result.IssueID,
			"session_id", entry.SessionID,
		)
		// Schedule continuation retry
		o.state.Completed[result.IssueID] = struct{}{}
		delete(o.state.RetryAttempts, result.IssueID)
		o.scheduleRetry(result.IssueID, 1, entry.IssueIdentifier, "", true)
	} else {
		slog.Warn("Agent task failed",
			"issue_id", result.IssueID,
			"session_id", entry.SessionID,
			"error", result.Error,
		)
		nextAttempt := 1
		if entry.Attempt != nil && *entry.Attempt > 0 {
			nextAttempt = *entry.Attempt + 1
		}
		errMsg := ""
		if result.Error != nil {
			errMsg = result.Error.Error()
		}
		o.scheduleRetry(result.IssueID, nextAttempt, entry.IssueIdentifier, errMsg, false)
	}
}

func (o *Orchestrator) handleAgentUpdate(msg AgentUpdateMsg) {
	entry, ok := o.state.Running[msg.IssueID]
	if !ok {
		return
	}

	event := msg.Event
	now := time.Now().UTC()

	entry.LastAgentEvent = event.Type
	entry.LastAgentTimestamp = &now
	entry.LastAgentMessage = fmt.Sprintf("%v", event.Data)

	// Track turn count on session_started BEFORE updating SessionID
	if event.Type == "session_started" && event.SessionID != "" && event.SessionID != entry.SessionID {
		entry.TurnCount++
	}

	if event.SessionID != "" {
		entry.SessionID = event.SessionID
	}
	if event.AgentPID != "" {
		entry.AgentPID = event.AgentPID
	}

	// Update token counts
	if event.TokenUsage != nil {
		delta := computeTokenDelta(entry, event.TokenUsage)
		entry.AgentInputTokens += delta.InputTokens
		entry.AgentOutputTokens += delta.OutputTokens
		entry.AgentTotalTokens += delta.TotalTokens
		entry.LastReportedInputTokens = max64(entry.LastReportedInputTokens, event.TokenUsage.InputTokens)
		entry.LastReportedOutputTokens = max64(entry.LastReportedOutputTokens, event.TokenUsage.OutputTokens)
		entry.LastReportedTotalTokens = max64(entry.LastReportedTotalTokens, event.TokenUsage.TotalTokens)

		// Update global totals
		if o.state.AgentTotals != nil {
			o.state.AgentTotals.InputTokens += delta.InputTokens
			o.state.AgentTotals.OutputTokens += delta.OutputTokens
			o.state.AgentTotals.TotalTokens += delta.TotalTokens
		}
	}
}

func computeTokenDelta(entry *RunningEntry, usage *agent.TokenUsage) agent.TokenUsage {
	return agent.TokenUsage{
		InputTokens:  maxDelta(usage.InputTokens, entry.LastReportedInputTokens),
		OutputTokens: maxDelta(usage.OutputTokens, entry.LastReportedOutputTokens),
		TotalTokens:  maxDelta(usage.TotalTokens, entry.LastReportedTotalTokens),
	}
}

func maxDelta(reported, lastReported int64) int64 {
	delta := reported - lastReported
	if delta < 0 {
		return 0
	}
	return delta
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (o *Orchestrator) handleRetryFired(ctx context.Context, msg RetryMsg) {
	retryEntry, ok := o.state.RetryAttempts[msg.IssueID]
	if !ok {
		return
	}
	attempt := retryEntry.Attempt
	identifier := retryEntry.Identifier
	retryError := retryEntry.Error
	delete(o.state.RetryAttempts, msg.IssueID)

	// Fetch current candidates
	issues, err := o.deps.FetchCandidateIssues()
	if err != nil {
		slog.Warn("Retry poll failed",
			"issue_id", msg.IssueID,
			"identifier", identifier,
			"error", err,
		)
		o.scheduleRetry(msg.IssueID, attempt+1, identifier, "retry poll failed: "+err.Error(), false)
		return
	}

	// Find the specific issue
	var found *linear.Issue
	for _, issue := range issues {
		if issue.ID == msg.IssueID {
			found = issue
			break
		}
	}

	if found == nil {
		slog.Debug("Issue no longer visible, removing claim", "issue_id", msg.IssueID)
		o.state.Release(msg.IssueID)
		return
	}

	filter := NewCandidateFilter(o.deps.ActiveStates(), o.deps.TerminalStates())

	// Check terminal
	if isTerminalIssueState(found.State, filter.TerminalStates) {
		slog.Info("Issue in terminal state during retry",
			"issue_id", msg.IssueID,
			"identifier", found.Identifier,
			"state", found.State,
		)
		if o.deps.RemoveIssueWorkspace != nil {
			_ = o.deps.RemoveIssueWorkspace(found.Identifier)
		}
		o.state.Release(msg.IssueID)
		return
	}

	// Check if still candidate
	if isRetryCandidateIssue(found, filter) && AvailableSlots(o.state) > 0 &&
		stateSlotsAvailable(found, o.state, o.deps.MaxConcurrentForState) {
		attemptVal := attempt
		o.dispatchIssue(ctx, found, &attemptVal)
	} else {
		slog.Debug("No available slots for retry",
			"issue_id", msg.IssueID,
			"identifier", found.Identifier,
		)
		_ = retryError // preserve for logging context
		o.scheduleRetry(msg.IssueID, attempt+1, found.Identifier, "no available orchestrator slots", false)
	}
}

func (o *Orchestrator) scheduleRetry(issueID string, attempt int, identifier string, errMsg string, isContinuation bool) {
	// Cancel existing timer
	if existing, ok := o.state.RetryAttempts[issueID]; ok {
		if existing.Timer != nil {
			existing.Timer.Stop()
		}
	}

	delay := RetryDelay(attempt, isContinuation, o.deps.MaxRetryBackoffMs())
	dueAt := time.Now().Add(delay).UnixMilli()

	errSuffix := ""
	if errMsg != "" {
		errSuffix = " error=" + errMsg
	}
	slog.Warn("Retrying issue",
		"issue_id", issueID,
		"identifier", identifier,
		"delay_ms", delay.Milliseconds(),
		"attempt", attempt,
		"continuation", isContinuation,
	)
	_ = errSuffix

	timer := time.AfterFunc(delay, func() {
		o.retryCh <- RetryMsg{IssueID: issueID}
	})

	o.state.RetryAttempts[issueID] = &RetryEntry{
		IssueID:    issueID,
		Identifier: identifier,
		Attempt:    attempt,
		DueAtMs:    dueAt,
		Error:      errMsg,
		Timer:      timer,
	}
}

func (o *Orchestrator) reconcileRunningIssues(ctx context.Context) {
	// Part A: Stall detection
	stallTimeoutMs := o.deps.StallTimeoutMs()
	stalledIDs, _ := ReconcileStalledIssues(o.state, stallTimeoutMs, time.Now().UTC())
	for _, issueID := range stalledIDs {
		entry := o.state.Running[issueID]
		if entry == nil {
			continue
		}
		nextAttempt := 1
		if entry.Attempt != nil && *entry.Attempt > 0 {
			nextAttempt = *entry.Attempt + 1
		}
		o.terminateRunningIssue(issueID, false)
		elapsed := stallElapsedMs(entry, time.Now().UTC())
		o.scheduleRetry(issueID, nextAttempt, entry.IssueIdentifier,
			fmt.Sprintf("stalled for %dms without agent activity", elapsed), false)
	}

	// Part B: Tracker state refresh
	runningIDs := make([]string, 0, len(o.state.Running))
	for id := range o.state.Running {
		runningIDs = append(runningIDs, id)
	}
	if len(runningIDs) == 0 {
		return
	}

	issues, err := o.deps.FetchIssueStatesByIDs(runningIDs)
	if err != nil {
		slog.Debug("Failed to refresh running issue states, keeping active workers", "error", err)
		return
	}

	filter := NewCandidateFilter(o.deps.ActiveStates(), o.deps.TerminalStates())
	result := ReconcileIssueStates(issues, o.state, filter)

	for _, issueID := range result.Terminal {
		o.terminateRunningIssue(issueID, true)
	}
	for _, issueID := range result.NonActive {
		o.terminateRunningIssue(issueID, false)
	}
	for _, issueID := range result.Unroutable {
		o.terminateRunningIssue(issueID, false)
	}
}

func (o *Orchestrator) terminateRunningIssue(issueID string, cleanupWorkspace bool) {
	entry, ok := o.state.Running[issueID]
	if !ok {
		o.state.Release(issueID)
		return
	}

	o.recordSessionTotals(entry)

	if cleanupWorkspace && o.deps.RemoveIssueWorkspace != nil {
		_ = o.deps.RemoveIssueWorkspace(entry.IssueIdentifier)
	}

	if entry.Cancel != nil {
		entry.Cancel()
	}

	delete(o.state.Running, issueID)
	o.state.Release(issueID)
	delete(o.state.RetryAttempts, issueID)
}

func (o *Orchestrator) recordSessionTotals(entry *RunningEntry) {
	if entry == nil || o.state.AgentTotals == nil {
		return
	}
	runtime := RunningSeconds(entry.StartedAt)
	o.state.AgentTotals.SecondsRunning += runtime
}

func (o *Orchestrator) runTerminalWorkspaceCleanup() {
	terminalStates := o.deps.TerminalStates()
	issues, err := o.deps.FetchIssuesByStates(terminalStates)
	if err != nil {
		slog.Warn("Skipping startup terminal workspace cleanup", "error", err)
		return
	}
	for _, issue := range issues {
		if issue.Identifier != "" && o.deps.RemoveIssueWorkspace != nil {
			_ = o.deps.RemoveIssueWorkspace(issue.Identifier)
		}
	}
}

func (o *Orchestrator) refreshRuntimeConfig() {
	o.state.PollIntervalMs = o.deps.PollIntervalMs()
	o.state.MaxConcurrentAgents = o.deps.MaxConcurrentAgents()
}

func (o *Orchestrator) shutdownWorkers() {
	for issueID, entry := range o.state.Running {
		slog.Info("Shutting down worker", "issue_id", issueID)
		if entry.Cancel != nil {
			entry.Cancel()
		}
	}
	// Cancel retry timers
	for _, retry := range o.state.RetryAttempts {
		if retry.Timer != nil {
			retry.Timer.Stop()
		}
	}
}

func (o *Orchestrator) notifyStateChange() {
	if o.deps.OnStateChange != nil {
		o.deps.OnStateChange()
	}
}

func (o *Orchestrator) buildSnapshot() *Snapshot {
	now := time.Now().UTC()
	nowMs := time.Now().UnixMilli()

	running := make([]RunningSnapshot, 0, len(o.state.Running))
	for _, entry := range o.state.Running {
		running = append(running, RunningSnapshot{
			IssueID:        entry.IssueID,
			Identifier:     entry.IssueIdentifier,
			State:          issueState(entry),
			SessionID:      entry.SessionID,
			AgentPID:       entry.AgentPID,
			InputTokens:    entry.AgentInputTokens,
			OutputTokens:   entry.AgentOutputTokens,
			TotalTokens:    entry.AgentTotalTokens,
			TurnCount:      entry.TurnCount,
			StartedAt:      entry.StartedAt,
			LastEvent:      entry.LastAgentEvent,
			LastTimestamp:   entry.LastAgentTimestamp,
			LastMessage:    entry.LastAgentMessage,
			RuntimeSeconds: now.Sub(entry.StartedAt).Seconds(),
		})
	}

	retrying := make([]RetrySnapshot, 0, len(o.state.RetryAttempts))
	for _, retry := range o.state.RetryAttempts {
		dueIn := retry.DueAtMs - nowMs
		if dueIn < 0 {
			dueIn = 0
		}
		retrying = append(retrying, RetrySnapshot{
			IssueID:    retry.IssueID,
			Identifier: retry.Identifier,
			Attempt:    retry.Attempt,
			DueInMs:    dueIn,
			Error:      retry.Error,
		})
	}

	return &Snapshot{
		Running:  running,
		Retrying: retrying,
		Totals:   o.state.AgentTotals,
		Polling: PollingSnapshot{
			PollIntervalMs: o.state.PollIntervalMs,
		},
	}
}

func issueState(entry *RunningEntry) string {
	if entry.Issue != nil {
		return entry.Issue.State
	}
	return ""
}
