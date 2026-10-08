# OpenLinker CLI

Chinese documentation: [README.zh-CN.md](./README.zh-CN.md)

JSON-first platform client for discovering Agents, creating tasks, starting runs
and inspecting results through the public Core API and `openlinker-go`.
It accepts User Tokens; stdout contains JSON and diagnostics go to stderr.

Local Codex/Claude bridging belongs to [Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node).
Native MCP, Agent Mode and Browser execution belong to [Plugin](https://github.com/OpenLinker-ai/openlinker-plugin).
This CLI has no local Worker, Provider adapter, Browser server or forwarding command.
See [MIGRATION.md](./MIGRATION.md) before replacing an older all-in-one CLI.

## Status and installation

The CLI is pre-1.0 and follows the Core API contract. Pin a release and review
`CHANGELOG.md` before upgrading.

Download a Linux, macOS, or Windows archive and its adjacent `.sha256` file
from [GitHub Releases](https://github.com/OpenLinker-ai/openlinker-cli/releases),
then verify the checksum before placing `openlinker` on your `PATH`. Go users
can install a fixed release directly:

```bash
go install github.com/OpenLinker-ai/openlinker-cli/cmd/openlinker@v0.x.y
```

Replace `v0.x.y` with the release you have chosen.

## Platform Skill packages

`npx skills@1.7.1` installs a published Skill ZIP into a local Claude Code or Codex skill directory. Copy its fixed-version command from the Skill install page, read the files first, then run it in your target project. `openlinker skills` manages **Core platform resources** and never runs that installer or any local Agent.

```bash
openlinker skills list --query report
openlinker skills get --id PACKAGE_UUID --version VERSION_UUID
openlinker skills download --id PACKAGE_UUID --version VERSION_UUID --digest SHA256 --output skill-bundle.json
openlinker auth login --scopes skill-packages:read,skill-packages:import,skill-bindings:read,skill-bindings:manage
openlinker skills import --id PUBLISHED_PACKAGE_UUID --version PUBLISHED_VERSION_UUID --digest SHA256
openlinker skills bind --agent OWNED_AGENT_UUID --id PRIVATE_PACKAGE_UUID --version PRIVATE_VERSION_UUID
openlinker skills bindings --agent OWNED_AGENT_UUID
openlinker skills unbind --agent OWNED_AGENT_UUID --id PRIVATE_PACKAGE_UUID
```

Use UUIDs and the lowercase SHA-256 from a trusted fixed-version reference. Public list/get/download calls are anonymous, including when a token is configured. Add `--owned` to list/get/download for your private packages. Owned list returns the Core list (up to 200 packages); public discovery supports `--query`, `--page` and `--limit` (1–50). Fixed-version `get` returns metadata; `download` saves canonical bundle JSON rather than installing client files.

Skill permissions are **opt-in** and require the matching new Core/API and authorization UI; existing CLI login defaults and stored grants are unchanged. The login command above requests only four skill scopes and replaces the instance's saved credential, so include your other required scopes when replacing an existing login. `skill-packages:read` includes **all your private skill file contents**; `skill-packages:import` copies published public/unlisted versions into private space. `skill-bindings:read/manage` inspect/change bindings only on owned Agents; a manually created token can restrict them to specific owned Agent UUIDs. Skills cannot be uploaded, published or made public using these scopes.

Download verifies the exact canonical bytes against Core metadata, and `--digest` additionally pins the caller's expected content. A digest from the same server alone does not establish publisher trust. Output must be a new file in an existing directory without symlink ancestors; bytes are published atomically with mode 0600 using a no-overwrite hard link. Existing files/symlinks are rejected; unsupported hard-link filesystems fail without fallback. A ZIP's bytes have a different digest and do not use the bundle digest. Withdrawal makes public download unavailable; imported private copies remain usable. Bindings pin an explicit version and preserve Core's compatibility, owner and lifecycle checks.

## Browser login

```bash
openlinker --api https://your-instance.example auth login
openlinker --api https://your-instance.example auth status
openlinker --api https://your-instance.example auth logout
```

Login opens the system browser. Confirm the account, instance, code and requested
permissions on the website. Core issues a revocable User Token valid for 30 days;
the browser's JWT stays in the browser. Core migration 094 and the matching Web
`/cli/authorize` page are required. This does not log in Claude/Codex, start a
Worker or load Plugin Browser services.

Use `auth login --device-code` over SSH or on a headless machine, and open the
printed URL from another device. `--no-browser` keeps the local callback flow
but lets you open the URL yourself on the same machine. Login prompts go to
stderr; successful stdout remains JSON and never contains the credential.
`--scopes agents:read,runs:read` requests only those permissions; the default also
includes `agents:run`, `runs:cancel` and `tasks:create`. These grants still obey
Core's ownership and visibility checks.

The default store is the OS keyring (macOS Keychain, Windows Credential Manager,
Linux Secret Service). An unavailable keyring fails instead of saving plaintext.
On POSIX systems, `--credential-store=file` explicitly selects a private 0600
plaintext file, suitable for a headless account with a private home directory.
Windows requires the keyring. Metadata lives under the OS user config directory
in `openlinker/auth`; `OPENLINKER_CONFIG_DIR` can select an absolute private
configuration directory. Keep this directory private (0700 on POSIX).

Credential precedence is **`--token` > `OPENLINKER_USER_TOKEN` > saved login for
that exact API instance**. API flags and environment overrides retain their precedence;
commands never launch a browser implicitly. `context` remains offline. Each API
instance has one saved account; use logout before changing that account. A saved
credential is never sent to another instance or through an HTTP redirect.
`https://openlinker.ai` and `https://api.openlinker.ai` have separate saved logins;
use the same API address for login and subsequent commands.

`auth status` checks the effective credential online and prints safe account,
issuer, grants and expiration metadata. `auth logout` revokes the saved token
before removing it; if revocation cannot be confirmed, it retains the local
record for retry. It does not clear or revoke a separate environment/flag token.
Expired/revoked tokens are removed on logout; sign in again afterward. Browser
User Token settings can also revoke a CLI token, including after a lost exchange
response. No automatic refresh or plaintext credential export is provided.

Saved login is optional for existing commands: when no valid saved credential is available, they continue anonymously (with a warning for an unreadable or expired record). `auth status` reports login problems directly. Ctrl-C cancels an in-progress login and removes its lock. SIGKILL or power loss may leave an instance-specific `.lock` file to remove after confirming no login/logout process is active.

If a keyring entry was deleted or its metadata was damaged, first revoke the affected OpenLinker CLI token in website settings. After confirming no login/logout is running, remove only that instance's JSON record under `openlinker/auth` (match its `api` field; if unreadable, the filename is the SHA-256 hex of the normalized API URL). Then sign in again. Unlock a locked keyring before attempting recovery; deleting the local record alone does not revoke a token.

## Configuration

Without an API flag or environment override, the CLI connects to `https://openlinker.ai`.
Precedence is **`--api` > `OPENLINKER_API_BASE` > `OPENLINKER_URL` > `https://openlinker.ai`**.
For a local Core instance, set the address explicitly:

```bash
export OPENLINKER_API_BASE=http://localhost:8080
export OPENLINKER_USER_TOKEN=ol_user_xxx
```

The CLI does not accept the retired `OPENLINKER_TOKEN`,
`OPENLINKER_RUNTIME_TOKEN`, `OPENLINKER_DEMO_JWT`, or `OPENLINKER_API_URL`
aliases. `--token` may be used to provide a User Token explicitly, but the
environment variable is safer for routine use because command-line arguments
may be retained in shell history or exposed in process listings.

Run identifiers may be injected by a surrounding environment for diagnostics:

```bash
export OPENLINKER_RUN_ID=33333333-3333-4333-8333-333333333333
export OPENLINKER_AGENT_ID=22222222-2222-4222-8222-222222222222
export OPENLINKER_TRACE_ID=44444444-4444-4444-8444-444444444444
```

These values are context only. They do not authorize runtime delegation.

## User Token grants

Manage User Tokens outside this CLI, either in Core Web under
`/settings/user-tokens` or through Core's JWT-protected `/api/v1/user-tokens`
API. Give each token only the Core grants needed for the commands it will run:

| Commands | Required grant |
| --- | --- |
| `context` | None; it makes no API request |
| `agents search`, `agents get`, `agents card` | `agents:read` |
| `run` | `agents:run` |
| `runs get`, `runs children`, `runs events`, `runs messages`, `runs artifacts` | `runs:read` |
| `tasks create` | `tasks:create` |
| `runs cancel` | `runs:cancel` |

An `agents:run` grant may be limited to one Agent. Grants do not replace Core's
ownership, visibility, or run-state checks.

## Commands

OpenLinker uses Cobra/pflag syntax. Use double-dash long flags such as `--api`,
`--agent`, and `--input`; single-dash long flags are not supported.

```bash
openlinker --api http://localhost:8080 --timeout 60s context
openlinker --api http://localhost:8080 run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --text "hello"
```

Inspect the configured context, CLI version, surface version, and capabilities
without exposing credentials or making a network request:

```bash
openlinker context
```

Discover Agents:

```bash
openlinker agents search --query "summarization" --callable
openlinker agents get --slug writer-agent
openlinker agents card --slug writer-agent --extended
```

Resolve a private task intent into Skill and Agent recommendations:

```bash
openlinker tasks create \
  --query "summarize a long document" \
  --skill summary
```

Start a top-level run:

```bash
openlinker run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a short summary"}'
```

For long-running work, return immediately with a Run ID and provide a stable
idempotency key that can be reused after a network failure:

```bash
openlinker run --async \
  --idempotency-key request-20260721-001 \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a detailed report"}'
```

Inspect run state and A2A traces that already exist:

```bash
openlinker runs get --id 33333333-3333-4333-8333-333333333333
openlinker runs children --id 33333333-3333-4333-8333-333333333333
openlinker runs events --id 33333333-3333-4333-8333-333333333333
openlinker runs messages --id 33333333-3333-4333-8333-333333333333
openlinker runs artifacts --id 33333333-3333-4333-8333-333333333333
openlinker runs cancel --id 33333333-3333-4333-8333-333333333333
```

`runs children` is backed by `openlinker-go`'s `ListRunChildren` method. The
CLI can inspect child runs but does not create delegated child calls.

## Skill Guidance

Skills may use this CLI for Agent discovery, top-level user-authorized calls,
and run inspection. Provide only `OPENLINKER_USER_TOKEN` with the minimum
required grants. Never expose a User Token in prompts or logs, and never give a
Skill an Agent Token.

Native SDK handlers call another Agent through their assignment-scoped
`RuntimeContext` and must provide an idempotency key. Provider Runtime sessions
remain private; Skills and caller commands use Core conversation IDs instead.

## Project Layout

```text
cmd/openlinker/main.go
pkg/root
pkg/shared
pkg/context
pkg/buildinfo
pkg/run
pkg/tasks/create
pkg/agents/search
pkg/agents/get
pkg/agents/card
pkg/runs/get
pkg/runs/children
pkg/runs/events
pkg/runs/messages
pkg/runs/artifacts
pkg/runs/cancel
```

## Development

```bash
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./cmd/openlinker

cd example/agent-skill
GOWORK=off go test ./...
```

See [CONTRIBUTING.md](./CONTRIBUTING.md) for the full contributor checks. Report
security issues through [SECURITY.md](./SECURITY.md); use
[SUPPORT.md](./SUPPORT.md) for reproducible bugs and feature requests.

## License

Apache-2.0. See [LICENSE](./LICENSE).
