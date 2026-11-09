# Ariadne

一个自己使用的 AI 协作工具：你和协调器对话，协调器把需求拆成计划图并安排讨论串去调研、实现、修复，你随时能看到每件事在做什么、在等什么。

设计取舍见 [设计哲学](./docs/philosophy.md)，领域概念与分层见 [架构](./docs/architecture.md)。

> 早期开发中，目前只能在本机浏览器里使用。

## 开发

需要 [Bun](https://bun.sh) 1.4 及以上。

```sh
bun install
bun run start    # 启动本地服务（127.0.0.1:7428）和构建好的页面，页面有改动时先构建
bun run dev      # 开发用：同时启动本地服务和前端开发服务器（localhost:5173）
bun run check    # 格式、lint、类型检查和测试
```

测试换掉了外部服务和操作系统的部分，发布前在真实环境里按 [真实环境检查清单](./docs/real-env-checklist.md) 过一遍。模型接口的回放样本用 `bun run record:provider` 录制，见清单里的 DeepSeek 一节。

本地服务第一次启动时会在 `~/.ariadne/token` 生成访问令牌，并在终端打印带令牌的链接，`bun run start` 之后直接打开它。开发时把链接里的 `#token=…` 接到 `http://localhost:5173/` 后面打开即可。

打开页面后，在右上角的“设置”里添加一个模型提供方（OpenAI 兼容接口、Anthropic 接口，或本机已登录的 Claude Code、Codex 和 ACP agent），填好地址和 API Key（外部 agent 只要命令），再添加一个模型，就可以和协调器对话了。设置保存在 `~/.ariadne/config.json`。

要让讨论串读写代码、运行命令，在顶栏的“项目设置”里给项目来源选一个本机目录、GitHub 仓库、GitLab 仓库、SSH 远程目录或远程 daemon。

- 本机目录：git 仓库会为每个讨论串建一个独立的 worktree（放在 `~/.ariadne/plugins/local/worktrees/` 下）和分支 `ariadne/…`，讨论串的提交留在这个分支上。讨论串面板上的“改动”逐个文件显示它相对基线分支改了什么，点“合并到 main”合并进来（默认压成一个提交，需要 git 2.38+）；改动合入后 worktree 自动移除，分支保留。
- GitHub 仓库：填 `owner/name`，访问令牌可以留空（会用 `GH_TOKEN`、`GITHUB_TOKEN` 或 `gh auth token`）。讨论串在自己的分支上工作，完成后推送分支、开 PR；PR 的检查失败时讨论串会被唤醒去修，合并由你决定。公开仓库第一次推送前会先问你一次，同意后整个项目有效。令牌存在 `~/.ariadne/config.json`，不进事件日志。
- GitLab 仓库：填 `group/project`（可以带子组）或项目地址；自建实例在“实例地址”里填 `https://gitlab.example.com`，或者直接填项目的完整地址。访问令牌需要 `api` 权限，可以留空：会用 `GITLAB_TOKEN`（只用于 `GITLAB_HOST` 指的实例，没设置时是 GitLab.com）或 `glab` 为这个实例保存的令牌。讨论串开的是 merge request（`!12`），流水线失败时同样会被唤醒去修；`internal` 项目对实例上所有人可见，第一次推送前也会先问你。
- SSH 远程目录：填主机（`user@host` 或 `~/.ssh/config` 里的 Host）和远端的绝对路径。用你自己的 ssh 配置、密钥和 agent，不支持密码；第一次连接前先在终端里 ssh 一次确认主机指纹。git 仓库会在远端的 `~/.ariadne-worktrees/` 下为每个讨论串建 worktree 和分支 `ariadne/…`，看改动和合并与本机目录相同。命令在远端以登录用户身份运行，不经过本机沙箱，连接不转发 agent 和端口。讨论串在远端的 `git push` 推不出去，合并之后由你自己推送。远端的 `PATH` 里有 `ariadne-daemon` 时，Ariadne 只开一个 ssh 进程，所有调用都走它，更快。
- 远程 daemon：连不了 SSH 的机器用它。在远端运行 `ariadne-daemon`（`bun run build:daemon` 打出单文件程序，或者有 bun 时 `bun packages/daemon/src/main.ts`），第一次启动打印配对码；把地址、配对码和远端目录填进来。默认只监听 `127.0.0.1:7429`，`--listen 0.0.0.0:7429` 让其他机器连上来；连接总是 TLS，按配对码里的证书指纹认 daemon。`ariadne-daemon pair` 换一个配对码，旧的立即失效。daemon 只存令牌的哈希，讨论串的命令在远端的沙箱里看不到 daemon 的目录。需要远端有 openssl。
- SSH 和 daemon 都可以再选一个“代码托管”（GitHub 或 GitLab）：讨论串在远端写代码，推送和开 PR 由本机做，令牌不去远端。远端目录必须是这个仓库的签出。

协调器和讨论串写的长内容放在“产物”里：文档、画板和仪表盘快照。右侧“产物”页的“全部产物”打开项目的产物总览，可以筛选、搜索，也可以自己新建；每种产物都能直接编辑，每次保存是一个新版本。

| 环境变量                 | 作用                                        | 默认值                   |
| ------------------------ | ------------------------------------------- | ------------------------ |
| `ARIADNE_HOME`           | 数据库与令牌所在的目录                      | `~/.ariadne`             |
| `ARIADNE_PORT`           | 本地服务的端口                              | `7428`                   |
| `ARIADNE_TOKEN`          | 指定访问令牌，不读文件                      | —                        |
| `ARIADNE_GITHUB_API_URL` | GitHub REST API 的地址（GitHub Enterprise） | `https://api.github.com` |
| `ARIADNE_GITHUB_GIT_URL` | 克隆仓库用的地址前缀                        | `https://github.com`     |
| `ARIADNE_REPO_POLL_MS`   | 检查 PR 状态的间隔（毫秒）                  | `60000`                  |
| `ARIADNE_DAEMON_HOME`    | daemon 的证书和令牌哈希所在的目录（远端）   | `~/.ariadne-daemon`      |

## 包

| 包                                  | 内容                                                          |
| ----------------------------------- | ------------------------------------------------------------- |
| `@ariadne/i18n`                     | 文案与翻译，服务端和前端共用                                  |
| `@ariadne/core`                     | 领域模型、SQLite 事件存储与投影                               |
| `@ariadne/plugin-sdk`               | 插件、上下文 provider 与模型 provider 的接口                  |
| `@ariadne/agent`                    | agent loop、调度、协调器与讨论串的工具                        |
| `@ariadne/plugin-openai-compatible` | OpenAI 兼容协议的模型 provider                                |
| `@ariadne/plugin-anthropic`         | Anthropic 协议的模型 provider                                 |
| `@ariadne/plugin-agents`            | 外部 agent（Claude Code、Codex、ACP）当模型 provider          |
| `@ariadne/plugin-local`             | 本地目录上下文源：工作区与 git worktree，托管仓库的共享 clone |
| `@ariadne/plugin-github`            | GitHub 仓库上下文源：共享 clone、worktree、PR 与检查          |
| `@ariadne/plugin-gitlab`            | GitLab 仓库上下文源：MR、流水线、issue，支持自建实例          |
| `@ariadne/plugin-ssh`               | SSH 远程目录上下文源：远端 worktree、文件与命令               |
| `@ariadne/plugin-daemon`            | 远程 daemon 上下文源：配对、证书指纹、一条连接跑所有调用      |
| `@ariadne/daemon`                   | 在远端运行的 `ariadne-daemon`                                 |
| `@ariadne/remote`                   | 远端工作区：脚本与 worktree，SSH 和 daemon 共用               |
| `@ariadne/sandbox`                  | 工作区沙箱，本机服务和 daemon 共用                            |
| `@ariadne/server`                   | 本地 HTTP 服务、鉴权、事件流与用户设置                        |
| `@ariadne/web`                      | 浏览器界面                                                    |
