---
name: openlinker-cli-for-generic
description: "Use this skill when a user-authorized task should discover OpenLinker Agents, resolve task recommendations, start a retry-safe top-level run, or inspect and explicitly cancel runs through the openlinker CLI."
---

# OpenLinker CLI Skill

Use the `openlinker` CLI as the user/API boundary to OpenLinker Core. Do not
handle or print tokens directly. Assume credentials and context are provided by
the environment unless the user explicitly supplies flags.

The CLI uses Cobra/pflag syntax. Always use double-dash long flags such as `--api`, `--agent`, `--input`, and `--id`; do not use single-dash long flags such as `-api`.

## Environment

The API base is optional and defaults to the local Core API:

```bash
OPENLINKER_API_BASE=http://localhost:8080
```

The CLI accepts one credential:

```bash
OPENLINKER_USER_TOKEN
```

The surrounding environment may inject identifiers for diagnostics:

```bash
OPENLINKER_RUN_ID
OPENLINKER_AGENT_ID
OPENLINKER_TRACE_ID
```

These identifiers are context only. They do not authorize calls. Never include
real token values in prompts, skill files, logs, or final answers.

## First Checks

Check the effective API base, CLI version, surface version, and capabilities
without exposing credentials:

```bash
openlinker context
```

If a command needs authentication and fails, ask the operator to provide
`OPENLINKER_USER_TOKEN` outside the prompt.

## Discover Agents

Search callable agents:

```bash
openlinker agents search --query "summary" --callable
```

Fetch an agent:

```bash
openlinker agents get --slug writer-agent
```

Fetch an agent card:

```bash
openlinker agents card --slug writer-agent --extended
```

Use discovery when the user gives a capability but not a concrete target agent id.

Resolve a private task intent when structured Skill matching is useful:

```bash
openlinker tasks create --query "summarize a long document" --skill summary
```

## Start a User Run

Use `run` when acting from a user/API context and starting a top-level OpenLinker run:

```bash
openlinker run \
  --agent agent_writer \
  --input '{"task":"write a short summary"}'
```

Plain text input is allowed:

```bash
openlinker run --agent agent_writer --text "write a short summary"
```

Prefer asynchronous execution for long work and provide one stable idempotency
key per logical request. Reuse the same key after an uncertain network result:

```bash
openlinker run --async \
  --idempotency-key request-20260721-001 \
  --agent agent_writer \
  --input-file -
```

## Inspect Runs

Get a run:

```bash
openlinker runs get --id run_xxx
```

Inspect A2A children:

```bash
openlinker runs children --id run_xxx
```

Inspect events, messages, and artifacts:

```bash
openlinker runs events --id run_xxx --limit 50
openlinker runs messages --id run_xxx
openlinker runs artifacts --id run_xxx
```

Only when the user explicitly requests cancellation, re-read the Run and then
cancel it once:

```bash
openlinker runs get --id run_xxx
openlinker runs cancel --id run_xxx
```

Use these to report run state and any existing parent-child relationship.

## Agent Runtime Boundary

These caller commands start user-authorized top-level runs. They do not accept
an Agent Token and do not create delegated child runs. An executing native
Agent must use its SDK assignment-scoped `RuntimeContext` with an idempotency
key; never copy Runtime credentials into a Skill.

## Output Handling

The CLI writes JSON to stdout. Parse stdout as JSON when making decisions. Treat stderr as diagnostics only.

In final answers, summarize run ids, statuses, and next actions. Do not include tokens.
