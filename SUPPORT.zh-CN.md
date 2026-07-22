# 支持

English documentation: [SUPPORT.md](./SUPPORT.md)

可用 GitHub Issues 报告可复现 bug、文档问题，以及符合 OpenLinker CLI 开源范围的功能
建议。

## 适合提交 Issue 的内容

- 命令解析、参数、input file 和 stdin 行为
- JSON stdout、诊断 stderr 或 exit code 行为
- User Token 鉴权问题，但不要提供 token 值
- Agent 发现、顶层 Run 创建和 Run 查看
- `agent` / `plugin` 命令、Runtime transport、Provider session 与取消
- Codex/Claude 生产镜像和 egress gateway 行为
- 与已发布 Core API 或 `openlinker-go` revision 的兼容性
- 自带 Skill、示例、跨平台二进制和文档

## 提交前请确认

- 搜索已有 Issue 和近期 commit。
- 在最新 `main` 或指定 release 上确认问题。
- 提供 CLI commit 或二进制版本、操作系统、架构和 Go 版本。
- 提供 Core API 版本或 commit，以及正在测试的命令。
- 提供复现步骤、期望行为、实际行为、exit code 和脱敏后的 stdout/stderr。
- 删除 User Token、Authorization header、私有 URL、客户输入、响应数据、本地路径和
  `.env` 值。

如果原命令使用了 `--token`，发布前请删掉该 flag 和 token 值，只注明“已提供 User
Token”。

## 不在这里处理

- 安全漏洞；请看 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md)
- Core 服务端存储、调度或 registry 实现
- 商业计费、钱包、提现、市场或 Hosted Dashboard
- 无法公开复现的私有部署调试

## 跨仓库问题

涉及 CLI 与 Core 或 SDK 的问题请包含：

- CLI commit SHA 或二进制版本
- Core API commit SHA 或版本
- `go.mod` 中的 `openlinker-go` 版本
- 命令名和脱敏后的参数
- 脱敏后的 HTTP status、stdout 和 stderr

Runtime 问题还应提供所选 transport、Provider CLI 版本、脱敏后的 `agent doctor` 输出，
以及问题发生在原生插件、前台进程还是容器模式。
