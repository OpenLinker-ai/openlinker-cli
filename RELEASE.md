# Release Process

Chinese documentation: [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)

OpenLinker CLI releases are cut from `main` after CI and local release gates
pass. Until the CLI and Core API contract are stable enough for strict semantic
versioning, record notable changes under `Unreleased` in
[CHANGELOG.md](./CHANGELOG.md).

## Cross-repository release order

Verify the immutable Go SDK module first. Pin that exact version and its public
module checksums in CLI, then test/build all six CLI targets with `GOWORK=off`
before releasing. CLI module and package graphs must exclude Node and Plugin.
After verifying the published CLI archives, update Plugin's independent caller
CLI archive/checksum locks. Plugin builds and releases its own execution host;
its host and Provider images have separate source and artifact checks.
Relative `replace`, unpublished candidates, and temporary local proxy validation
are not public release evidence. Keep prior immutable artifacts for rollback;
source releases do not automatically deploy or modify runtime state.

## Pre-release checklist

1. Confirm `README.md` and `README.zh-CN.md` describe the same caller, native
   plugin, Runtime Worker, provider-session, and production-image boundaries.
2. Confirm `CONTRIBUTING`, `SECURITY`, `SUPPORT`, `RELEASE`, bundled Skills,
   and examples are current in both languages where a translation exists.
3. Confirm `CHANGELOG.md` describes command, SDK, credential-boundary, and
   packaging changes, including every breaking change.
4. Run the format check:

   ```bash
   test -z "$(find . -path ./.git -prune -o -name '*.go' -print | xargs gofmt -l)"
   ```

5. Run root-module checks:

   ```bash
   GOWORK=off go test ./...
   GOWORK=off go test -race ./...
   GOWORK=off go vet ./...
   ```

6. Run nested example-module checks:

   ```bash
   (cd example/agent-skill && GOWORK=off go test ./...)
   (cd example/agent-skill && GOWORK=off go test -race ./...)
   (cd example/agent-skill && GOWORK=off go vet ./...)
   ```

7. Build outside the repository and run a current-source secret scan:

   ```bash
   tmpdir="$(mktemp -d)"
   GOWORK=off go build -trimpath -ldflags="-s -w" -o "$tmpdir/openlinker" ./cmd/openlinker
   rm -rf "$tmpdir"
   gitleaks dir --redact .
   ```

8. Confirm the release matrix builds Linux, macOS, and Windows binaries for
   amd64 and arm64, and publishes a SHA-256 checksum for every archive.
9. Inspect an archive and confirm it contains the platform binary,
   `README.md`, `README.zh-CN.md`, `LICENSE`, and the `skills/` directory.
10. Confirm generated archives, local binaries, coverage output, `.env` files,
    and `example/agent-skill/skills/openlinker-cli/scripts/openlinker` are not
    tracked.
11. Confirm every token in documentation, tests, fixtures, and release output
    is a placeholder. No User Token, Agent Token, Authorization header, private
    URL, customer input, or response payload may enter a release.

## Tagging

Use semantic version tags when maintainers publish versioned binaries:

```bash
git tag v0.x.y
git push origin v0.x.y
```

Pre-1.0 releases may include breaking changes, but they must be called out in
`CHANGELOG.md`.
