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

### 发版构建

```sh
bun run release                                # 当前平台
bun run release --target linux-x64,windows-x64 # 指定平台，或 --target all
bun run release --no-upx                       # 不压缩
```

先构建页面，再把服务端和全部页面文件编译成一个程序，最后用 [UPX](https://upx.github.io) 压缩，产物在 `dist/release/ariadne-<平台>`。可选平台：`linux-x64`、`linux-arm64`、`darwin-x64`、`darwin-arm64`、`windows-x64`。`PATH` 里没有 `upx` 时直接报错，不想压缩就加 `--no-upx`；macOS 的程序不压缩（UPX 打包后在新版 macOS 上起不来）。当前平台的程序压缩后会运行一次 `ariadne self-check`，确认还能读到页面；为其他平台交叉构建的程序跑不了这一步，要拿到对应的机器上自己运行一次。

这个程序不需要装 bun，运行起来和 `bun run start` 一样：数据在 `~/.ariadne`，环境变量相同，启动后自动打开页面。

`ariadne --version` 打印版本号，版本号来自根目录 `package.json`。

### 发布 alpha 版本

由 `.github/workflows/release.yml` 自动完成：

1. master 每有新提交，就把上次发版之后的提交整理进 release PR（分支 `release/alpha`）。PR 里把 `package.json` 的版本号改成下一个 alpha，并在 `CHANGELOG.md` 顶部加上这一版的提交列表。这个分支每次都从 master 重新生成，不要在上面手动提交。
2. 合入 release PR 后，构建全部平台的程序，在 Linux、Windows、macOS 上各运行一次 `--version` 和 `self-check`，都通过后发布 `v<版本号>` 预发布版本，附上程序和这一版的更新日志。

需要在仓库的 Settings → Actions → General 里勾选 “Allow GitHub Actions to create and approve pull requests”，否则开不了 release PR。正式版本另走流程，还没有做。

测试换掉了外部服务和操作系统的部分，发布前在真实环境里按 [真实环境检查清单](./docs/real-env-checklist.md) 过一遍。模型接口的回放样本用 `bun run record:provider` 录制，见清单里的 DeepSeek 一节。

`bun run start` 之后会自动在浏览器里打开页面（已经有页面开着时不再开新的；没有图形界面的机器上不开，终端里照常打印带令牌的链接）。本地服务第一次启动时在 `~/.ariadne/token` 生成访问令牌。开发时把链接里的 `#token=…` 接到 `http://localhost:5173/` 后面打开即可。

第一次打开时先接上一个模型：本机已经登录的 Claude Code、Codex，或环境变量里的 Key（`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`DEEPSEEK_API_KEY`、`OPENROUTER_API_KEY`）会被列出来，点“用它”即可；也可以手动添加（OpenAI 兼容接口、Anthropic 接口，或 ACP agent），填好地址和 API Key 后模型列表会自动拉取，上下文、输出上限和价格按公开的模型目录填好，保存时会验证能不能连上。设置保存在 `~/.ariadne/config.json`。

之后首屏就是一个输入框：写下想做的事，可以顺手选一个目录，发出去就建好了项目。项目名取目录名；没选目录时由协调器按第一句话起一个，右键项目可以随时重命名。

要让讨论串读写代码、运行命令，在顶栏的“项目设置”里给项目来源选一个本机目录、GitHub 仓库、GitLab 仓库、SSH 远程目录或远程 daemon。

- 本机目录：git 仓库会为每个讨论串建一个独立的 worktree（放在 `~/.ariadne/plugins/local/worktrees/` 下）和分支 `ariadne/…`，讨论串的提交留在这个分支上。讨论串面板上的“改动”逐个文件显示它相对基线分支改了什么，点“合并到 main”合并进来（默认压成一个提交，需要 git 2.38+）；改动合入后 worktree 自动移除，分支保留。
- GitHub 仓库：填 `owner/name`，访问令牌可以留空（会用 `GH_TOKEN`、`GITHUB_TOKEN` 或 `gh auth token`）。讨论串在自己的分支上工作，完成后推送分支、开 PR；PR 的检查失败时讨论串会被唤醒去修，合并由你决定。公开仓库第一次推送前会先问你一次，同意后整个项目有效。令牌存在 `~/.ariadne/config.json`，不进事件日志。
- GitLab 仓库：填 `group/project`（可以带子组）或项目地址；自建实例在“实例地址”里填 `https://gitlab.example.com`，或者直接填项目的完整地址。访问令牌需要 `api` 权限，可以留空：会用 `GITLAB_TOKEN`（只用于 `GITLAB_HOST` 指的实例，没设置时是 GitLab.com）或 `glab` 为这个实例保存的令牌。讨论串开的是 merge request（`!12`），流水线失败时同样会被唤醒去修；`internal` 项目对实例上所有人可见，第一次推送前也会先问你。
- SSH 远程目录：填主机（`user@host` 或 `~/.ssh/config` 里的 Host）和远端的绝对路径。用你自己的 ssh 配置、密钥和 agent，不支持密码；第一次连接前先在终端里 ssh 一次确认主机指纹。git 仓库会在远端的 `~/.ariadne-worktrees/` 下为每个讨论串建 worktree 和分支 `ariadne/…`，看改动和合并与本机目录相同。命令在远端以登录用户身份运行，连接不转发 agent 和端口。讨论串在远端的 `git push` 推不出去，合并之后由你自己推送。远端的 `PATH` 里有 `ariadne-daemon` 时，Ariadne 只开一个 ssh 进程，所有调用都走它，更快。
- 远程 daemon：连不了 SSH 的机器用它。在远端运行 `ariadne-daemon`（`bun run build:daemon` 打出单文件程序，或者有 bun 时 `bun packages/daemon/src/main.ts`），第一次启动打印配对码；把地址、配对码和远端目录填进来。默认只监听 `127.0.0.1:7429`，`--listen 0.0.0.0:7429` 让其他机器连上来；连接总是 TLS，按配对码里的证书指纹认 daemon。`ariadne-daemon pair` 换一个配对码，旧的立即失效。daemon 只存令牌的哈希；讨论串的命令以运行 daemon 的用户身份执行，建议给它单独一个用户。需要远端有 openssl。

还要让它们读别的仓库或目录（共用的组件库、接口文档、另一个服务的代码），在项目设置的“项目来源”里添加参考，或者直接跟协调器说。参考是只读的，命令看不到它。

- SSH 和 daemon 都可以再选一个“代码托管”（GitHub 或 GitLab）：讨论串在远端写代码，推送和开 PR 由本机做，令牌不去远端。远端目录必须是这个仓库的签出。

协调器和讨论串写的长内容放在“产物”里：文档、画板和仪表盘快照。右侧“产物”页的“全部产物”打开项目的产物总览，可以筛选、搜索，也可以自己新建；每种产物都能直接编辑，每次保存是一个新版本。

| 环境变量                 | 作用                                         | 默认值                   |
| ------------------------ | -------------------------------------------- | ------------------------ |
| `ARIADNE_HOME`           | 数据库与令牌所在的目录                       | `~/.ariadne`             |
| `ARIADNE_PORT`           | 本地服务的端口                               | `7428`                   |
| `ARIADNE_TOKEN`          | 指定访问令牌，不读文件                       | —                        |
| `ARIADNE_OPEN`           | 设为 `0` 时 `bun run start` 不自动打开浏览器 | `1`                      |
| `ARIADNE_GITHUB_API_URL` | GitHub REST API 的地址（GitHub Enterprise）  | `https://api.github.com` |
| `ARIADNE_GITHUB_GIT_URL` | 克隆仓库用的地址前缀                         | `https://github.com`     |
| `ARIADNE_REPO_POLL_MS`   | 检查 PR 状态的间隔（毫秒）                   | `60000`                  |
| `ARIADNE_DAEMON_HOME`    | daemon 的证书和令牌哈希所在的目录（远端）    | `~/.ariadne-daemon`      |

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
| `@ariadne/plugin-local`             | 本地目录项目来源：工作区与 git worktree，托管仓库的共享 clone |
| `@ariadne/plugin-github`            | GitHub 仓库项目来源：共享 clone、worktree、PR 与检查          |
| `@ariadne/plugin-gitlab`            | GitLab 仓库项目来源：MR、流水线、issue，支持自建实例          |
| `@ariadne/plugin-ssh`               | SSH 远程目录项目来源：远端 worktree、文件与命令               |
| `@ariadne/plugin-daemon`            | 远程 daemon 项目来源：配对、证书指纹、一条连接跑所有调用      |
| `@ariadne/daemon`                   | 在远端运行的 `ariadne-daemon`                                 |
| `@ariadne/remote`                   | 远端工作区：脚本与 worktree，SSH 和 daemon 共用               |
| `@ariadne/server`                   | 本地 HTTP 服务、鉴权、事件流与用户设置                        |
| `@ariadne/web`                      | 浏览器界面                                                    |
