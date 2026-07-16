# OpenLinker CLI

Chinese documentation: [README.zh-CN.md](./README.zh-CN.md)

Small JSON-first CLI for discovering and invoking OpenLinker Agents from a
user/API context. It is intentionally thin over `openlinker-go`:

- stdout is always JSON;
- diagnostics and errors go to stderr;
- the CLI accepts only an OpenLinker User Token and never prints it;
- command implementations are split by subcommand under `pkg/`.

The CLI is not an Agent runtime. Runtime connections, WebSocket/long-poll
transport switching, durable execution, cancellation, and delegated Agent
calls belong to the official SDK Runtime Workers. Native handlers use their
SDK `RuntimeContext`; [OpenLinker Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node)
is only a temporary Adapter for existing HTTP, command, Codex, or A2A backends
and gives those backends a run-scoped localhost helper. Do not pass a
long-lived Agent Token to this CLI or to a business Agent process.

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

Inspect context without exposing credentials:

```bash
openlinker context
```

Discover Agents:

```bash
openlinker agents search --query "summarization" --callable
openlinker agents get --slug writer-agent
openlinker agents card --slug writer-agent --extended
```

Start a top-level run:

```bash
openlinker run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a short summary"}'
```

Inspect run state and A2A traces that already exist:

```bash
openlinker runs get --id 33333333-3333-4333-8333-333333333333
openlinker runs children --id 33333333-3333-4333-8333-333333333333
openlinker runs events --id 33333333-3333-4333-8333-333333333333
openlinker runs messages --id 33333333-3333-4333-8333-333333333333
openlinker runs artifacts --id 33333333-3333-4333-8333-333333333333
```

`runs children` is backed by `openlinker-go`'s `ListRunChildren` method. The
CLI can inspect child runs but does not create delegated child calls.

## Skill Guidance

Skills may use this CLI for Agent discovery, top-level user-authorized calls,
and run inspection. Provide only `OPENLINKER_USER_TOKEN` with the minimum
required grants. Never expose a User Token in prompts or logs, and never give a
Skill an Agent Token.

Native SDK handlers call another Agent through their assignment-scoped
`RuntimeContext` and must provide an idempotency key. Only an existing backend
running behind Agent Node should use the run-scoped localhost helper injected
by that Adapter. The Agent Node documentation defines its URL, authorization
header, and idempotency rules.

## Project Layout

```text
cmd/openlinker/main.go
pkg/root
pkg/shared
pkg/context
pkg/run
pkg/agents/search
pkg/agents/get
pkg/agents/card
pkg/runs/get
pkg/runs/children
pkg/runs/events
pkg/runs/messages
pkg/runs/artifacts
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
