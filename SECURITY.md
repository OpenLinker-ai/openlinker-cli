# Security Policy

Chinese documentation: [SECURITY.zh-CN.md](./SECURITY.zh-CN.md)

Do not open public issues for vulnerabilities.

Use GitHub private vulnerability reporting when available. If it is not
available, contact the maintainers through the published OpenLinker
security/support channel. Include the affected repository, commit or release,
reproduction steps, impact, and whether any live credential, private endpoint,
or customer data is involved.

## Supported versions

OpenLinker CLI is pre-1.0. Security fixes target the current `main` branch and
the latest tagged release when tags are available. Older commits may not
receive backports unless maintainers explicitly announce support for a release
line.

## Security-sensitive areas

- User Token loading from `OPENLINKER_USER_TOKEN` and `--token`
- Authorization header construction and credential redaction
- stdout/stderr separation and error handling
- API base URL, HTTP transport, TLS, and redirect behavior
- input read from arguments, files, or stdin
- JSON request and response handling
- bundled Skill execution and environment forwarding
- release archives and example build artifacts
- rejection of Agent Tokens, runtime credentials, and retired aliases

The environment variable is preferred for routine use. A token passed through
`--token` may be retained in shell history or visible in a process listing.
Never paste a real token into an issue, transcript, screenshot, or test.

## Reporting guidance

Please include:

- the affected commit, tag, or binary version
- operating system, architecture, and Go version
- the command name and sanitized arguments
- the Core API version or commit and relevant `openlinker-go` version
- a minimal reproduction with sanitized stdout and stderr
- whether a User Token, private URL, customer input, or response data was exposed

Remove Authorization headers and token values completely; partial masking is
not a substitute for revocation. If a credential was exposed, revoke or
replace it before sharing details.

Agent Node Runtime v2, WebSocket/Pull, mTLS, Agent Token, lease, or durable
execution vulnerabilities should be reported against
[OpenLinker Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node),
with cross-repository impact noted when relevant.

## Disclosure

Maintainers will triage reports as quickly as practical. Please avoid public
disclosure until a fix, mitigation, or coordinated disclosure timeline is
available.
