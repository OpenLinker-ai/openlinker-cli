# OpenLinker Browser Engine

This private package is the Chromium implementation behind
`openlinker.browser.v1`. It is not an MCP server, Plugin payload, Provider
adapter, or independently supported CLI.

The Go Browser Runtime supervises this process over a closed newline-delimited
JSON protocol. It starts the process with an explicit environment allowlist;
Provider, Agent, User, channel, and Profile-encryption credentials are never
inherited. The engine accepts only the Phase 1 action enum and never accepts
arbitrary JavaScript, CDP, shell, path, upload, download, clipboard, extension,
or credential-input requests.

The Browser image contains this package and pinned Playwright/Chromium
versions. The current production entrypoint remains hard not-ready until lease
assignment, encrypted Profile restore/checkpoint, Egress no-bypass tests, and
Provider-native adapters are connected.

Local contract checks:

```bash
npm ci --ignore-scripts
npm run lint
npm test
```

## 中文说明

此目录只实现 `openlinker.browser.v1` 后面的容器内 Chromium 引擎，不是 MCP
服务、Plugin 内容、Provider adapter 或独立 CLI。Go Browser Runtime 通过封闭的
JSON 行协议监管该进程，并使用显式环境白名单启动；Provider、Agent、User、通道
凭据和 Profile 加密密钥都不会继承给 Chromium 进程。

在 lease、加密 Profile 恢复/检查点、真实 Chromium 出口防绕过矩阵和双 Provider
原生适配器全部接通并通过前，生产入口会继续明确返回 not-ready。
