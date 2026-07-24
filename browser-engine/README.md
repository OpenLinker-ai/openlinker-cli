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
versions. A trusted Runtime Agent assigns the authoritative lease and exposes
the Browser MCP tool to the ordinary Codex or Claude Code client. Browser
execution never uses a Provider-native computer-use API.

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

可信 Runtime Agent 分配权威 lease，并把 Browser MCP 工具暴露给普通 Codex 或
Claude Code 客户端；浏览器执行不使用 Provider 原生 computer-use API。
