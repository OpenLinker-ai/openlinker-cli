# Changelog

## Unreleased

- Add platform Skill package discovery, metadata, verified canonical download, published-version private import and owned Agent binding commands. Skill permissions are explicit opt-in; login defaults stay unchanged. Local installation remains an external `npx skills` operation.

## v0.2.0 — 2026-10-04

This formal release includes the platform-client migration and browser login
from the v0.2.0 prereleases. The CLI is pre-1.0; read
[MIGRATION.md](./MIGRATION.md) before replacing an older all-in-one installation.

### Breaking changes

- Default to `https://openlinker.ai` when no API flag or environment override is
  supplied. Precedence remains `--api` > `OPENLINKER_API_BASE` > `OPENLINKER_URL`.
  Local/self-hosted scripts that relied on `http://localhost:8080` must now select
  it explicitly. Saved credentials stay bound to their original API instance.
- Remove local `agent` and `plugin` commands, Worker/Provider/Browser execution,
  and the Plugin module dependency. Native MCP, deep Agent control and Browser
  entry points use Plugin's `openlinker-plugin-host`; ordinary local bridging
  uses Agent Node. There is no forwarding or automatic execution migration.
- Go import consumers must replace the removed CLI Browser/Agent implementation
  packages with the appropriate Plugin packages. The unused implementation
  helper `pkg/shared.SplitCSV` has also been removed.
- Remove the `delegate` command, `--runtime-token`/`OPENLINKER_RUNTIME_TOKEN`,
  and the legacy `OPENLINKER_TOKEN`, `OPENLINKER_DEMO_JWT`, `OPENLINKER_API_URL`
  and `OPENLINKER_AGENT_TOKEN` aliases. Use `OPENLINKER_USER_TOKEN` and
  `OPENLINKER_API_BASE`/`OPENLINKER_URL` for explicit caller configuration.
- Existing native Plugin packages that launch the old CLI must retain their
  pinned executable until the matching Plugin host package is installed.
  Preserve identities, credentials, sessions and state, and stop/drain the old
  Worker before starting a replacement. Never share a data directory between
  two running Workers; source installation does not migrate live state.

### Added and retained

- Add browser and device-code `auth login`, online `auth status`, and revoking
  `auth logout`. Core migration 094 and the matching authorization page are
  required. Credentials default to the OS keyring; POSIX plaintext file storage
  requires explicit selection. Saved logins are isolated by API instance.
- Retain platform `context`, `agents`, `tasks`, `run`, and `runs` commands, User
  Token grants, JSON output, asynchronous calls, cancellation and idempotency.
  The CLI does not accept Runtime credentials or provide delegated execution.
- Pin the verified Go SDK revision `63fc87d73406`, including synchronized public
  contracts and dependency updates. CLI source builds require Go 1.26.4;
  the SDK retains its Go 1.25 baseline.
- Publish six platform archives with adjacent SHA-256 files and exact build
  metadata, bilingual documentation, and caller Skills.

The preview history below describes earlier builds. Its execution packages,
Plugin dependency and Browser images are superseded by the migration above and
are not part of the v0.2.0 platform CLI. The preview's Removed entries remain
effective and are included in the breaking changes above.

## v0.2.0-rc.1 — historical preview

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
