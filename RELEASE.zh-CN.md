# 发布流程

English documentation: [RELEASE.md](./RELEASE.md)

OpenLinker CLI 从 `main` 发布，前提是 CI 和本地发布门禁都通过。在 CLI 与 Core API
契约足够稳定并采用严格语义化版本之前，重要变化记录在
[CHANGELOG.md](./CHANGELOG.md) 的 `Unreleased` 中。

## 发布前检查

1. 确认 `README.md` 和 `README.zh-CN.md` 对命令、User Token grant 和 Agent Node
   边界的描述一致。
2. 确认 `CONTRIBUTING`、`SECURITY`、`SUPPORT`、`RELEASE`、自带 Skill 和示例是
   最新的；有中文版的文档必须同步更新。
3. 确认 `CHANGELOG.md` 描述了命令、SDK、凭据边界和打包变化，并明确列出所有
   breaking change。
4. 运行格式检查：

   ```bash
   test -z "$(find . -path ./.git -prune -o -name '*.go' -print | xargs gofmt -l)"
   ```

5. 运行根模块检查：

   ```bash
   GOWORK=off go test ./...
   GOWORK=off go test -race ./...
   GOWORK=off go vet ./...
   ```

6. 运行示例子模块检查：

   ```bash
   (cd example/agent-skill && GOWORK=off go test ./...)
   (cd example/agent-skill && GOWORK=off go test -race ./...)
   (cd example/agent-skill && GOWORK=off go vet ./...)
   ```

7. 在仓库外构建，并扫描当前源码中的 secret：

   ```bash
   tmpdir="$(mktemp -d)"
   GOWORK=off go build -trimpath -ldflags="-s -w" -o "$tmpdir/openlinker" ./cmd/openlinker
   rm -rf "$tmpdir"
   gitleaks dir --redact .
   ```

8. 确认 release matrix 为 Linux、macOS 和 Windows 构建 amd64、arm64 二进制，并为
   每个 archive 发布 SHA-256 checksum。
9. 抽查 archive，确认其中包含对应平台的二进制、`README.md`、
   `README.zh-CN.md`、`LICENSE` 和 `skills/` 目录。
10. 确认生成的 archive、本地二进制、coverage 输出、`.env` 和
    `example/agent-skill/skills/openlinker-cli/scripts/openlinker` 没有被跟踪。
11. 确认文档、测试、fixture 和 release 输出中的 token 都是占位值。User Token、
    Agent Token、Authorization header、私有 URL、客户输入或响应 payload 都不能进入
    release。

## 打 tag

维护者发布版本化二进制时使用语义化版本 tag：

```bash
git tag v0.x.y
git push origin v0.x.y
```

pre-1.0 版本可以包含 breaking change，但必须在 `CHANGELOG.md` 中说明。
