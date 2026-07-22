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
- User Token、Agent Token 与 Provider 凭据隔离
- Runtime transport 恢复、token-only discovery 策略、lease 与可靠交付
- Provider 子进程隔离、私有 session 状态和取消
- 生产 entrypoint、只读镜像、secret file 与 egress 过滤

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

本仓 Runtime Worker、Agent Token、Provider Adapter、生产镜像和 egress gateway 的漏洞
应在这里报告。兼容的旧 Adapter 仍在其自身仓库处理；如有跨仓影响请明确注明。

## 披露

维护者会尽快处理报告。在修复、缓解方案或协调披露时间线确定前，请勿公开披露。
