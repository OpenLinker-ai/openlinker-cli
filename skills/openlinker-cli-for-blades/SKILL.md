---
name: openlinker-cli-for-blades
description: "Use this skill through Blades when a user-authorized task should discover OpenLinker Agents, resolve task recommendations, start a retry-safe top-level run, or inspect and explicitly cancel runs."
---

# OpenLinker CLI Skill

Use this skill through the Blades `run_skill_script` tool. Do not invent shell commands.

The executable script is:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker"
}
```

Credentials and context must come from environment variables. Never print or
reveal credential values.

The CLI uses Cobra/pflag syntax. Always pass long flags as separate double-dash arguments, for example `["--agent", "agent_writer"]`; do not use single-dash long flags such as `["-agent", "agent_writer"]`.

## Environment

The CLI accepts one credential:

```bash
OPENLINKER_USER_TOKEN
```

The API base is optional and defaults to the local Core API:

```bash
OPENLINKER_API_BASE
```

The surrounding environment may also inject identifiers for diagnostics:

```bash
OPENLINKER_RUN_ID
OPENLINKER_AGENT_ID
OPENLINKER_TRACE_ID
```

These identifiers are context only. They do not authorize calls.

## Commands

Check the effective API base, CLI version, surface version, and capabilities:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["context"]
}
```

Search callable agents by text:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["agents", "search", "--query", "summary", "--callable"]
}
```

Search callable agents by tag:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["agents", "search", "--tag", "a2a", "--callable"]
}
```

Get an agent by slug:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["agents", "get", "--slug", "writer-agent"]
}
```

Get an extended Agent Card:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["agents", "card", "--slug", "writer-agent", "--extended"]
}
```

Resolve a private task intent:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["tasks", "create", "--query", "summarize a long document", "--skill", "summary"]
}
```

Start a top-level run:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["run", "--agent", "agent_writer", "--input", "{\"task\":\"write a short summary\"}"]
}
```

Prefer asynchronous execution for long work and reuse one stable idempotency
key when retrying the same logical request:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["run", "--async", "--idempotency-key", "request-20260721-001", "--agent", "agent_writer", "--input", "{\"task\":\"write a detailed report\"}"]
}
```

Inspect a run:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["runs", "get", "--id", "run_xxx"]
}
```

Inspect run children:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["runs", "children", "--id", "run_xxx"]
}
```

The same `runs` group can inspect `events`, `messages`, and `artifacts`.

Only after an explicit user request, re-read the Run and invoke
`["runs", "cancel", "--id", "run_xxx"]` once.

## Agent Runtime Boundary

These caller commands start user-authorized top-level runs. They do not accept
an Agent Token and do not create delegated child runs. An executing native
Agent must use its SDK assignment-scoped `RuntimeContext` with an idempotency
key; never copy Runtime credentials into a Skill.

## Stop Rule

After a successful CLI JSON response, summarize the result and stop. Do not
repeat the same command unless the user asks for another query.
