package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Kurichi/symphony/internal/agent"
	"github.com/Kurichi/symphony/internal/claudecode"
	"github.com/Kurichi/symphony/internal/codex"
	"github.com/Kurichi/symphony/internal/config"
	cfgpkg "github.com/Kurichi/symphony/internal/config"
	"github.com/Kurichi/symphony/internal/dashboard"
	"github.com/Kurichi/symphony/internal/linear"
	"github.com/Kurichi/symphony/internal/logging"
	"github.com/Kurichi/symphony/internal/mcpserver"
	"github.com/Kurichi/symphony/internal/orchestrator"
	"github.com/Kurichi/symphony/internal/prompt"
	"github.com/Kurichi/symphony/internal/server"
	"github.com/Kurichi/symphony/internal/tracker"
	"github.com/Kurichi/symphony/internal/workflow"
	"github.com/Kurichi/symphony/internal/workspace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "symphony: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Check for mcp-tools subcommand
	if len(os.Args) > 1 && os.Args[1] == "mcp-tools" {
		return runMCPTools()
	}

	// Parse flags
	var (
		workflowPath string
		portOverride int
		logFile      string
		noDashboard  bool
	)

	flag.StringVar(&workflowPath, "workflow", "", "Path to WORKFLOW.md (default: ./WORKFLOW.md)")
	flag.IntVar(&portOverride, "port", -1, "HTTP server port (-1 = use config, 0 = ephemeral)")
	flag.StringVar(&logFile, "log", "", "Log file path (enables file logging)")
	flag.BoolVar(&noDashboard, "no-dashboard", false, "Disable terminal dashboard")
	flag.Parse()

	// If workflow path given as positional argument
	if flag.NArg() > 0 && workflowPath == "" {
		workflowPath = flag.Arg(0)
	}
	if workflowPath == "" {
		workflowPath = "WORKFLOW.md"
	}
	workflowPath, _ = filepath.Abs(workflowPath)

	// Setup logging
	logCfg := logging.Config{JSON: true, FilePath: logFile}
	closer, err := logging.Setup(logCfg)
	if err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	slog.Info("Symphony starting",
		"workflow", workflowPath,
		"pid", os.Getpid(),
	)

	// Load and watch workflow
	store, err := workflow.NewStore(workflowPath)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}
	defer store.Close()

	// Create config
	cfg := config.New(func() (*workflow.LoadedWorkflow, error) {
		return store.Current()
	})

	// Validate config at startup
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	// Create tracker
	var trk tracker.Tracker
	switch cfg.TrackerKind() {
	case "linear":
		trk = linear.NewClient(linear.ClientConfig{
			Endpoint:       cfg.LinearEndpoint(),
			APIToken:       cfg.LinearAPIToken(),
			ProjectSlug:    cfg.LinearProjectSlug(),
			Assignee:       cfg.LinearAssignee(),
			ActiveStates:   cfg.LinearActiveStates(),
			TerminalStates: cfg.LinearTerminalStates(),
		})
	case "memory":
		trk = tracker.NewMemoryTracker(nil)
	default:
		return fmt.Errorf("unsupported tracker kind: %q", cfg.TrackerKind())
	}

	// Create workspace manager
	wsMgr := workspace.NewManager(cfg.WorkspaceRoot(), workspace.HooksConfig{
		AfterCreate:  cfg.WorkspaceHooks().AfterCreate,
		BeforeRun:    cfg.WorkspaceHooks().BeforeRun,
		AfterRun:     cfg.WorkspaceHooks().AfterRun,
		BeforeRemove: cfg.WorkspaceHooks().BeforeRemove,
		TimeoutMs:    cfg.WorkspaceHooks().TimeoutMs,
	})

	// Create agent backend
	var agentBackend agent.Backend
	backendType := cfg.AgentBackend()
	switch backendType {
	case "claude-code":
		agentBackend = claudecode.NewBackend(claudecode.BackendOpts{
			Command:      cfg.ClaudeCodeCommand(),
			AllowedTools: cfg.ClaudeCodeAllowedTools(),
			WorkflowPath: workflowPath,
		})
	default: // "codex" or unset
		agentBackend = codex.NewBackend(codex.BackendOpts{
			Command:        cfg.CodexCommand(),
			TurnTimeoutMs:  cfg.CodexTurnTimeoutMs(),
			ReadTimeoutMs:  cfg.CodexReadTimeoutMs(),
			StallTimeoutMs: cfg.CodexStallTimeoutMs(),
		})
	}

	// Dynamic tool handler
	toolHandler := agent.NewDynamicToolHandler(trk.ExecuteGraphQL)

	// Build active states set for agent runner
	activeStatesSet := make(map[string]struct{})
	for _, s := range cfg.LinearActiveStates() {
		activeStatesSet[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}

	// State change notification (for dashboard)
	var stateChangeNotify func()

	// Create orchestrator
	orch := orchestrator.New(orchestrator.Deps{
		PollIntervalMs:        cfg.PollIntervalMs,
		MaxConcurrentAgents:   cfg.MaxConcurrentAgents,
		MaxConcurrentForState: cfg.MaxConcurrentAgentsForState,
		MaxRetryBackoffMs:     cfg.MaxRetryBackoffMs,
		ActiveStates:          cfg.LinearActiveStates,
		TerminalStates:        cfg.LinearTerminalStates,
		StallTimeoutMs:        cfg.CodexStallTimeoutMs,
		Validate:              cfg.Validate,
		WorkflowPrompt: func() string {
			return cfg.WorkflowPrompt()
		},
		WorkspaceRoot: func() string {
			return cfg.WorkspaceRoot()
		},
		FetchCandidateIssues:  trk.FetchCandidateIssues,
		FetchIssueStatesByIDs: trk.FetchIssueStatesByIDs,
		FetchIssuesByStates:   trk.FetchIssuesByStates,
		RunAgent: func(ctx context.Context, issue *linear.Issue, attempt *int, onMessage agent.MessageHandler) error {
			// Create workspace
			result, err := wsMgr.CreateForIssue(issue.Identifier)
			if err != nil {
				return fmt.Errorf("create workspace: %w", err)
			}

			// Run before_run hook
			if err := wsMgr.RunBeforeRunHook(result.Path, issue.Identifier); err != nil {
				return fmt.Errorf("before_run hook: %w", err)
			}
			defer wsMgr.RunAfterRunHook(result.Path, issue.Identifier)

			// Build prompt
			promptText, err := prompt.BuildPrompt(issue, cfg.WorkflowPrompt())
			if err != nil {
				return fmt.Errorf("build prompt: %w", err)
			}

			// Run agent
			runCfg := agent.RunnerConfig{
				MaxTurns:          cfg.AgentMaxTurns(),
				Backend:           agentBackend,
				WorkspacePath:     result.Path,
				ApprovalPolicy:    cfg.CodexApprovalPolicy(),
				Sandbox:           cfg.CodexThreadSandbox(),
				TurnSandboxPolicy: cfg.CodexTurnSandboxPolicy(result.Path),
				DynamicTools:      []agent.ToolSpec{agent.LinearGraphQLToolSpec()},
				OnMessage:         onMessage,
				ToolHandler:       toolHandler,
				IssueFetcher:      trk.FetchIssueStatesByIDs,
				ActiveStates:      activeStatesSet,
			}
			_, err = agent.Run(ctx, runCfg, issue, promptText, attempt)
			return err
		},
		RemoveIssueWorkspace: func(identifier string) error {
			return wsMgr.RemoveIssueWorkspace(identifier)
		},
		OnStateChange: func() {
			if stateChangeNotify != nil {
				stateChangeNotify()
			}
		},
	})

	// Setup context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start HTTP server if configured
	serverPort := resolveServerPort(cfg, portOverride)
	if serverPort != nil {
		srv := server.New(server.Config{
			Host: cfg.ServerHost(),
			Port: *serverPort,
		}, orch)
		addr, err := srv.Start()
		if err != nil {
			slog.Error("Failed to start HTTP server", "error", err)
		} else {
			slog.Info("HTTP server started", "address", addr)
			defer srv.Shutdown(context.Background())
		}
	}

	// Start terminal dashboard
	if !noDashboard && cfg.ObservabilityEnabled() {
		dash := dashboard.New(dashboard.Config{
			RefreshMs:    cfg.ObservabilityRefreshMs(),
			Orchestrator: orch,
		})
		stateChangeNotify = func() { dash.NotifyUpdate() }
		go func() {
			if err := dash.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("Dashboard error", "error", err)
			}
		}()
	}

	// Run orchestrator (blocks until shutdown)
	slog.Info("Orchestrator starting")
	if err := orch.Run(ctx); err != nil && ctx.Err() == nil {
		return fmt.Errorf("orchestrator: %w", err)
	}

	slog.Info("Symphony shutdown complete")
	return nil
}

func resolveServerPort(cfg *cfgpkg.Config, portOverride int) *int {
	if portOverride >= 0 {
		return &portOverride
	}
	return cfg.ServerPort()
}

// runMCPTools runs the MCP stdio server mode for Claude Code integration.
func runMCPTools() error {
	// In mcp-tools mode, we read WORKFLOW.md for Linear API config
	workflowPath := "WORKFLOW.md"
	if len(os.Args) > 2 {
		workflowPath = os.Args[2]
	}
	workflowPath, _ = filepath.Abs(workflowPath)

	wf, err := workflow.LoadFile(workflowPath)
	if err != nil {
		return fmt.Errorf("load workflow for mcp-tools: %w", err)
	}

	cfg := config.New(func() (*workflow.LoadedWorkflow, error) {
		return wf, nil
	})

	client := linear.NewClient(linear.ClientConfig{
		Endpoint:       cfg.LinearEndpoint(),
		APIToken:       cfg.LinearAPIToken(),
		ProjectSlug:    cfg.LinearProjectSlug(),
		ActiveStates:   cfg.LinearActiveStates(),
		TerminalStates: cfg.LinearTerminalStates(),
	})

	srv := mcpserver.NewServer(client.ExecuteGraphQL)
	return srv.Run(context.Background())
}
