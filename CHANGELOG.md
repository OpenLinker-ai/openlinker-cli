# Changelog

All notable changes to `openlinker-cli` will be documented in this file.

The CLI is pre-1.0. Breaking changes may occur while the OpenLinker API and
runtime boundaries are stabilizing.

## Unreleased

### Changed

- Pinned `openlinker-go` to the Runtime v2-only SDK revision that exposes Agent
  credentials exclusively as Agent Tokens.
- Reworked bundled Skills and examples around User Token discovery, top-level
  calls, and run inspection. Runtime delegation now points to Agent Node's
  run-scoped localhost helper.
- Clarified the command-to-grant mapping and the boundary between this
  user-facing CLI and Agent Node's Runtime v2 WebSocket/Pull responsibilities.
- Added bilingual contributor, security, support, and release guidance plus
  issue/PR templates, dependency updates, and complete release archives.

### Removed

- Breaking: removed the `delegate` command and its retired
  `/api/v1/agent-runtime/call-agent` request path.
- Breaking: removed `--runtime-token`, `OPENLINKER_RUNTIME_TOKEN`, and all
  handling of long-lived Agent credentials from the CLI.
- Breaking: removed legacy `OPENLINKER_TOKEN`, `OPENLINKER_DEMO_JWT`,
  `OPENLINKER_API_URL`, and `OPENLINKER_AGENT_TOKEN` environment aliases. User
  commands now use only `OPENLINKER_USER_TOKEN` and `OPENLINKER_API_BASE`.
