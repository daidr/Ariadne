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

要让 thread 读写代码、运行命令，在右侧“项目来源”里选一个本机目录、GitHub 仓库、GitLab 仓库或 SSH 远程目录。

- 本机目录：git 仓库会为每个 thread 建一个独立的 worktree（放在 `~/.ariadne/plugins/local/worktrees/` 下）和分支 `ariadne/…`，thread 的提交留在这个分支上，合并由你决定。
- GitHub 仓库：填 `owner/name`，访问令牌可以留空（会用 `GH_TOKEN`、`GITHUB_TOKEN` 或 `gh auth token`）。thread 在自己的分支上工作，完成后推送分支、开 PR；PR 的检查失败时 thread 会被唤醒去修，合并由你决定。公开仓库第一次推送前会先问你一次，同意后整个项目有效。令牌存在 `~/.ariadne/config.json`，不进事件日志。
- GitLab 仓库：填 `group/project`（可以带子组）或项目地址；自建实例在“实例地址”里填 `https://gitlab.example.com`，或者直接填项目的完整地址。访问令牌需要 `api` 权限，可以留空：会用 `GITLAB_TOKEN`（只用于 `GITLAB_HOST` 指的实例，没设置时是 GitLab.com）或 `glab` 为这个实例保存的令牌。thread 开的是 merge request（`!12`），流水线失败时同样会被唤醒去修；`internal` 项目对实例上所有人可见，第一次推送前也会先问你。
- SSH 远程目录：填主机（`user@host` 或 `~/.ssh/config` 里的 Host）和远端的绝对路径。用你自己的 ssh 配置、密钥和 agent，不支持密码；第一次连接前先在终端里 ssh 一次确认主机指纹。git 仓库会在远端的 `~/.ariadne-worktrees/` 下为每个 thread 建 worktree 和分支 `ariadne/…`，命令在远端以登录用户身份运行，不经过本机沙箱，连接不转发 agent 和端口。thread 在远端推送前每次都会先问你。

协调器和 thread 写的长内容放在“产物”里：文档、画板和仪表盘快照。右侧“产物”页的“全部产物”打开项目的产物总览，可以筛选、搜索，也可以自己新建；每种产物都能直接编辑，每次保存是一个新版本。

| 环境变量                 | 作用                                        | 默认值                   |
| ------------------------ | ------------------------------------------- | ------------------------ |
| `ARIADNE_HOME`           | 数据库与令牌所在的目录                      | `~/.ariadne`             |
| `ARIADNE_PORT`           | 本地服务的端口                              | `7428`                   |
| `ARIADNE_TOKEN`          | 指定访问令牌，不读文件                      | —                        |
| `ARIADNE_GITHUB_API_URL` | GitHub REST API 的地址（GitHub Enterprise） | `https://api.github.com` |
| `ARIADNE_GITHUB_GIT_URL` | 克隆仓库用的地址前缀                        | `https://github.com`     |
| `ARIADNE_REPO_POLL_MS`   | 检查 PR 状态的间隔（毫秒）                  | `60000`                  |

## 包

| 包                                  | 内容                                                          |
| ----------------------------------- | ------------------------------------------------------------- |
| `@ariadne/i18n`                     | 文案与翻译，服务端和前端共用                                  |
| `@ariadne/core`                     | 领域模型、SQLite 事件存储与投影                               |
| `@ariadne/plugin-sdk`               | 插件、上下文 provider 与模型 provider 的接口                  |
| `@ariadne/agent`                    | agent loop、调度、协调器与 thread 的工具                      |
| `@ariadne/plugin-openai-compatible` | OpenAI 兼容协议的模型 provider                                |
| `@ariadne/plugin-anthropic`         | Anthropic 协议的模型 provider                                 |
| `@ariadne/plugin-local`             | 本地目录上下文源：工作区与 git worktree，托管仓库的共享 clone |
| `@ariadne/plugin-github`            | GitHub 仓库上下文源：共享 clone、worktree、PR 与检查          |
| `@ariadne/plugin-gitlab`            | GitLab 仓库上下文源：MR、流水线、issue，支持自建实例          |
| `@ariadne/plugin-ssh`               | SSH 远程目录上下文源：远端 worktree、文件与命令               |
| `@ariadne/server`                   | 本地 HTTP 服务、鉴权、事件流与用户设置                        |
| `@ariadne/web`                      | 浏览器界面                                                    |
