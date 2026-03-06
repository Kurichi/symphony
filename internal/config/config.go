package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Kurichi/symphony/internal/workflow"
)

// HooksConfig holds workspace lifecycle hook commands.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// Config provides typed, default-aware access to workflow configuration values.
type Config struct {
	workflow func() (*workflow.LoadedWorkflow, error)
}

// New creates a Config backed by the given workflow supplier function.
func New(workflowFn func() (*workflow.LoadedWorkflow, error)) *Config {
	return &Config{workflow: workflowFn}
}

// --- helpers ----------------------------------------------------------------

// cfg returns the normalised config map (string keys, recursive).
func (c *Config) cfg() map[string]any {
	wf, err := c.workflow()
	if err != nil || wf == nil {
		return map[string]any{}
	}
	return normalizeKeys(wf.Config)
}

// section returns a nested map[string]any for a top-level key.
func (c *Config) section(key string) map[string]any {
	m, _ := c.cfg()[key].(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// --- tracker ----------------------------------------------------------------

func (c *Config) TrackerKind() string {
	v := stringVal(c.section("tracker"), "kind")
	if v == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func (c *Config) LinearEndpoint() string {
	return stringValOr(c.section("tracker"), "endpoint", DefaultLinearEndpoint)
}

func (c *Config) LinearAPIToken() string {
	v := stringVal(c.section("tracker"), "api_key")
	return resolveEnvValue(v, os.Getenv("LINEAR_API_KEY"))
}

func (c *Config) LinearProjectSlug() string {
	return stringVal(c.section("tracker"), "project_slug")
}

func (c *Config) LinearAssignee() string {
	v := stringVal(c.section("tracker"), "assignee")
	return resolveEnvValue(v, os.Getenv("LINEAR_ASSIGNEE"))
}

func (c *Config) LinearActiveStates() []string {
	if v := csvOrList(c.section("tracker"), "active_states"); len(v) > 0 {
		return normalizeStates(v)
	}
	return DefaultActiveStates
}

func (c *Config) LinearTerminalStates() []string {
	if v := csvOrList(c.section("tracker"), "terminal_states"); len(v) > 0 {
		return normalizeStates(v)
	}
	return DefaultTerminalStates
}

// --- polling -----------------------------------------------------------------

func (c *Config) PollIntervalMs() int {
	return intValOr(c.section("polling"), "interval_ms", DefaultPollIntervalMs)
}

// --- workspace ---------------------------------------------------------------

func (c *Config) WorkspaceRoot() string {
	v := stringVal(c.section("workspace"), "root")
	return resolvePathValue(v, DefaultWorkspaceRoot())
}

func (c *Config) WorkspaceHooks() HooksConfig {
	h := c.section("hooks")
	return HooksConfig{
		AfterCreate:  stringVal(h, "after_create"),
		BeforeRun:    stringVal(h, "before_run"),
		AfterRun:     stringVal(h, "after_run"),
		BeforeRemove: stringVal(h, "before_remove"),
		TimeoutMs:    intValOr(h, "timeout_ms", DefaultHookTimeoutMs),
	}
}

func (c *Config) HookTimeoutMs() int {
	return intValOr(c.section("hooks"), "timeout_ms", DefaultHookTimeoutMs)
}

// --- agent -------------------------------------------------------------------

func (c *Config) MaxConcurrentAgents() int {
	return intValOr(c.section("agent"), "max_concurrent_agents", DefaultMaxConcurrentAgents)
}

func (c *Config) MaxRetryBackoffMs() int {
	return intValOr(c.section("agent"), "max_retry_backoff_ms", DefaultMaxRetryBackoffMs)
}

func (c *Config) AgentMaxTurns() int {
	return intValOr(c.section("agent"), "max_turns", DefaultAgentMaxTurns)
}

func (c *Config) MaxConcurrentAgentsForState(state string) int {
	s := c.section("agent")
	byState, _ := s["max_concurrent_agents_by_state"].(map[string]any)
	norm := strings.ToLower(strings.TrimSpace(state))
	if byState != nil {
		if v, ok := byState[norm]; ok {
			if n := toInt(v); n > 0 {
				return n
			}
		}
	}
	return c.MaxConcurrentAgents()
}

// --- codex -------------------------------------------------------------------

func (c *Config) CodexCommand() string {
	return stringValOr(c.section("codex"), "command", DefaultCodexCommand)
}

func (c *Config) CodexTurnTimeoutMs() int {
	return intValOr(c.section("codex"), "turn_timeout_ms", DefaultCodexTurnTimeoutMs)
}

func (c *Config) CodexReadTimeoutMs() int {
	return intValOr(c.section("codex"), "read_timeout_ms", DefaultCodexReadTimeoutMs)
}

func (c *Config) CodexStallTimeoutMs() int {
	v := intValOr(c.section("codex"), "stall_timeout_ms", DefaultCodexStallTimeoutMs)
	if v < 0 {
		return 0
	}
	return v
}

func (c *Config) CodexApprovalPolicy() map[string]any {
	v, _ := c.section("codex")["approval_policy"]
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return DefaultCodexApprovalPolicy()
}

func (c *Config) CodexThreadSandbox() string {
	return stringValOr(c.section("codex"), "thread_sandbox", DefaultCodexThreadSandbox)
}

func (c *Config) CodexTurnSandboxPolicy(workspace string) map[string]any {
	v, _ := c.section("codex")["turn_sandbox_policy"]
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return c.defaultTurnSandboxPolicy(workspace)
}

func (c *Config) defaultTurnSandboxPolicy(workspace string) map[string]any {
	root := strings.TrimSpace(workspace)
	if root == "" {
		root = c.WorkspaceRoot()
	}
	root, _ = filepath.Abs(root)

	return map[string]any{
		"type":                 "workspaceWrite",
		"writableRoots":       []string{root},
		"readOnlyAccess":      map[string]any{"type": "fullAccess"},
		"networkAccess":       false,
		"excludeTmpdirEnvVar": false,
		"excludeSlashTmp":     false,
	}
}

// --- agent backend -----------------------------------------------------------

func (c *Config) AgentBackend() string {
	v := stringVal(c.section("agent"), "backend")
	if v == "" {
		return "codex"
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func (c *Config) ClaudeCodeCommand() string {
	return stringValOr(c.section("claude_code"), "command", DefaultClaudeCodeCommand)
}

func (c *Config) ClaudeCodeAllowedTools() []string {
	v := csvOrList(c.section("claude_code"), "allowed_tools")
	if len(v) > 0 {
		return v
	}
	return DefaultClaudeCodeAllowedTools()
}

// --- observability -----------------------------------------------------------

func (c *Config) ObservabilityEnabled() bool {
	return boolValOr(c.section("observability"), "dashboard_enabled", DefaultObservabilityEnabled)
}

func (c *Config) ObservabilityRefreshMs() int {
	return intValOr(c.section("observability"), "refresh_ms", DefaultObservabilityRefresh)
}

// --- server ------------------------------------------------------------------

func (c *Config) ServerPort() *int {
	v, ok := c.section("server")["port"]
	if !ok {
		return nil
	}
	if n := toInt(v); n >= 0 {
		return &n
	}
	return nil
}

func (c *Config) ServerHost() string {
	return stringValOr(c.section("server"), "host", DefaultServerHost)
}

// --- prompt ------------------------------------------------------------------

func (c *Config) WorkflowPrompt() string {
	wf, err := c.workflow()
	if err != nil || wf == nil {
		return DefaultPromptTemplate
	}
	if strings.TrimSpace(wf.PromptTemplate) == "" {
		return DefaultPromptTemplate
	}
	return wf.PromptTemplate
}

// --- validation --------------------------------------------------------------

func (c *Config) Validate() error {
	kind := c.TrackerKind()
	switch kind {
	case "linear", "memory":
		// ok
	case "":
		return fmt.Errorf("config: missing tracker.kind")
	default:
		return fmt.Errorf("config: unsupported tracker.kind %q", kind)
	}

	if kind == "linear" {
		if c.LinearAPIToken() == "" {
			return fmt.Errorf("config: missing linear API token (tracker.api_key or $LINEAR_API_KEY)")
		}
		if c.LinearProjectSlug() == "" {
			return fmt.Errorf("config: missing tracker.project_slug")
		}
	}

	if strings.TrimSpace(c.CodexCommand()) == "" {
		return fmt.Errorf("config: missing codex.command")
	}

	return nil
}

// ---------------------------------------------------------------------------
// Value extraction helpers
// ---------------------------------------------------------------------------

// resolveEnvValue resolves a $VAR reference. If the raw value starts with "$",
// the corresponding environment variable is looked up. Falls back to fallback
// if the env var is unset.
func resolveEnvValue(raw, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if fallback == "" {
			return ""
		}
		return strings.TrimSpace(fallback)
	}
	if strings.HasPrefix(raw, "$") {
		envName := raw[1:]
		if !isEnvVarName(envName) {
			return raw
		}
		v := os.Getenv(envName)
		if v == "" {
			if fallback == "" {
				return ""
			}
			return strings.TrimSpace(fallback)
		}
		return strings.TrimSpace(v)
	}
	return raw
}

// resolvePathValue resolves a path that may contain ~ or $VAR.
func resolvePathValue(raw, defaultVal string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultVal
	}

	// Resolve $VAR reference
	if strings.HasPrefix(raw, "$") {
		envName := raw[1:]
		if isEnvVarName(envName) {
			v := os.Getenv(envName)
			if v == "" {
				return defaultVal
			}
			raw = v
		}
	}

	// Expand ~ (e.g., "~/workspaces" → "/home/user/workspaces")
	if strings.HasPrefix(raw, "~/") || raw == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if raw == "~" {
				raw = home
			} else {
				raw = filepath.Join(home, raw[2:]) // skip "~/"
			}
		}
	}

	if raw == "" {
		return defaultVal
	}

	// If it looks like a path, expand it
	if strings.Contains(raw, "/") || strings.Contains(raw, string(filepath.Separator)) {
		abs, err := filepath.Abs(raw)
		if err == nil {
			return abs
		}
	}

	return raw
}

func isEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if i == 0 {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_') {
				return false
			}
		} else {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				return false
			}
		}
	}
	return true
}

func stringVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch tv := v.(type) {
	case string:
		return strings.TrimSpace(tv)
	case bool:
		return strconv.FormatBool(tv)
	case int:
		return strconv.Itoa(tv)
	case float64:
		return strconv.FormatFloat(tv, 'f', -1, 64)
	default:
		return ""
	}
}

func stringValOr(m map[string]any, key, def string) string {
	v := stringVal(m, key)
	if v == "" {
		return def
	}
	return v
}

func toInt(v any) int {
	switch tv := v.(type) {
	case int:
		return tv
	case float64:
		return int(tv)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(tv))
		return n
	}
	return 0
}

func intValOr(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	n := toInt(v)
	if n == 0 {
		// Distinguish explicit 0 from parse failure: if the original wasn't zero-ish, use default
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "0" {
			return 0
		}
		if _, ok := v.(int); ok {
			return 0
		}
		if f, ok := v.(float64); ok && f == 0 {
			return 0
		}
		return def
	}
	return n
}

func boolValOr(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch tv := v.(type) {
	case bool:
		return tv
	case string:
		switch strings.ToLower(strings.TrimSpace(tv)) {
		case "true":
			return true
		case "false":
			return false
		}
	}
	return def
}

// csvOrList extracts a []string from a YAML value that can be either a list
// or a comma-separated string.
func csvOrList(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch tv := v.(type) {
	case []any:
		var out []string
		for _, elem := range tv {
			if s := fmt.Sprint(elem); strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		parts := strings.Split(tv, ",")
		var out []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

func normalizeStates(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		n := strings.TrimSpace(s)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// normalizeKeys converts top-level and second-level map keys to lowercase
// for config section matching, but preserves deeper nested key casing
// (e.g., codex turn_sandbox_policy values are passed through to Codex as-is).
func normalizeKeys(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = normalizeValueShallow(v)
	}
	return out
}

// normalizeValueShallow normalizes one level of nesting (section keys) but
// preserves values at deeper levels to avoid breaking pass-through config.
func normalizeValueShallow(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			out[strings.ToLower(k)] = ensureStringKeys(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			out[strings.ToLower(fmt.Sprint(k))] = ensureStringKeys(val)
		}
		return out
	default:
		return v
	}
}

// ensureStringKeys converts map[any]any to map[string]any without altering key casing.
func ensureStringKeys(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			out[k] = ensureStringKeys(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			out[fmt.Sprint(k)] = ensureStringKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(tv))
		for i, elem := range tv {
			out[i] = ensureStringKeys(elem)
		}
		return out
	default:
		return v
	}
}
