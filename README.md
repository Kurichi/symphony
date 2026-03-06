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

### Option 3. Go 実装を使う

Codex と Claude Code の両方に対応したプラガブルなエージェントバックエンドを備えた Go による完全再実装です。

#### 前提条件

- Go 1.22+
- [Linear](https://linear.app) アカウントと API キー
- [Codex](https://github.com/openai/codex) または [Claude Code](https://claude.com/claude-code) がインストール済みであること

#### クイックスタート

1. **プロジェクトルートに `WORKFLOW.md` を作成:**

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

ステータスベースのルーティング、PR フィードバックスイープ、リワーク処理を含む完全な設定例は [elixir/WORKFLOW.md](elixir/WORKFLOW.md) を参照してください。

2. **環境変数を設定:**

```bash
export LINEAR_API_KEY="lin_api_xxxxx"
# オプション: 認証ユーザーにアサインされたイシューのみ処理
export LINEAR_ASSIGNEE="me"
```

3. **ビルドと実行:**

```bash
# ビルド
go build -o bin/symphony ./cmd/symphony

# 実行 (カレントディレクトリの WORKFLOW.md を読み込み)
./bin/symphony

# ワークフローファイルのパスを指定する場合
./bin/symphony /path/to/WORKFLOW.md
```

#### CLI オプション

| フラグ | 説明 | デフォルト |
|---|---|---|
| `--workflow` | WORKFLOW.md のパス | `./WORKFLOW.md` |
| `--port` | HTTP ダッシュボードのポート (`-1` = 設定値を使用, `0` = エフェメラル) | `-1` |
| `--log` | ログファイルパス (ローテーション付きファイルログを有効化) | _(なし)_ |
| `--no-dashboard` | ターミナルダッシュボードを無効化 | `false` |

#### サブコマンド

| コマンド | 説明 |
|---|---|
| `symphony` | オーケストレーターを実行 (デフォルト) |
| `symphony mcp-tools` | MCP stdio サーバーモードで実行 (Claude Code から `linear_graphql` ツールを利用する際に使用) |

#### エージェントバックエンド

WORKFLOW.md のフロントマターで `agent.backend` を設定します:

**Codex** (`agent.backend: codex`, デフォルト):
- JSON-RPC 2.0 over stdio で通信
- セッション内で複数ターンをサポートする長寿命プロセス
- `codex:` セクションで承認ポリシーとサンドボックスを設定可能

**Claude Code** (`agent.backend: claude-code`):
- ターンごとに `claude` CLI を `--output-format stream-json` で起動
- `--resume <session-id>` によるマルチターン対応
- MCP 経由で `linear_graphql` ツールを公開 (`symphony mcp-tools` サブプロセス)
- `claude_code:` セクションで設定可能:

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

#### 可観測性

- **ターミナルダッシュボード**: 実行中のエージェント、リトライキュー、トークン使用量をリアルタイム表示 (lipgloss スタイリング、デフォルト有効)
- **HTTP ダッシュボード**: WORKFLOW.md の `server.port` または `--port` フラグで設定。`http://127.0.0.1:<port>/` にアクセス
- **REST API**: `GET /api/status` (スナップショット), `POST /api/refresh` (即時ポーリング実行), `GET /health`
- **構造化ログ**: slog による JSON 出力、`--log` でファイルローテーション対応

#### アーキテクチャ

```
cmd/symphony/main.go          CLI エントリポイント + mcp-tools サブコマンド
internal/
├── config/                    型付き設定、$VAR 解決、動的リロード
├── workflow/                  WORKFLOW.md パーサー + fsnotify ファイル監視
├── tracker/                   Tracker インターフェース + インメモリ実装
├── linear/                    GraphQL クライアント、ページネーション、アサイニールーティング
├── orchestrator/              コア状態マシン (ディスパッチ、リコンサイル、リトライ)
├── agent/                     Backend インターフェース、Runner、DynamicTool ハンドラ
├── codex/                     Codex JSON-RPC バックエンド
├── claudecode/                Claude Code CLI バックエンド
├── mcpserver/                 MCP stdio サーバー (linear_graphql ツール)
├── workspace/                 イシュー毎のワークスペース管理 + パス安全性
├── prompt/                    Liquid テンプレートレンダリング
├── server/                    Echo HTTP サーバー + 埋め込みダッシュボード
├── dashboard/                 lipgloss ターミナル UI
└── logging/                   slog + lumberjack ログローテーション
```

---

## License

This project is licensed under the [Apache License 2.0](LICENSE).
