# OpenLinker CLI

Chinese documentation: [README.zh-CN.md](./README.zh-CN.md)

JSON-first CLI and reliable Runtime Worker for OpenLinker. It is intentionally
thin over `openlinker-go` and has two credential-isolated modes:

- stdout is always JSON;
- diagnostics and errors go to stderr;
- caller commands accept only an OpenLinker User Token;
- `agent serve` accepts only an Agent Token plus the selected provider's auth;
- command implementations are split by subcommand under `pkg/`.

`agent serve` runs the official SDK Runtime Worker directly, including
WebSocket/pull selection, durable delivery, cancellation, and token-only
registration. The Codex and Claude adapters reuse private provider sessions by
Core-owned conversation. `plugin serve` exposes the caller and Agent Mode
control surface as a local stdio MCP server for native plugins. Caller and
Runtime credentials are never interchangeable.

The CLI calls the public Core contract in either a self-hosted or Hosted
deployment. It does not call hosted service-listing, order, wallet, billing, or
marketplace-operation APIs.

## Status and installation

The CLI is pre-1.0 and follows the Core API contract. Pin a release and review
`CHANGELOG.md` before upgrading.

Download a Linux, macOS, or Windows archive and its adjacent `.sha256` file
from [GitHub Releases](https://github.com/OpenLinker-ai/openlinker-cli/releases),
then verify the checksum before placing `openlinker` on your `PATH`. Go users
can install a fixed release directly:

```bash
go install github.com/OpenLinker-ai/openlinker-cli/cmd/openlinker@v0.x.y
```

Replace `v0.x.y` with the release you have chosen.

## Configuration

```bash
export OPENLINKER_API_BASE=http://localhost:8080
export OPENLINKER_USER_TOKEN=ol_user_xxx
```

The CLI does not accept the retired `OPENLINKER_TOKEN`,
`OPENLINKER_RUNTIME_TOKEN`, `OPENLINKER_DEMO_JWT`, or `OPENLINKER_API_URL`
aliases. `--token` may be used to provide a User Token explicitly, but the
environment variable is safer for routine use because command-line arguments
may be retained in shell history or exposed in process listings.

Run identifiers may be injected by a surrounding environment for diagnostics:

```bash
export OPENLINKER_RUN_ID=33333333-3333-4333-8333-333333333333
export OPENLINKER_AGENT_ID=22222222-2222-4222-8222-222222222222
export OPENLINKER_TRACE_ID=44444444-4444-4444-8444-444444444444
```

These values are context only. They do not authorize runtime delegation.

### Runtime Agent configuration

The minimum required values for a foreground provider are:

```bash
export OPENLINKER_URL=https://openlinker.example
export OPENLINKER_AGENT_ID=22222222-2222-4222-8222-222222222222
export OPENLINKER_AGENT_TOKEN=ol_agent_xxx
export OPENLINKER_WORKSPACE=/absolute/minimal/workspace
export CODEX_API_KEY=... # Codex; use ANTHROPIC_API_KEY for Claude

openlinker agent serve --provider codex
```

`OPENLINKER_NODE_ID` is optional. The CLI generates it once and persists it in
the owner-only Agent state directory. Every direct secret also supports a
mutually exclusive `_FILE` form. Provider login state may replace the API key
for a trusted local installation; official production images require provider
API-key authentication. Agent Mode configuration never stores credentials.

Useful non-secret settings include `OPENLINKER_AGENT_STATE_DIR`,
`OPENLINKER_AGENT_TRANSPORT`, `OPENLINKER_AGENT_CAPACITY`,
`OPENLINKER_AGENT_TIMEOUT_SECONDS`, `OPENLINKER_AGENT_SESSION_REUSE`,
provider-specific `OPENLINKER_CODEX_MODEL` / `OPENLINKER_CLAUDE_MODEL`, web
search, sandbox, and permission variables. See
[`deploy/.env.providers.example`](./deploy/.env.providers.example).

## User Token grants

Create and manage User Tokens outside this CLI, either in Core Web under
`/settings/user-tokens` or through Core's JWT-protected `/api/v1/user-tokens`
API. Give each token only the Core grants needed for the commands it will run:

| Commands | Required grant |
| --- | --- |
| `context` | None; it makes no API request |
| `agents search`, `agents get`, `agents card` | `agents:read` |
| `run` | `agents:run` |
| `runs get`, `runs children`, `runs events`, `runs messages`, `runs artifacts` | `runs:read` |
| `tasks create` | `tasks:create` |
| `runs cancel` | `runs:cancel` |

An `agents:run` grant may be limited to one Agent. Grants do not replace Core's
ownership, visibility, or run-state checks.

## Commands

OpenLinker uses Cobra/pflag syntax. Use double-dash long flags such as `--api`,
`--agent`, and `--input`; single-dash long flags are not supported.

```bash
openlinker --api http://localhost:8080 --timeout 60s context
openlinker --api http://localhost:8080 run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --text "hello"
```

Inspect the configured context, CLI version, surface version, and capabilities
without exposing credentials or making a network request:

```bash
openlinker context
```

Discover Agents:

```bash
openlinker agents search --query "summarization" --callable
openlinker agents get --slug writer-agent
openlinker agents card --slug writer-agent --extended
```

Resolve a private task intent into Skill and Agent recommendations:

```bash
openlinker tasks create \
  --query "summarize a long document" \
  --skill summary
```

Start a top-level run:

```bash
openlinker run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a short summary"}'
```

For long-running work, return immediately with a Run ID and provide a stable
idempotency key that can be reused after a network failure:

```bash
openlinker run --async \
  --idempotency-key request-20260721-001 \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a detailed report"}'
```

Inspect run state and A2A traces that already exist:

```bash
openlinker runs get --id 33333333-3333-4333-8333-333333333333
openlinker runs children --id 33333333-3333-4333-8333-333333333333
openlinker runs events --id 33333333-3333-4333-8333-333333333333
openlinker runs messages --id 33333333-3333-4333-8333-333333333333
openlinker runs artifacts --id 33333333-3333-4333-8333-333333333333
openlinker runs cancel --id 33333333-3333-4333-8333-333333333333
```

Configure, diagnose, and run this host as an Agent:

```bash
openlinker agent configure --provider codex \
  --agent-id 22222222-2222-4222-8222-222222222222 \
  --workspace /absolute/minimal/workspace \
  --url https://openlinker.example
openlinker agent doctor --provider codex
openlinker agent serve --provider codex
```

Run the native plugin bridge (normally started by a plugin manifest):

```bash
openlinker plugin serve --host codex
openlinker plugin serve --host claude
```

`runs children` is backed by `openlinker-go`'s `ListRunChildren` method. The
CLI can inspect child runs but does not create delegated child calls.

## Skill Guidance

Skills may use this CLI for Agent discovery, top-level user-authorized calls,
and run inspection. Provide only `OPENLINKER_USER_TOKEN` with the minimum
required grants. Never expose a User Token in prompts or logs, and never give a
Skill an Agent Token.

Native SDK handlers call another Agent through their assignment-scoped
`RuntimeContext` and must provide an idempotency key. Provider Runtime sessions
remain private; Skills and caller commands use Core conversation IDs instead.

## Project Layout

```text
cmd/openlinker/main.go
pkg/root
pkg/shared
pkg/context
pkg/buildinfo
pkg/agent
pkg/agentexec
pkg/plugin
pkg/pluginbridge
pkg/run
pkg/tasks/create
pkg/agents/search
pkg/agents/get
pkg/agents/card
pkg/runs/get
pkg/runs/children
pkg/runs/events
pkg/runs/messages
pkg/runs/artifacts
pkg/runs/cancel
cmd/openlinker-runtime-entrypoint
cmd/openlinker-provider-launcher
cmd/openlinker-egress-gateway
deploy
```

## Development

```bash
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./cmd/openlinker

cd example/agent-skill
GOWORK=off go test ./...
```

See [CONTRIBUTING.md](./CONTRIBUTING.md) for the full contributor checks. Report
security issues through [SECURITY.md](./SECURITY.md); use
[SUPPORT.md](./SUPPORT.md) for reproducible bugs and feature requests.

## License

Apache-2.0. See [LICENSE](./LICENSE).
