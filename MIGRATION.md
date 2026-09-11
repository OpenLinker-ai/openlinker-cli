# CLI execution boundary migration

The platform CLI now contains only `context`, `agents`, `tasks`, `run`, and `runs`.
The `agent` and `plugin` command groups and their advertised capabilities are removed.
There is no forwarding, automatic download, or local Worker in the new CLI.
Existing caller commands, User Token grants, JSON output and Core API behavior remain compatible.

| Previous entry | Replacement |
| --- | --- |
| Ordinary local Codex / Claude bridging | Agent Node native adapters; follow its configuration and enrollment guide |
| `openlinker agent configure/serve/status/doctor` for a deep Plugin Agent | `openlinker-plugin-host agent configure/serve/status/doctor` from the Plugin release |
| `openlinker plugin serve --host codex/claude` | `openlinker-plugin-host plugin serve --host codex/claude` |
| Browser and delegation proxy commands | Plugin host private commands, selected by Plugin-owned manifests |
| Platform Skills and caller commands | `openlinker` |

Install the matching Plugin package before replacing the legacy all-in-one CLI in
an existing native Plugin installation. Old packages whose launcher still resolves
`openlinker` require their old pinned CLI; upgrading that CLI alone breaks their
execution entry. New Plugin packages provide an explicit `setup-plugin-host` installer for the pinned
Linux/macOS/Windows amd64 or arm64 release, verify its SHA-256 and capability metadata,
and never fall back to CLI. Complete host installation before restarting native MCP.

The Plugin host consumes the existing v1 Plugin configuration, Agent/Node identity,
credentials, session and state directories. Stop/drain the old Worker before
starting the host against the same state. This is an executable change, not a state
reset. Moving a deployment to Agent Node is a separate configuration migration;
do not assume its defaults or paths equal Plugin's. Keep the previous executable
for rollback and never run two Workers against the same state directory.
