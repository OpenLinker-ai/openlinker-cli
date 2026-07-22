# Changelog

All notable changes to `openlinker-cli` will be documented in this file.

The CLI is pre-1.0. Breaking changes may occur while the OpenLinker API and
runtime boundaries are stabilizing.

## v0.2.0-rc.1 - Unreleased

### Added

- Added `tasks create`, asynchronous `run --async`, `runs cancel`, and an
  explicit `--idempotency-key` for retry-safe Agent calls.
- Added versioned CLI surface and capability metadata to the redacted
  `context` JSON output for native plugin compatibility checks.
- Added `agent configure/serve/status/doctor` and `plugin serve` for token-only
  Runtime registration, native stdio MCP plugins, Core-owned conversation
  continuity, and private Codex/Claude session reuse.
- Added hardened Codex, Claude, and egress-gateway production image targets.

### Changed

- Made native Codex MCP calls accept Codex client `_meta`, added a validated
  OpenAI-compatible Base URL setting, and allowed new or resumed sessions to
  run from non-Git workspaces.
- Pinned `openlinker-go` to the Runtime v2-only SDK revision that exposes Agent
  credentials exclusively as Agent Tokens.
- Reworked bundled Skills and examples around User Token discovery, top-level
  calls, run inspection, and SDK `RuntimeContext` delegation.
- Clarified the command-to-grant mapping and the credential boundary between
  caller commands, the Runtime Worker, and provider subprocesses.
- Added bilingual contributor, security, support, and release guidance plus
  issue/PR templates, dependency updates, and complete release archives.

### Removed

- Breaking: removed the `delegate` command and its retired
  `/api/v1/agent-runtime/call-agent` request path.
- Breaking: removed `--runtime-token`, `OPENLINKER_RUNTIME_TOKEN`, and all
  handling of Agent credentials from caller commands. Agent credentials are
  accepted only by the isolated `agent` / plugin Agent Mode surface.
- Breaking: removed legacy `OPENLINKER_TOKEN`, `OPENLINKER_DEMO_JWT`,
  `OPENLINKER_API_URL`, and `OPENLINKER_AGENT_TOKEN` environment aliases. User
  commands now use only `OPENLINKER_USER_TOKEN` and `OPENLINKER_API_BASE`.
