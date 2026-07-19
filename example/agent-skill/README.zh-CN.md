# OpenLinker Skill Agent 示例

[English](README.md)

这个示例演示 Blades Agent 如何通过 Skill 调用 OpenLinker CLI。仓库不会提交编译后的
`openlinker` 文件，请先在本机编译。

## 编译 CLI

在仓库根目录运行：

```bash
mkdir -p ./example/agent-skill/skills/openlinker-cli/scripts
go build -o ./example/agent-skill/skills/openlinker-cli/scripts/openlinker ./cmd/openlinker
chmod +x ./example/agent-skill/skills/openlinker-cli/scripts/openlinker
```

生成的文件只供本机使用，不要提交到 Git。

## 运行

```bash
cd example/agent-skill

OPENAI_API_KEY=sk-xxx \
OPENLINKER_API_BASE=https://api.openlinker.ai \
OPENLINKER_USER_TOKEN=ol_user_xxx \
go run .
```

外部环境还可以注入下面这些运行标识，方便排查问题：

```bash
OPENLINKER_AGENT_ID=agent_xxx
OPENLINKER_RUN_ID=run_xxx
OPENLINKER_TRACE_ID=trace_xxx
```

Skill 会让 Blades 通过 `run_skill_script` 执行 `scripts/openlinker`。CLI 使用 User
Token 发现 Agent 和发起顶层运行。真实 Token 不能写进 `SKILL.md`、提示词、日志或
Git。

CLI 不负责发起子 Agent 调用。运行在 Agent Node 后面的旧后端需要调用其他 Agent 时，应
使用 Agent Node 为当前任务提供的本地 helper，不能把长期 Agent Token 交给业务 Agent。

## 在其他 Agent 项目里使用

把 `skills/openlinker-cli` 复制到 Agent 项目中，再为目标机器编译
`cmd/openlinker`，把可执行文件放到：

```text
skills/openlinker-cli/scripts/openlinker
```

最后让 Agent 框架加载这个 Skill，并在运行时提供同样的 OpenLinker 环境变量。
