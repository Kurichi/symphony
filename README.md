# Symphony

Symphony turns project work into isolated, autonomous implementation runs, allowing teams to manage
work instead of supervising coding agents.

[![Symphony demo video preview](.github/media/symphony-demo-poster.jpg)](.github/media/symphony-demo.mp4)

_In this [demo video](.github/media/symphony-demo.mp4), Symphony monitors a Linear board for work and spawns agents to handle the tasks. The agents complete the tasks and provide proof of work: CI status, PR review feedback, complexity analysis, and walkthrough videos. When accepted, the agents land the PR safely. Engineers do not need to supervise Codex; they can manage the work at a higher level._

> [!WARNING]
> Symphony is a low-key engineering preview for testing in trusted environments.

## Running Symphony

### Requirements

Symphony works best in codebases that have adopted
[harness engineering](https://openai.com/index/harness-engineering/). Symphony is the next step --
moving from managing coding agents to managing work that needs to get done.

### Option 1. Make your own

Tell your favorite coding agent to build Symphony in a programming language of your choice:

> Implement Symphony according to the following spec:
> https://github.com/openai/symphony/blob/main/SPEC.md

### Option 2. Use our experimental reference implementation

Check out [elixir/README.md](elixir/README.md) for instructions on how to set up your environment
and run the Elixir-based Symphony implementation. You can also ask your favorite coding agent to
help with the setup:

> Set up Symphony for my repository based on
> https://github.com/openai/symphony/blob/main/elixir/README.md

### Option 3. Use the Go implementation

A full Go reimplementation with pluggable agent backend support (Codex + Claude Code).

#### Prerequisites

- Go 1.22+
- A [Linear](https://linear.app) account with an API key
- [Codex](https://github.com/openai/codex) or [Claude Code](https://claude.com/claude-code) installed

#### Quick Start

1. **Create a `WORKFLOW.md`** in your project root:

```markdown
---
tracker:
  kind: linear
  api_key: $LINEAR_API_KEY
  project_slug: your-project-slug
polling:
  interval_ms: 30000
workspace:
  root: ~/symphony_workspaces
agent:
  backend: codex
  max_concurrent_agents: 5
  max_turns: 20
hooks:
  after_create: |
    git clone git@github.com:your-org/your-repo.git .
  before_run: |
    git fetch origin && git checkout main && git pull
---
You are working on a Linear issue.

Identifier: {{ issue.identifier }}
Title: {{ issue.title }}

Body:
{% if issue.description %}
{{ issue.description }}
{% else %}
No description provided.
{% endif %}
```

See [elixir/WORKFLOW.md](elixir/WORKFLOW.md) for a full-featured example with status-based routing, PR feedback sweeps, and rework handling.

2. **Set environment variables:**

```bash
export LINEAR_API_KEY="lin_api_xxxxx"
# Optional: only process issues assigned to the authenticated user
export LINEAR_ASSIGNEE="me"
```

3. **Build and run:**

```bash
# Build
go build -o bin/symphony ./cmd/symphony

# Run (reads WORKFLOW.md from current directory)
./bin/symphony

# Or specify a workflow path
./bin/symphony /path/to/WORKFLOW.md
```

#### CLI Options

| Flag | Description | Default |
|---|---|---|
| `--workflow` | Path to WORKFLOW.md | `./WORKFLOW.md` |
| `--port` | HTTP dashboard port (`-1` = use config, `0` = ephemeral) | `-1` |
| `--log` | Log file path (enables file logging with rotation) | _(none)_ |
| `--no-dashboard` | Disable terminal dashboard | `false` |

#### Subcommands

| Command | Description |
|---|---|
| `symphony` | Run the orchestrator (default) |
| `symphony mcp-tools` | Run MCP stdio server mode (used by Claude Code for `linear_graphql` tool) |

#### Agent Backends

Set `agent.backend` in your WORKFLOW.md front matter:

**Codex** (`agent.backend: codex`, default):
- Communicates via JSON-RPC 2.0 over stdio
- Long-lived process with multi-turn support within a session
- Configurable approval policy and sandbox settings under `codex:` section

**Claude Code** (`agent.backend: claude-code`):
- Spawns `claude` CLI per turn with `--output-format stream-json`
- Multi-turn via `--resume <session-id>`
- `linear_graphql` tool exposed via MCP (`symphony mcp-tools` subprocess)
- Configurable under `claude_code:` section:

```yaml
agent:
  backend: claude-code
claude_code:
  command: claude
  allowed_tools:
    - Bash
    - Read
    - Edit
    - Write
    - Glob
    - Grep
```

#### Observability

- **Terminal dashboard**: Live lipgloss-styled view of running agents, retry queue, and token usage (enabled by default)
- **HTTP dashboard**: Set `server.port` in WORKFLOW.md or use `--port` flag; browse to `http://127.0.0.1:<port>/`
- **REST API**: `GET /api/status` (snapshot), `POST /api/refresh` (trigger immediate poll), `GET /health`
- **Structured logging**: JSON via slog, optional file rotation with `--log`

#### Architecture

```
cmd/symphony/main.go          CLI entrypoint + mcp-tools subcommand
internal/
├── config/                    Typed config with $VAR resolution, dynamic reload
├── workflow/                  WORKFLOW.md parser + fsnotify file watcher
├── tracker/                   Tracker interface + memory implementation
├── linear/                    GraphQL client, pagination, assignee routing
├── orchestrator/              Core state machine (dispatch, reconcile, retry)
├── agent/                     Backend interface, Runner, DynamicTool handler
├── codex/                     Codex JSON-RPC backend
├── claudecode/                Claude Code CLI backend
├── mcpserver/                 MCP stdio server (linear_graphql tool)
├── workspace/                 Per-issue workspace manager + path safety
├── prompt/                    Liquid template rendering
├── server/                    Echo HTTP server + embedded dashboard
├── dashboard/                 Lipgloss terminal UI
└── logging/                   slog + lumberjack rotation
```

---

## License

This project is licensed under the [Apache License 2.0](LICENSE).
