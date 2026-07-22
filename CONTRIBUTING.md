# Contributing to OpenLinker CLI

Chinese documentation: [CONTRIBUTING.zh-CN.md](./CONTRIBUTING.zh-CN.md)

Thanks for helping improve OpenLinker CLI. This repository owns the JSON-first
caller client, native plugin bridge, reliable Runtime Worker, provider
adapters, and hardened production images.

## Development setup

The root module contains the CLI. `example/agent-skill` is a separate Go module
and must be checked separately.

```bash
GOWORK=off go test ./...
GOWORK=off go build ./cmd/openlinker

cd example/agent-skill
GOWORK=off go test ./...
```

Use placeholder User Tokens in tests and documentation. Never commit a real
User Token, Agent Token, private endpoint, local `.env`, customer input, or
response payload containing sensitive data.

## Scope boundaries

Changes that belong here include:

- Cobra commands and the JSON stdout / diagnostic stderr contract
- User Token authentication for user-authorized Core API calls
- Agent discovery, top-level run creation, and run inspection
- `openlinker-go` integration
- Agent Mode, token-only Runtime transport, and provider session adapters
- local stdio MCP bridge and native plugin control tools
- production entrypoint, provider isolation, and egress gateway
- bundled Skills, examples, packaging, and CLI documentation

Changes that do not belong here include:

- delegated child-run creation from an executing Agent
- Core registry storage, server-side scheduling, or Hosted billing, wallet,
  marketplace, and dashboard behavior

## CLI rules

- Caller commands accept only `OPENLINKER_USER_TOKEN` or the explicit `--token`
  User Token flag; Runtime commands accept only their isolated Agent/provider
  credential sources.
- Prefer the environment variable in examples because command-line token values
  may enter shell history or process listings.
- Never print credentials to stdout or stderr.
- Keep successful stdout machine-readable JSON. Send diagnostics to stderr.
- Keep `OPENLINKER_RUN_ID`, `OPENLINKER_AGENT_ID`, and
  `OPENLINKER_TRACE_ID` as context only; they do not authorize calls.
- Do not restore `delegate`, `--runtime-token`, legacy credential aliases, or
  the retired runtime call-agent route. Agent-side child calls use SDK
  `RuntimeContext`, not caller credentials.
- Describe User Token authorization as grants. Protocol fields that are
  explicitly named `scopes` may keep their protocol-defined name.

## Pull request expectations

- Add tests for command parsing, HTTP method/path/auth behavior, and JSON output.
- Cover credential redaction and rejected legacy/runtime interfaces when those
  boundaries change.
- Update English and Chinese documentation together.
- Explain Core API or `openlinker-go` compatibility impact.
- Redact tokens, private URLs, local paths, customer input, and response bodies
  from fixtures and logs.

## Checks

```bash
test -z "$(find . -path ./.git -prune -o -name '*.go' -print | xargs gofmt -l)"
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...

(cd example/agent-skill && GOWORK=off go test ./...)
(cd example/agent-skill && GOWORK=off go test -race ./...)
(cd example/agent-skill && GOWORK=off go vet ./...)

tmpdir="$(mktemp -d)"
GOWORK=off go build -trimpath -o "$tmpdir/openlinker" ./cmd/openlinker
rm -rf "$tmpdir"
```

## Security

Do not open public issues for vulnerabilities. Follow
[SECURITY.md](./SECURITY.md).

## License

By contributing, you agree that your contribution is licensed under the
Apache-2.0 license used by this repository.
