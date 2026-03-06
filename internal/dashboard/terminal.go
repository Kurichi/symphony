package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Kurichi/symphony/internal/orchestrator"
)

// Config holds terminal dashboard configuration.
type Config struct {
	RefreshMs       int
	RenderInterval  time.Duration
	Orchestrator    *orchestrator.Orchestrator
}

// Dashboard renders orchestrator state in the terminal using lipgloss.
type Dashboard struct {
	cfg          Config
	updateCh     chan struct{}
}

// New creates a new terminal dashboard.
func New(cfg Config) *Dashboard {
	if cfg.RefreshMs <= 0 {
		cfg.RefreshMs = 1000
	}
	if cfg.RenderInterval <= 0 {
		cfg.RenderInterval = 16 * time.Millisecond
	}
	return &Dashboard{
		cfg:      cfg,
		updateCh: make(chan struct{}, 1),
	}
}

// NotifyUpdate signals that orchestrator state has changed.
func (d *Dashboard) NotifyUpdate() {
	select {
	case d.updateCh <- struct{}{}:
	default:
	}
}

// Run starts the dashboard render loop. Blocks until context is cancelled.
func (d *Dashboard) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(d.cfg.RefreshMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.render()
		case <-d.updateCh:
			d.render()
		}
	}
}

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#58a6ff")).
			MarginBottom(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#8b949e"))

	runningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#3fb950"))

	retryStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d29922"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#484f58"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#30363d")).
			Padding(0, 1)
)

func (d *Dashboard) render() {
	snapshot, err := d.cfg.Orchestrator.RequestSnapshot(5 * time.Second)
	if err != nil {
		slog.Debug("Dashboard snapshot failed", "error", err)
		return
	}

	var sb strings.Builder

	// Clear screen and move cursor to top
	sb.WriteString("\033[2J\033[H")

	// Title
	sb.WriteString(titleStyle.Render("Symphony Orchestrator"))
	sb.WriteString("\n\n")

	// Polling status
	pollStatus := "Idle"
	if snapshot.Polling.Checking {
		pollStatus = "Checking now..."
	} else if snapshot.Polling.NextPollInMs > 0 {
		pollStatus = fmt.Sprintf("Next poll in %ds", snapshot.Polling.NextPollInMs/1000)
	}
	sb.WriteString(headerStyle.Render("Polling: "))
	sb.WriteString(pollStatus)
	sb.WriteString(fmt.Sprintf(" (interval: %ds)", snapshot.Polling.PollIntervalMs/1000))
	sb.WriteString("\n\n")

	// Running agents
	sb.WriteString(headerStyle.Render(fmt.Sprintf("Running Agents (%d)", len(snapshot.Running))))
	sb.WriteString("\n")
	if len(snapshot.Running) == 0 {
		sb.WriteString(dimStyle.Render("  No active agents"))
		sb.WriteString("\n")
	} else {
		for _, r := range snapshot.Running {
			line := fmt.Sprintf("  %s %s  session=%s  tokens=%d  turns=%d  %ds",
				runningStyle.Render(r.Identifier),
				dimStyle.Render(r.State),
				dimStyle.Render(truncate(r.SessionID, 20)),
				r.TotalTokens,
				r.TurnCount,
				int(r.RuntimeSeconds),
			)
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// Retry queue
	sb.WriteString(headerStyle.Render(fmt.Sprintf("Retry Queue (%d)", len(snapshot.Retrying))))
	sb.WriteString("\n")
	if len(snapshot.Retrying) == 0 {
		sb.WriteString(dimStyle.Render("  No retries queued"))
		sb.WriteString("\n")
	} else {
		for _, r := range snapshot.Retrying {
			line := fmt.Sprintf("  %s  attempt=%s  due in %ds",
				retryStyle.Render(coalesce(r.Identifier, r.IssueID)),
				retryStyle.Render(fmt.Sprintf("#%d", r.Attempt)),
				r.DueInMs/1000,
			)
			if r.Error != "" {
				line += dimStyle.Render("  "+truncate(r.Error, 60))
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// Totals
	if snapshot.Totals != nil {
		sb.WriteString(headerStyle.Render("Totals"))
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("  Tokens: %d (in: %d, out: %d)  Runtime: %ds\n",
			snapshot.Totals.TotalTokens,
			snapshot.Totals.InputTokens,
			snapshot.Totals.OutputTokens,
			int(snapshot.Totals.SecondsRunning),
		))
	}

	fmt.Fprint(os.Stdout, sb.String())
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
