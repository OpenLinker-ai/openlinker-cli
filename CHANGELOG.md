# Changelog

## Unreleased — platform client boundary

- Remove local `agent` and `plugin` commands and their Plugin module dependency.
- Move native MCP, deep Agent control and Browser/proxy entry points to Plugin's
  `openlinker-plugin-host`; ordinary Codex/Claude bridging uses Agent Node.
- Keep caller commands and JSON contracts. See [MIGRATION.md](./MIGRATION.md)
  before upgrading an older native Plugin or Worker installation.


All notable changes to `openlinker-cli` will be documented in this file.

The CLI is pre-1.0. Breaking changes may occur while the OpenLinker API and
runtime boundaries are stabilizing.

## Unreleased

### Changed

- Browser Runtime, Codex/Claude execution adapters, native assets and portable
  image/deployment definitions now belong to `openlinker-plugin`. CLI remains
  the single public executable and command/MCP composition layer; CLI commands,
  flags and Agent registration identity are unchanged by this extraction.
- Pin the Plugin implementation to the immutable
  `v0.1.58-0.20260905174434-1fea781f2b48` module with verified checksums. The Go
  SDK remains pinned to `v0.2.0-rc7` and is still the sole Runtime Worker
  lifecycle, assignment/ACK and spool implementation.
- Publish Plugin source before dependent CLI artifacts. Provider images and
  native packages require a matching checksum-locked CLI release; source
  publication does not deploy or migrate Runtime volumes.

### Removed

- Breaking for Go import consumers: the former CLI Browser/Agent implementation
  packages moved to Plugin's `packages/browser-runtime` and
  `packages/agent-adapters`. Import those packages from the Plugin module;
  standalone CLI users retain the existing command surface.

## v0.2.0-rc.1 - Unreleased

### Added

- Added capability-gated `restricted` and `full` Browser interaction. Full
  Attachments receive only Core-issued, generation-fenced mutation authority
  for an exact HTTPS origin scope and record a bounded local mutation journal.
- Added deterministic Browser reliability controls: per-principal origin
  budgets, challenge classification evidence, access-denial guards, document
  generation tracking over Playwright's pipe-backed CDP session, and
  checkpoint-safe profile/environment evidence.
- Added reproducible real-Chromium acceptance harnesses for isolated Browser
  behavior and the credential-backed Codex/Claude restricted/full matrix.
- Added `tasks create`, asynchronous `run --async`, `runs cancel`, and an
  explicit `--idempotency-key` for retry-safe Agent calls.
- Added versioned CLI surface and capability metadata to the redacted
  `context` JSON output for native plugin compatibility checks.
- Added `agent configure/serve/status/doctor` and `plugin serve` for token-only
  Runtime registration, native stdio MCP plugins, Core-owned conversation
  continuity, and private Codex/Claude session reuse.
- Added hardened Codex, Claude, and egress-gateway production image targets.
- Added the isolated Browser execution profile. Human continuation is
  capability-gated: with `human_control_available=false`, a required challenge
  remains fenced and terminal; with `human_control_available=true`, the Owner
  can drive the bounded `PAUSED -> HUMAN -> PAUSED -> AGENT` lifecycle without
  persisting Viewer frames or input as Run events.

### Changed

- Updated the formal `openlinker-go` dependency to `v0.2.0-rc5`; no workspace
  replacement or vendored SDK is used by release builds.
- Browser Provider images now keep page traffic behind the egress gateway,
  expose no remote-debugging listener, enforce exact mutation-origin scopes,
  bundle the checksum-pinned OpenLinker Plugin v0.1.2, and publish
  dual-architecture image, SBOM, and provenance evidence. The optional Google
  Chrome image remains operator-built and amd64-only.
- The Egress Gateway now tries a bounded set of already validated public DNS
  addresses before failing an HTTPS CONNECT. It never re-resolves during the
  fallback and rejects the complete answer set if any address is non-public.
- Made native Codex MCP calls accept Codex client `_meta`, added a validated
  OpenAI-compatible Base URL setting, and allowed new or resumed sessions to
  run from non-Git workspaces.
- Pinned `openlinker-go` to the Runtime v2-only SDK revision that exposes Agent
  credentials exclusively as Agent Tokens.
- Kept Browser Viewer actions, frames and validation in the CLI while using
  the Go SDK only for an explicitly registered opaque Runtime extension
  transport.
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
