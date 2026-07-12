---
name: openlinker-cli
description: "Use this skill through Blades when a user-authorized task should discover OpenLinker Agents, start a top-level run, or inspect run status, children, events, messages, and artifacts."
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

Check current context:

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

Start a top-level run:

```json
{
  "skill_name": "openlinker-cli",
  "script_path": "scripts/openlinker",
  "args": ["run", "--agent", "agent_writer", "--input", "{\"task\":\"write a short summary\"}"]
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

## Agent Runtime Boundary

This CLI starts user-authorized top-level runs. It does not accept an Agent
Token and does not create delegated child runs.

When code running under OpenLinker Agent Node needs another Agent, use the
run-scoped localhost helper injected by Agent Node. Follow the Agent Node
documentation for the helper URL, authorization header, and idempotency rules;
do not invent or copy helper credentials into a Skill.

## Stop Rule

After a successful CLI JSON response, summarize the result and stop. Do not
repeat the same command unless the user asks for another query.
