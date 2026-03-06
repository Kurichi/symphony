package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Kurichi/symphony/internal/linear"
)

// RunnerConfig holds configuration for an agent runner.
type RunnerConfig struct {
	MaxTurns          int
	Backend           Backend
	WorkspacePath     string
	ApprovalPolicy    map[string]any
	Sandbox           string
	TurnSandboxPolicy map[string]any
	DynamicTools      []ToolSpec
	OnMessage         MessageHandler
	ToolHandler       *DynamicToolHandler

	// IssueFetcher refreshes issue state between turns.
	// Returns the updated issues or error.
	IssueFetcher func(ids []string) ([]*linear.Issue, error)

	// ActiveStates is the set of active state names (lowercased).
	ActiveStates map[string]struct{}
}

// ContinuationPrompt generates the guidance prompt for continuation turns.
func ContinuationPrompt(turnNumber, maxTurns int) string {
	return fmt.Sprintf(`Continuation guidance:

- The previous agent turn completed normally, but the Linear issue is still in an active state.
- This is continuation turn #%d of %d for the current agent run.
- Resume from the current workspace and workpad state instead of restarting from scratch.
- The original task instructions and prior turn context are already present in this thread, so do not restate them before acting.
- Focus on the remaining ticket work and do not end the turn while the issue stays active unless you are truly blocked.
`, turnNumber, maxTurns)
}

// RunResult holds the outcome of a complete agent run (potentially multiple turns).
type RunResult struct {
	TurnCount  int
	LastResult *TurnResult
	Error      error
}

// Run executes an agent run for a single issue.
// It creates a session and runs up to MaxTurns turns, checking issue state between turns.
func Run(ctx context.Context, cfg RunnerConfig, issue *linear.Issue, initialPrompt string, attempt *int) (*RunResult, error) {
	logger := slog.With("issue_id", issue.ID, "issue_identifier", issue.Identifier)
	logger.Info("Starting agent run")

	// Start a new session
	session, err := cfg.Backend.StartSession(ctx, SessionOpts{
		WorkspacePath:  cfg.WorkspacePath,
		ApprovalPolicy: cfg.ApprovalPolicy,
		Sandbox:        cfg.Sandbox,
		DynamicTools:   cfg.DynamicTools,
		OnMessage:      cfg.OnMessage,
		ToolHandler:    cfg.ToolHandler,
	})
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	defer func() {
		if cerr := session.Close(); cerr != nil {
			logger.Warn("Failed to close session", "error", cerr)
		}
	}()

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}

	currentIssue := issue
	var lastResult *TurnResult

	for turn := 1; turn <= maxTurns; turn++ {
		// Build prompt
		var prompt string
		if turn == 1 {
			prompt = initialPrompt
		} else {
			prompt = ContinuationPrompt(turn, maxTurns)
		}

		// Run the turn
		title := fmt.Sprintf("%s: %s", currentIssue.Identifier, currentIssue.Title)
		result, err := session.RunTurn(ctx, prompt, TurnOpts{
			TurnSandboxPolicy: cfg.TurnSandboxPolicy,
			Title:             title,
		})
		if err != nil {
			return &RunResult{TurnCount: turn, LastResult: lastResult, Error: err}, err
		}
		lastResult = result

		logger.Info("Completed agent turn",
			"session_id", result.SessionID,
			"turn", turn,
			"max_turns", maxTurns,
			"status", result.Status.String(),
		)

		// If the turn didn't complete successfully, stop
		if result.Status != TurnCompleted {
			return &RunResult{
				TurnCount:  turn,
				LastResult: result,
				Error:      fmt.Errorf("turn %s: %s", result.Status.String(), result.Error),
			}, fmt.Errorf("turn %s: %s", result.Status.String(), result.Error)
		}

		// Check if we should continue
		if turn >= maxTurns {
			logger.Info("Reached max turns with issue still active; returning control to orchestrator")
			break
		}

		shouldContinue, refreshed, err := checkIssueContinuation(cfg, currentIssue)
		if err != nil {
			return &RunResult{TurnCount: turn, LastResult: lastResult, Error: err}, err
		}
		if !shouldContinue {
			logger.Info("Issue no longer active, ending agent run")
			break
		}
		currentIssue = refreshed
		logger.Info("Continuing agent run after normal turn completion",
			"turn", turn,
			"max_turns", maxTurns,
		)
	}

	return &RunResult{TurnCount: maxTurns, LastResult: lastResult}, nil
}

// checkIssueContinuation checks whether the issue is still in an active state.
func checkIssueContinuation(cfg RunnerConfig, issue *linear.Issue) (bool, *linear.Issue, error) {
	if cfg.IssueFetcher == nil || issue.ID == "" {
		return false, issue, nil
	}

	issues, err := cfg.IssueFetcher([]string{issue.ID})
	if err != nil {
		return false, nil, fmt.Errorf("issue state refresh failed: %w", err)
	}

	if len(issues) == 0 {
		return false, issue, nil
	}

	refreshed := issues[0]
	if isActiveState(refreshed.State, cfg.ActiveStates) {
		return true, refreshed, nil
	}
	return false, refreshed, nil
}

func isActiveState(state string, activeStates map[string]struct{}) bool {
	if activeStates == nil {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(state))
	_, ok := activeStates[normalized]
	return ok
}
