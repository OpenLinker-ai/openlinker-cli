# OpenLinker CLI

English documentation: [README.md](./README.md)

OpenLinker CLI 是一个小型命令行工具，供用户或 API 调用方发现并运行 OpenLinker
Agent。它的标准输出固定为 JSON，只对 `openlinker-go` 做一层轻量封装：

- stdout 始终输出 JSON；
- 诊断信息和错误写入 stderr；
- CLI 只接受 OpenLinker User Token，并且不会打印它；
- 每个子命令的实现分别放在 `pkg/` 下。

CLI 不是 Agent runtime。Runtime 连接、WebSocket/长轮询切换、持久化执行、取消和 Agent
子调用属于官方 SDK Runtime Worker。原生 handler 使用 SDK `RuntimeContext`；
[OpenLinker Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node)
只为既有 HTTP、command、Codex、A2A backend 提供临时 Adapter，并向这些 backend 注入
本次 Run 专用的 localhost helper。不要把长效 Agent Token 交给 CLI 或业务 Agent 进程。

CLI 只调用自托管或 Hosted 部署中的 Core 公共契约，不调用 Hosted 的服务商品、订单、钱包、
计费或市场运营 API。

## 状态与安装

CLI 目前是 pre-1.0，并跟随 Core API 契约演进。升级前请固定 release，并阅读
`CHANGELOG.md`。

Linux、macOS、Windows 压缩包及相邻的 `.sha256` 文件发布在
[GitHub Releases](https://github.com/OpenLinker-ai/openlinker-cli/releases)。校验 checksum
后再把 `openlinker` 放入 `PATH`。Go 用户也可以直接安装固定 release：

```bash
go install github.com/OpenLinker-ai/openlinker-cli/cmd/openlinker@v0.x.y
```

请把 `v0.x.y` 替换成实际选择的 release。

## 配置

```bash
export OPENLINKER_API_BASE=http://localhost:8080
export OPENLINKER_USER_TOKEN=ol_user_xxx
```

CLI 不接受已退役的 `OPENLINKER_TOKEN`、`OPENLINKER_RUNTIME_TOKEN`、
`OPENLINKER_DEMO_JWT` 和 `OPENLINKER_API_URL` 别名。也可以通过 `--token`
显式提供 User Token，但日常使用更推荐环境变量，因为命令行参数可能进入 shell history
或暴露在进程列表中。

外围环境可以注入以下标识，用于诊断：

```bash
export OPENLINKER_RUN_ID=33333333-3333-4333-8333-333333333333
export OPENLINKER_AGENT_ID=22222222-2222-4222-8222-222222222222
export OPENLINKER_TRACE_ID=44444444-4444-4444-8444-444444444444
```

这些值只是上下文，不提供 runtime 子调用权限。

## User Token 权限

User Token 的创建和管理不属于 CLI，可在 Core Web 的 `/settings/user-tokens`，或通过
Core 受 JWT 保护的 `/api/v1/user-tokens` API 完成。每枚 Token 只应获得目标命令所需的
Core grant：

| 命令 | 所需 grant |
| --- | --- |
| `context` | 无；该命令不发送 API 请求 |
| `agents search`、`agents get`、`agents card` | `agents:read` |
| `run` | `agents:run` |
| `runs get`、`runs children`、`runs events`、`runs messages`、`runs artifacts` | `runs:read` |
| `tasks create` | `tasks:create` |
| `runs cancel` | `runs:cancel` |

`agents:run` grant 可以收窄到单个 Agent。grant 不会跳过 Core 的所有权、可见性或
Run 状态检查。

## 命令

OpenLinker 使用 Cobra/pflag 语法。`--api`、`--agent`、`--input` 等长参数必须使用
双横线；不支持单横线长参数。

```bash
openlinker --api http://localhost:8080 --timeout 60s context
openlinker --api http://localhost:8080 run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --text "hello"
```

查看当前上下文、CLI 版本、surface 版本和 capability；该命令不联网，也不暴露凭据：

```bash
openlinker context
```

发现 Agent：

```bash
openlinker agents search --query "summarization" --callable
openlinker agents get --slug writer-agent
openlinker agents card --slug writer-agent --extended
```

把私有任务意图解析为 Skill 和 Agent 推荐：

```bash
openlinker tasks create \
  --query "总结一份长文档" \
  --skill summary
```

启动顶层 Run：

```bash
openlinker run \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a short summary"}'
```

长任务可立即返回 Run ID，并提供网络失败后可复用的稳定幂等键：

```bash
openlinker run --async \
  --idempotency-key request-20260721-001 \
  --agent 22222222-2222-4222-8222-222222222222 \
  --input '{"task":"write a detailed report"}'
```

查看已有 Run 状态和 A2A 轨迹：

```bash
openlinker runs get --id 33333333-3333-4333-8333-333333333333
openlinker runs children --id 33333333-3333-4333-8333-333333333333
openlinker runs events --id 33333333-3333-4333-8333-333333333333
openlinker runs messages --id 33333333-3333-4333-8333-333333333333
openlinker runs artifacts --id 33333333-3333-4333-8333-333333333333
openlinker runs cancel --id 33333333-3333-4333-8333-333333333333
```

`runs children` 调用 `openlinker-go` 的 `ListRunChildren`。CLI 可以查看 child
Run，但不会创建 Agent 子调用。

## Skill 使用说明

Skill 可以通过该 CLI 发现 Agent、启动用户授权的顶层调用，以及查看 Run。只提供带最小
grant 的 `OPENLINKER_USER_TOKEN`。不要把 User Token 放进 prompt 或日志，也不要把
Agent Token 交给 Skill。

原生 SDK handler 通过当前 assignment 的 `RuntimeContext` 调用另一个 Agent，并且必须
提供幂等 key。只有运行在 Agent Node Adapter 后面的既有 backend 才使用它注入的本次
Run 专用 localhost helper；helper 的 URL、Authorization header 和幂等规则以 Agent Node
文档为准。

## 项目结构

```text
cmd/openlinker/main.go
pkg/root
pkg/shared
pkg/context
pkg/buildinfo
pkg/run
pkg/tasks/create
pkg/agents/search
pkg/agents/get
pkg/agents/card
pkg/runs/get
pkg/runs/children
pkg/runs/events
pkg/runs/messages
pkg/runs/artifacts
pkg/runs/cancel
```

## 开发

```bash
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go build ./cmd/openlinker

cd example/agent-skill
GOWORK=off go test ./...
```

完整贡献检查见 [CONTRIBUTING.zh-CN.md](./CONTRIBUTING.zh-CN.md)。安全问题请按照
[SECURITY.zh-CN.md](./SECURITY.zh-CN.md) 提交；可复现 bug 和功能建议见
[SUPPORT.zh-CN.md](./SUPPORT.zh-CN.md)。

## 许可证

Apache-2.0。详见 [LICENSE](./LICENSE)。
