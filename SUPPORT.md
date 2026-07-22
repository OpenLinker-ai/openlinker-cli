# Support

Chinese documentation: [SUPPORT.zh-CN.md](./SUPPORT.zh-CN.md)

Use GitHub issues for reproducible bugs, documentation problems, and feature
requests that fit OpenLinker CLI's open-source scope.

## Good issue topics

- command parsing, flags, input files, and stdin behavior
- JSON stdout, diagnostic stderr, or exit-code behavior
- User Token authentication without sharing the token value
- Agent discovery, top-level run creation, and run inspection
- `agent` / `plugin` commands, Runtime transports, provider sessions, and cancellation
- Codex/Claude production images and egress gateway behavior
- compatibility with a released Core API or `openlinker-go` revision
- bundled Skills, examples, cross-platform binaries, and documentation

## Before opening an issue

- Search existing issues and recent commits.
- Confirm the problem on the latest `main` branch or a named release.
- Include the CLI commit or binary version, operating system, architecture, and
  Go version.
- Include the Core API version or commit and the command being tested.
- Provide reproduction steps, expected behavior, actual behavior, exit code,
  and sanitized stdout/stderr.
- Remove User Tokens, Authorization headers, private URLs, customer input,
  response data, local paths, and `.env` values.

If the original command used `--token`, replace the flag and its value with a
short note such as “User Token supplied” before posting it.

## Not supported here

- vulnerabilities; follow [SECURITY.md](./SECURITY.md)
- Core server storage, scheduling, or registry implementation
- commercial billing, wallet, withdrawal, marketplace, or Hosted dashboard requests
- private deployment debugging without reproducible public details

## Cross-repository questions

For issues that involve CLI and Core or an SDK together, include:

- CLI commit SHA or binary version
- Core API commit SHA or version
- `openlinker-go` version from `go.mod`
- command name and sanitized arguments
- sanitized HTTP status, stdout, and stderr

For Runtime problems, also include the selected transport, provider CLI
version, redacted `agent doctor` output, and whether the failure occurs in
native plugin, foreground, or container mode.
