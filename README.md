# OpenLinker CLI

Small JSON-first CLI for discovering and invoking OpenLinker Agents from a
user/API context. It is intentionally thin over `openlinker-go`:

- stdout is always JSON;
- diagnostics and errors go to stderr;
- the CLI accepts only an OpenLinker User Token and never prints it;
- command implementations are split by subcommand under `pkg/`.

The CLI is not an Agent runtime. Runtime v2 connections, WebSocket/Pull
transport switching, durable execution, cancellation, and delegated Agent
calls belong to [OpenLinker Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node).
Agent Node gives an executing backend a run-scoped localhost helper for A2A
delegation; do not pass a long-lived Agent Token to this CLI or to a business
Agent process.

## Configuration

```bash
export OPENLINKER_API_BASE=http://localhost:8080
export OPENLINKER_USER_TOKEN=ol_user_xxx
```

The CLI does not accept the retired `OPENLINKER_TOKEN`,
`OPENLINKER_RUNTIME_TOKEN`, `OPENLINKER_DEMO_JWT`, or `OPENLINKER_API_URL`
aliases. `--token` may be used to provide a User Token explicitly.

Run identifiers may be injected by a surrounding environment for diagnostics:

```bash
export OPENLINKER_RUN_ID=run_xxx
export OPENLINKER_AGENT_ID=agent_xxx
export OPENLINKER_TRACE_ID=trace_xxx
```

These values are context only. They do not authorize runtime delegation.

## Commands

OpenLinker uses Cobra/pflag syntax. Use double-dash long flags such as `--api`,
`--agent`, and `--input`; single-dash long flags are not supported.

```bash
openlinker --api http://localhost:8080 --timeout 60s context
openlinker --api http://localhost:8080 --token ol_user_xxx run --agent agent_writer --text "hello"
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
  --agent agent_writer \
  --input '{"task":"write a short summary"}'
```

Inspect run state and A2A traces that already exist:

```bash
openlinker runs get --id run_xxx
openlinker runs children --id run_xxx
openlinker runs events --id run_xxx
openlinker runs messages --id run_xxx
openlinker runs artifacts --id run_xxx
```

`runs children` is backed by `openlinker-go`'s `ListRunChildren` method. The
CLI can inspect child runs but does not create delegated child calls.

## Skill Guidance

Skills may use this CLI for Agent discovery, top-level user-authorized calls,
and run inspection. Provide only `OPENLINKER_USER_TOKEN` with the minimum
required scopes. Never expose a User Token in prompts or logs, and never give a
Skill an Agent Token.

When code executing inside Agent Node needs to call another Agent, use the
run-scoped localhost helper injected by Agent Node. The official Agent Node
documentation defines its URL, authorization header, and idempotency rules.

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
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/openlinker
```
