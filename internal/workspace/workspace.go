package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// excludedEntries are tmp artifacts cleaned from existing workspaces.
var excludedEntries = []string{".elixir_ls", "tmp"}

// HooksConfig holds shell commands to run at workspace lifecycle points.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// CreateResult reports the workspace path and whether it was newly created.
type CreateResult struct {
	Path       string
	CreatedNow bool
}

// Manager creates and manages per-issue workspaces under a root directory.
type Manager struct {
	root  string
	hooks HooksConfig
}

// NewManager returns a workspace manager rooted at the given directory.
func NewManager(root string, hooks HooksConfig) *Manager {
	return &Manager{root: root, hooks: hooks}
}

// Root returns the configured workspace root path.
func (m *Manager) Root() string {
	return m.root
}

// CreateForIssue creates or reuses an isolated workspace directory for the
// given issue identifier. SPEC section 7.
func (m *Manager) CreateForIssue(identifier string) (*CreateResult, error) {
	safeID := SafeIdentifier(identifier)
	wsPath := filepath.Join(m.root, safeID)

	if err := m.validatePath(wsPath); err != nil {
		return nil, fmt.Errorf("workspace path validation: %w", err)
	}

	createdNow, err := m.ensureWorkspace(wsPath)
	if err != nil {
		return nil, fmt.Errorf("ensure workspace: %w", err)
	}

	if createdNow {
		if err := m.runHookIfSet(m.hooks.AfterCreate, wsPath, identifier, "after_create"); err != nil {
			return nil, fmt.Errorf("after_create hook: %w", err)
		}
	}

	return &CreateResult{Path: wsPath, CreatedNow: createdNow}, nil
}

// Remove deletes a workspace directory after validation.
func (m *Manager) Remove(workspacePath string) error {
	exists, err := pathExists(workspacePath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := m.validatePath(workspacePath); err != nil {
		return err
	}

	// Best-effort before_remove hook
	_ = m.runHookIfSet(m.hooks.BeforeRemove, workspacePath, filepath.Base(workspacePath), "before_remove")

	return os.RemoveAll(workspacePath)
}

// RemoveIssueWorkspace removes the workspace for a specific issue identifier.
func (m *Manager) RemoveIssueWorkspace(identifier string) error {
	safeID := SafeIdentifier(identifier)
	wsPath := filepath.Join(m.root, safeID)
	return m.Remove(wsPath)
}

// RunBeforeRunHook runs the before_run hook if configured.
func (m *Manager) RunBeforeRunHook(workspacePath string, identifier string) error {
	return m.runHookIfSet(m.hooks.BeforeRun, workspacePath, identifier, "before_run")
}

// RunAfterRunHook runs the after_run hook if configured. Failures are logged
// but not propagated (matching Elixir behaviour).
func (m *Manager) RunAfterRunHook(workspacePath string, identifier string) error {
	if err := m.runHookIfSet(m.hooks.AfterRun, workspacePath, identifier, "after_run"); err != nil {
		slog.Warn("after_run hook failed", "error", err, "workspace", workspacePath)
	}
	return nil
}

// SafeIdentifier replaces any character not in [A-Za-z0-9._-] with underscore.
func SafeIdentifier(identifier string) string {
	if identifier == "" {
		return "issue"
	}
	return unsafeChars.ReplaceAllString(identifier, "_")
}

// ensureWorkspace creates the directory or cleans an existing one.
func (m *Manager) ensureWorkspace(wsPath string) (bool, error) {
	info, err := os.Stat(wsPath)
	if err == nil {
		if info.IsDir() {
			cleanTmpArtifacts(wsPath)
			return false, nil
		}
		// Exists but not a directory -- remove and recreate.
		if err := os.RemoveAll(wsPath); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		return false, err
	}
	return true, nil
}

// cleanTmpArtifacts removes known temporary entries from an existing workspace.
func cleanTmpArtifacts(wsPath string) {
	for _, entry := range excludedEntries {
		_ = os.RemoveAll(filepath.Join(wsPath, entry))
	}
}

// validatePath ensures the workspace path is under the root and contains no
// symlink components that could escape.
func (m *Manager) validatePath(wsPath string) error {
	absRoot, err := filepath.Abs(m.root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	absWS, err := filepath.Abs(wsPath)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	if absWS == absRoot {
		return fmt.Errorf("workspace path equals root: %s", absWS)
	}

	rootPrefix := absRoot + string(filepath.Separator)
	if !strings.HasPrefix(absWS+string(filepath.Separator), rootPrefix) {
		return fmt.Errorf("workspace %q is outside root %q", absWS, absRoot)
	}

	// Walk path components from root to wsPath checking for symlinks.
	relPath, err := filepath.Rel(absRoot, absWS)
	if err != nil {
		return err
	}
	current := absRoot
	for _, segment := range strings.Split(relPath, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break // remaining path doesn't exist yet -- OK
		}
		if err != nil {
			return fmt.Errorf("lstat %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink detected at %q (root=%s)", current, absRoot)
		}
	}
	return nil
}

// runHookIfSet executes a shell hook command if it is non-empty.
func (m *Manager) runHookIfSet(command, wsPath, identifier, hookName string) error {
	if command == "" {
		return nil
	}

	timeoutMs := m.hooks.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 60_000
	}

	slog.Info("Running workspace hook",
		"hook", hookName,
		"identifier", identifier,
		"workspace", wsPath,
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = wsPath
	cmd.Env = append(os.Environ(),
		"SYMPHONY_WORKSPACE="+wsPath,
		"SYMPHONY_ISSUE_IDENTIFIER="+identifier,
	)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("hook %s timed out after %dms", hookName, timeoutMs)
	}
	if err != nil {
		out := sanitizeHookOutput(output)
		return fmt.Errorf("hook %s failed (exit %v): %s", hookName, err, out)
	}
	return nil
}

// sanitizeHookOutput truncates hook output for logging.
func sanitizeHookOutput(output []byte) string {
	const maxBytes = 2048
	if len(output) <= maxBytes {
		return string(output)
	}
	return string(output[:maxBytes]) + "... (truncated)"
}

func pathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
