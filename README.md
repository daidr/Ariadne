# Ariadne

一个自己使用的 AI 协作工具：你和协调器对话，协调器把需求拆成计划图并安排 threads 去调研、实现、修复，你随时能看到每件事在做什么、在等什么。

设计取舍见 [设计哲学](./docs/philosophy.md)，领域概念与分层见 [架构](./docs/architecture.md)。

> 早期开发中，目前只能在本机浏览器里使用。

## 开发

需要 [Bun](https://bun.sh) 1.4 及以上。

```sh
bun install
bun run dev      # 同时启动本地服务（127.0.0.1:7428）和前端开发服务器（localhost:5173）
bun run check    # 格式、lint、类型检查和测试
```

本地服务第一次启动时会在 `~/.ariadne/token` 生成访问令牌，并在终端打印带令牌的链接。开发时把链接里的 `#token=…` 接到 `http://localhost:5173/` 后面打开即可；`bun run build` 之后，本地服务会直接提供构建好的页面。

打开页面后，在右上角的“设置”里添加一个模型提供方（OpenAI 兼容接口或 Anthropic 接口），填好地址、API Key 和默认模型名，就可以和协调器对话了。设置保存在 `~/.ariadne/config.json`。

要让 thread 读写代码、运行命令，在右侧“工作目录”里填一个本机目录。git 仓库会为每个 thread 建一个独立的 worktree（放在 `~/.ariadne/plugins/local/worktrees/` 下）和分支 `ariadne/…`，thread 的提交留在这个分支上，合并由你决定。

| 环境变量        | 作用                   | 默认值       |
| --------------- | ---------------------- | ------------ |
| `ARIADNE_HOME`  | 数据库与令牌所在的目录 | `~/.ariadne` |
| `ARIADNE_PORT`  | 本地服务的端口         | `7428`       |
| `ARIADNE_TOKEN` | 指定访问令牌，不读文件 | —            |

## 包

| 包                                  | 内容                                         |
| ----------------------------------- | -------------------------------------------- |
| `@ariadne/i18n`                     | 文案与翻译，服务端和前端共用                 |
| `@ariadne/core`                     | 领域模型、SQLite 事件存储与投影              |
| `@ariadne/plugin-sdk`               | 插件、上下文 provider 与模型 provider 的接口 |
| `@ariadne/agent`                    | agent loop、调度、协调器与 thread 的工具     |
| `@ariadne/plugin-openai-compatible` | OpenAI 兼容协议的模型 provider               |
| `@ariadne/plugin-anthropic`         | Anthropic 协议的模型 provider                |
| `@ariadne/plugin-local`             | 本地目录上下文源：工作区与 git worktree      |
| `@ariadne/server`                   | 本地 HTTP 服务、鉴权、事件流与用户设置       |
| `@ariadne/web`                      | 浏览器界面                                   |
