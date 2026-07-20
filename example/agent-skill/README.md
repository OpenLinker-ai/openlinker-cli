# OpenLinker Skill Agent Example

[简体中文](README.zh-CN.md)

This example shows how a Blades Agent can use the OpenLinker CLI through a
Skill. The repository does not commit the compiled `openlinker` binary, so build
it locally before running this example.

## Build the CLI

From the repository root:

```bash
mkdir -p ./example/agent-skill/skills/openlinker-cli/scripts
go build -o ./example/agent-skill/skills/openlinker-cli/scripts/openlinker ./cmd/openlinker
chmod +x ./example/agent-skill/skills/openlinker-cli/scripts/openlinker
```

The generated file is a local build artifact. Do not commit it.

## Run

```bash
cd example/agent-skill

OPENAI_API_KEY=sk-xxx \
OPENLINKER_API_BASE=https://api.openlinker.ai \
OPENLINKER_USER_TOKEN=ol_user_xxx \
go run .
```

Optional execution context:

```bash
OPENLINKER_AGENT_ID=agent_xxx
OPENLINKER_RUN_ID=run_xxx
OPENLINKER_TRACE_ID=trace_xxx
```

The Skill asks Blades to invoke `scripts/openlinker` through
`run_skill_script`. The CLI uses the User Token for discovery and top-level
runs; real tokens must stay outside `SKILL.md`, prompts, logs, and commits.
Delegated child calls are not a CLI feature. Agent runtimes use OpenLinker Agent
Node's run-scoped localhost helper instead of exposing a long-lived Agent Token
to the business Agent.

## Use the Skill Elsewhere

Copy `skills/openlinker-cli` into your Agent project, build `cmd/openlinker` for
the target machine, and place the resulting executable at:

```text
skills/openlinker-cli/scripts/openlinker
```

Then load the Skill in your Agent framework and provide the same OpenLinker
environment variables at runtime.
