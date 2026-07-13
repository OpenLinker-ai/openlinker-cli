# 贡献 OpenLinker CLI

English documentation: [CONTRIBUTING.md](./CONTRIBUTING.md)

感谢你改进 OpenLinker CLI。本仓库维护 OpenLinker 的用户/API 命令行客户端：标准输出
固定为 JSON，能力集中在发现 Agent、启动顶层 Run 和查看 Run。

## 开发环境

仓库根目录是 CLI 主模块；`example/agent-skill` 是独立 Go module，需要单独检查。

```bash
GOWORK=off go test ./...
GOWORK=off go build ./cmd/openlinker

cd example/agent-skill
GOWORK=off go test ./...
```

测试和文档只能使用占位 User Token。不要提交真实 User Token、Agent Token、私有
endpoint、本地 `.env`、客户输入，或包含敏感数据的响应 payload。

## 范围边界

适合放在这里：

- Cobra 命令与 JSON stdout / 诊断 stderr 契约
- 用于用户授权 Core API 调用的 User Token 鉴权
- Agent 发现、顶层 Run 创建和 Run 查看
- `openlinker-go` 集成
- CLI 自带的 Skill、示例、打包和文档

不适合放在这里：

- Agent Token 处理或 Agent 凭据生命周期
- Agent Runtime WebSocket/长轮询 session、mTLS、lease、resume、持久化 spool、
  取消或执行 adapter
- 正在执行的 Agent 创建 child Run
- Core registry 存储、服务端调度，以及 Hosted 计费、钱包、市场和 Dashboard

Agent 侧执行与子调用属于
[OpenLinker Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node)。

## CLI 规则

- 只接受 `OPENLINKER_USER_TOKEN`，或通过 `--token` 显式提供 User Token。
- 示例优先使用环境变量，因为命令行里的 token 可能进入 shell history 或进程列表。
- stdout 和 stderr 都不能打印凭据。
- 成功响应的 stdout 必须保持为机器可读 JSON；诊断信息写入 stderr。
- `OPENLINKER_RUN_ID`、`OPENLINKER_AGENT_ID` 和
  `OPENLINKER_TRACE_ID` 只是上下文，不提供调用权限。
- 不要恢复 `delegate`、`--runtime-token`、旧凭据别名或已退役的 runtime
  call-agent 路由。
- User Token 授权统一称为 grant。只有协议字段本身明确命名为 `scopes` 时才保留该名称。

## PR 要求

- 命令解析、HTTP method/path/auth 行为和 JSON 输出变化需要测试。
- 凭据脱敏或 runtime 边界变化时，要覆盖旧接口被拒绝的行为。
- 英文和中文文档一起更新。
- 说明对 Core API 或 `openlinker-go` 的兼容性影响。
- fixture 和日志中不得出现 token、私有 URL、本地路径、客户输入和响应正文。

## 检查

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

## 安全

不要公开提交漏洞 Issue。请按照 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md) 处理。

## 许可证

贡献即表示你同意贡献内容使用本仓库的 Apache-2.0 许可证。
