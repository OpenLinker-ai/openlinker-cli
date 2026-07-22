# Codex Native Runtime Compatibility Design

Status: accepted on 2026-07-22

## Problem

The native Codex caller and Runtime Agent paths expose three independent CLI
compatibility defects:

1. Codex 0.145 adds a standards-compatible `_meta` object to
   `tools/call.params`. The plugin bridge strictly rejects that field before it
   validates the tool arguments, so every native OpenLinker MCP call fails with
   JSON-RPC `-32602` even though the advertised tool arguments are valid.
2. Codex Runtime Agent execution intentionally uses `--ignore-user-config`, but
   the CLI has no explicit base-URL setting. An OpenAI-compatible Router key is
   therefore sent to the default OpenAI endpoint instead of the configured
   Router.
3. The Runtime Agent documentation permits a minimal existing workspace, while
   the generated Codex command rejects a non-Git directory because it omits
   `--skip-git-repo-check`.

The Provider session-reuse implementation already has unit coverage and remains
owned by `pkg/agentexec`. The fixes must preserve that ownership and must not
move Provider execution into the plugin, Core, SDK, or Agent Node.

## Goals

- Accept the standard MCP request metadata emitted by current Codex clients.
- Keep strict rejection for every other unknown `tools/call.params` field.
- Add an explicit, non-secret Codex base URL that works for both first execution
  and session resume while user configuration remains ignored.
- Allow the documented minimal non-Git workspace.
- Preserve secret isolation, command argument safety, Core-owned conversation
  keys, and private Provider session IDs.
- Release the corrected CLI as the next `v0.2.0-rc.N` artifact for the plugin to
  pin by checksum.

## Non-goals and Repository Boundary

- Do not change Core, Cloud, SDK, Agent Node, or plugin packaging here.
- Do not inherit arbitrary Codex `config.toml`, MCP servers, rules, or approval
  settings.
- Do not implement an executable wrapper around Codex.
- Do not expose Provider session IDs through OpenLinker results or events.
- Do not add a Router-specific WebSocket policy; Codex retains its own
  WebSocket-to-HTTP fallback behavior.

## MCP Request Compatibility

`toolCallParams` will gain one ignored `_meta` field represented as raw JSON.
The outer params decoder will continue to use `DisallowUnknownFields`, so only
`name`, `arguments`, and `_meta` are accepted. Tool argument validation and the
tool input schemas remain unchanged.

Regression coverage will send the same shape used by Codex 0.145, including
`progressToken` and `x-codex-turn-metadata`, to a local bridge and prove that a
safe local Agent Mode diagnostic tool reaches normal tool handling. A separate
test will prove that an unrelated unknown outer field still produces `-32602`.

## Explicit Codex Base URL

The non-secret Agent configuration will add `codex_base_url`. It is configurable
through all existing Agent Mode surfaces:

- standalone CLI flag: `--codex-base-url`;
- environment: `OPENLINKER_CODEX_BASE_URL`;
- native MCP `configure_agent_mode` argument: `codex_base_url`;
- production example environment and bilingual documentation.

The URL is optional. When present it must be an absolute `http` or `https` URL
with a host and without credentials, query, or fragment. A path such as `/v1`
is allowed because OpenAI-compatible Routers commonly expose that prefix.

`agent.Service` passes the validated value into `agentexec.ProviderConfig`.
`CodexProvider` adds a direct argument equivalent to
`-c openai_base_url="<url>"` to both new and resumed Codex executions. The value
is passed as one `exec.Command` argument; it is never evaluated by a shell and
is not copied into the prompt, Run metadata, events, or result.

The CLI continues to use `--ignore-user-config` and `--ignore-rules`. This keeps
Runtime Agent execution deterministic and prevents unrelated user MCP servers
or approval policy from entering a called Agent session.

## Minimal Workspace Behavior

Both new and resumed Codex commands will pass `--skip-git-repo-check` at the
position accepted by the corresponding Codex subcommand. Existing workspace
existence and directory checks remain in Agent configuration. No repository is
created and no Git state is modified.

## Testing

Unit tests will cover:

- `_meta` acceptance and continued unknown-field rejection;
- configuration file, flag, environment, and MCP propagation of
  `codex_base_url`;
- URL validation, including allowed `/v1` paths and rejected credentials,
  query, fragment, or non-HTTP schemes;
- exact new-session and resume argument generation;
- non-Git workspace execution arguments;
- existing Core-conversation-to-Provider-session reuse and session recovery.

The repository gate is `GOWORK=off go test ./...`, followed by race, vet, build,
and the existing image/security checks required by CI. A real acceptance test
must use a Router-backed Codex invocation and a two-turn OpenLinker conversation;
the second Run must report `codex_session_resumed=true` without returning the
raw Codex session ID.

## Release and Rollback

After the CLI PR is merged, publish the next release candidate with the normal
signed/checksummed release workflow. The plugin will update its lock only after
all expected platform archives and adjacent checksum files exist.

Rollback is the preceding CLI release candidate. The new configuration field is
optional, so removing it restores default Codex endpoint behavior without a
config migration.
