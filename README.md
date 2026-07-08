# OpenLinker CLI

Small JSON-first Cobra CLI for OpenLinker agents, skills, and automation.

The CLI is intentionally thin over the Go SDK. It is designed for agent-facing
skills:

- stdout is always JSON
- diagnostics and errors go to stderr
- tokens are read from flags or environment variables but are never printed
- `delegate` defaults to the current OpenLinker run context from environment
- command implementations are split by subcommand under `pkg/`

## Configuration

```bash
export OPENLINKER_API_BASE=http://localhost:8080
export OPENLINKER_TOKEN=ol_user_xxx
export OPENLINKER_RUNTIME_TOKEN=ol_runtime_xxx
```

Runtime context, usually injected by the OpenLinker runtime:

```bash
export OPENLINKER_RUN_ID=run_xxx
export OPENLINKER_AGENT_ID=agent_xxx
export OPENLINKER_TRACE_ID=trace_xxx
```

## Commands

OpenLinker uses Cobra/pflag style flags. Use double-dash long flags such as
`--api`, `--agent`, and `--input`. Single-dash long flags like `-api` are not
supported.

Global flags:

```bash
openlinker --api http://localhost:8080 --timeout 60s context
openlinker --api http://localhost:8080 --token ol_user_xxx run --agent agent_writer --text "hello"
openlinker --api http://localhost:8080 --runtime-token ol_runtime_xxx delegate --agent agent_reviewer --text "review this"
```

Inspect runtime context without exposing credentials:

```bash
openlinker context
```

Discover agents:

```bash
openlinker agents search --query "summarization" --callable
openlinker agents get --slug writer-agent
openlinker agents card --slug writer-agent --extended
```

Run an agent from a user/API context:

```bash
openlinker run \
  --agent agent_writer \
  --input '{"task":"write a short summary"}'
```

Delegate from the current OpenLinker run:

```bash
openlinker delegate \
  --agent agent_reviewer \
  --reason "review the generated summary" \
  --input '{"task":"review this draft"}'
```

The command above uses `OPENLINKER_RUN_ID` as the parent run. Override it with
`--parent-run` when needed.

Inspect run state and A2A delegation traces:

```bash
openlinker runs get --id run_xxx
openlinker runs children --id run_xxx
openlinker runs events --id run_xxx
openlinker runs messages --id run_xxx
openlinker runs artifacts --id run_xxx
```

`runs children` is backed by `openlinker-go`'s `ListRunChildren` SDK method.

## Skill Guidance

Skills should call this CLI instead of directly handling OpenLinker tokens. A
skill can decide when to delegate, then run:

```bash
openlinker delegate --agent agent_xxx --reason "..." --input '{"task":"..."}'
```

Do not put tokens in skill files or command examples. Use environment variables,
workload identity, or a credential broker outside of the agent prompt.

## Project Layout

The executable entrypoint is intentionally small:

```text
cmd/openlinker/main.go
```

Cobra command wiring lives in:

```text
pkg/root
```

Shared CLI utilities live in:

```text
pkg/shared
```

Each user-facing subcommand has its own package:

```text
pkg/context
pkg/run
pkg/delegate
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

Run tests with:

```bash
go test ./...
```
