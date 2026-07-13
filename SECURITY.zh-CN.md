# 安全策略

English documentation: [SECURITY.md](./SECURITY.md)

不要用公开 Issue 报告安全漏洞。

优先使用 GitHub 私密漏洞报告。如果不可用，请通过 OpenLinker 公布的安全或支持渠道联系
维护者。报告中请包含受影响仓库、commit 或 release、复现步骤、影响范围，以及是否涉及
真实凭据、私有 endpoint 或客户数据。

## 支持版本

OpenLinker CLI 目前是 pre-1.0。安全修复面向当前 `main` 分支，以及可用时的最新
release tag。除非维护者明确公告，否则旧 commit 不承诺 backport。

## 敏感区域

- 从 `OPENLINKER_USER_TOKEN` 和 `--token` 读取 User Token
- Authorization header 构造和凭据脱敏
- stdout/stderr 分离和错误处理
- API base URL、HTTP transport、TLS 和 redirect 行为
- 从参数、文件或 stdin 读取输入
- JSON 请求和响应处理
- 自带 Skill 的执行与环境变量转发
- release archive 和示例构建产物
- 拒绝 Agent Token、runtime 凭据和已退役别名

日常使用优先选择环境变量。通过 `--token` 传入的 token 可能保留在 shell history
或出现在进程列表中。不要把真实 token 放进 Issue、终端记录、截图或测试。

## 报告建议

请提供：

- 受影响 commit、tag 或二进制版本
- 操作系统、架构和 Go 版本
- 命令名称及脱敏后的参数
- Core API 版本或 commit，以及相关 `openlinker-go` 版本
- 最小复现和脱敏后的 stdout、stderr
- 是否暴露 User Token、私有 URL、客户输入或响应数据

请完整删除 Authorization header 和 token 值；部分打码不能替代凭据撤销。如果凭据已
暴露，请先撤销或替换，再分享细节。

涉及 Agent Node、Agent Runtime WebSocket/长轮询、mTLS、Agent Token、lease 或持久化执行
的漏洞，应提交到
[OpenLinker Agent Node](https://github.com/OpenLinker-ai/openlinker-agent-node)；
如果同时影响 CLI，请注明跨仓影响。

## 披露

维护者会尽快处理报告。在修复、缓解方案或协调披露时间线确定前，请勿公开披露。
