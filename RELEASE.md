# Release Process

Chinese documentation: [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)

OpenLinker CLI releases are cut from `main` after CI and local release gates
pass. Until the CLI and Core API contract are stable enough for strict semantic
versioning, record notable changes under `Unreleased` in
[CHANGELOG.md](./CHANGELOG.md).

## Pre-release checklist

1. Confirm `README.md` and `README.zh-CN.md` describe the same commands, User
   Token grants, and Agent Node boundary.
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
