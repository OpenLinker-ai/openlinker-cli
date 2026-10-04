# OpenLinker CLI

English documentation: [README.md](./README.md)

CLI 是平台命令行客户端：通过公开 Core API 和 `openlinker-go` 查询 Agent、创建任务、发起运行和查看结果。
使用 User Token，stdout 输出 JSON，诊断写入 stderr。

本地 Codex／Claude 接入使用 [Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node)。
原生 MCP、Agent Mode 和浏览器执行由 [Plugin](https://github.com/OpenLinker-ai/openlinker-plugin) 提供。
CLI 不包含本地 Worker、Provider 适配器、浏览器服务或执行转发命令。
升级旧版一体化 CLI 前请阅读 [迁移说明](./MIGRATION.md)。

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

## 浏览器登录

```bash
openlinker --api https://your-instance.example auth login
openlinker --api https://your-instance.example auth status
openlinker --api https://your-instance.example auth logout
```

需要 Core migration 094 和包含 `/cli/authorize` 的配套网页。登录会打开系统浏览器；
核对账号、实例、终端验证码和权限后，Core 签发有效期 30 天、可撤销的 User Token。
网页登录 JWT 留在网页，不接入本地 Claude/Codex，不启动 Worker 或 Plugin Browser。

SSH/无浏览器环境使用 `auth login --device-code`，在另一台设备打开终端显示的链接。
`--no-browser` 保留本机回调，仅取消自动打开浏览器，需要在同一台电脑打开链接。
提示输出到 stderr，成功结果在 stdout 输出 JSON，均不打印凭据。
`--scopes agents:read,runs:read` 可缩小申请权限；默认另外包含 `agents:run`、
`runs:cancel`、`tasks:create`，仍受 Core 所有权与可见性检查约束。

默认保存在系统钥匙串：macOS Keychain、Windows Credential Manager 或 Linux Secret
Service。钥匙串不可用时不会自动降级。POSIX 用户可以显式使用
`--credential-store=file` 保存为私有 0600 明文文件；Windows 必须使用钥匙串。
元数据位于系统用户配置目录的 `openlinker/auth`，可用绝对路径
`OPENLINKER_CONFIG_DIR` 指定私有目录（POSIX 要求 0700）。

优先级：**`--token` > `OPENLINKER_USER_TOKEN` > 当前 API 实例保存的登录凭据**。
现有 API 默认值和脚本用法保持不变，普通调用不会隐式打开浏览器；`context` 仍离线。
一个 API 实例保存一个账号，切换账号先退出登录。不会向其他实例或 HTTP 重定向转发
保存的凭据。`auth status` 在线验证实际凭据，只显示账号、签发实例、权限和有效期。

`auth logout` 先撤销本实例保存的 Token，再移除本地记录；网络失败无法确认撤销时保留
记录供重试。它不会清除或撤销另外通过环境变量/参数传入的 Token。已过期或撤销的
凭据可直接退出后重新登录。也可在网页 User Token 设置页撤销，包括兑换响应丢失时
已签发的 Token。首版没有自动刷新或明文导出功能。

现有命令不强制要求保存登录：无有效凭据时仍可匿名调用；记录过期或不可读会输出警告。`auth status` 直接报告登录问题。Ctrl-C 会取消登录并清理锁；SIGKILL 或断电可能遗留对应实例的 `.lock` 文件，确认没有登录/退出进程运行后再删除。

如果钥匙串条目被删除或元数据损坏，先到网页撤销对应的 OpenLinker CLI Token。确认没有登录/退出进程后，只删除 `openlinker/auth` 中该实例的 JSON 记录（通过 `api` 字段匹配；记录不可读时，文件名为规范化 API URL 的 SHA-256 十六进制值），然后重新登录。钥匙串只是锁定时应先解锁；仅删除本地记录不会撤销远端 Token。

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

User Token 的通用管理仍在网页或 API 进行，可在 Core Web 的 `/settings/user-tokens`，或通过
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
提供幂等 key。Provider Runtime session 始终私有；Skill 和调用方命令只使用 Core
conversation ID。

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
