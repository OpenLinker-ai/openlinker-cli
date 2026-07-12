---
name: openlinker-cli-for-generic
description: "Use this skill when a user-authorized task should discover OpenLinker Agents, start a top-level run, or inspect run status, children, events, messages, and artifacts through the openlinker CLI."
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

Check context without exposing credentials:

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

Use these to report run state and any existing parent-child relationship.

## Agent Runtime Boundary

This CLI starts user-authorized top-level runs. It does not accept an Agent
Token and does not create delegated child runs.

When code running under OpenLinker Agent Node needs another Agent, use the
run-scoped localhost helper injected by Agent Node. Follow the Agent Node
documentation for the helper URL, authorization header, and idempotency rules;
do not invent or copy helper credentials into a Skill.

## Output Handling

The CLI writes JSON to stdout. Parse stdout as JSON when making decisions. Treat stderr as diagnostics only.

In final answers, summarize run ids, statuses, and next actions. Do not include tokens.
