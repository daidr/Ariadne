# Ariadne

一个自己使用的 AI 协作工具：你和协调器对话，协调器把需求拆成计划图并安排讨论串去调研、实现、修复，你随时能看到每件事在做什么、在等什么。

设计取舍见 [设计哲学](./docs/philosophy.md)，领域概念与分层见 [架构](./docs/architecture.md)。

> 早期开发中，目前只能在本机浏览器里使用。

## 开发

需要 Go 1.27.4 和 [Bun](https://bun.sh) 1.4.4。Go 运行后端，Bun 用于前端开发和构建。

```sh
bun install
bun run start    # 启动本地服务（127.0.0.1:7428）和构建好的页面，页面有改动时先构建
bun run dev      # 开发用：同时启动本地服务和前端开发服务器（localhost:5173）
bun run dev:sandbox # 浏览器验收：临时 settings、数据库、示例仓库和模拟 AI 接口
bun run check    # 格式、lint、类型检查和前端测试
go test ./...    # Go 后端测试
```

`dev:sandbox` 每次在系统临时目录创建独立的 `ARIADNE_HOME`，并打印浏览器链接、settings 路径和模拟 AI 地址。默认浏览器端口为 `5183`，后端端口为 `7438`，可用 `--port 7440 --web-port 5185` 调整。前端支持热更新。修改 Go 后端后重启沙箱生效。旧数据验证和实现说明见 [Go 后端迁移](./docs/backend-go-migration.md)。

沙箱预置工作中、待确认和已完成的讨论串，代码变更来自临时示例 Git 仓库，可以实际查看和合并。模型设置已接好本机 OpenAI 兼容接口，不需要真实 API Key：`mock` 流式回复，`mock-slow` 慢速流式回复（可测试中断），`mock-error` 返回请求失败（可测试错误与重试）。这些模型只按固定规则回复，不执行 AI 推理。按 Ctrl+C 停止前后端及模拟接口，临时数据保留便于排查，下次启动另建一套。

沙箱的搜索配置也接到本机模拟接口，Key 为 `dev-only`：可以在设置中切换 Tavily / DeepSeek、测试连接；换成其他 Key 会返回 401。

### 发版构建

```sh
bun run release                                # 当前平台
bun run release --target linux-x64,windows-x64 # 指定平台，或 --target all
```

构建程序将前端嵌入 Go 可执行文件，输出到 `dist/release/ariadne-<平台>`。支持 `linux-x64`、`linux-arm64`、`darwin-x64`、`darwin-arm64` 和 `windows-x64`，所有平台禁用 UPX。构建当前平台时会执行 `self-check`，发版流程会在每个目标平台上运行产物自检。

在「设置 → 更新」可检查新版本、查看更新日志，并选择正式或测试渠道。发布的测试版默认跟随测试渠道，正式渠道暂无发布时会保留当前版本。独立程序可以手动更新，也可以开启自动更新，下载核验通过后会替换程序，下一次启动生效。由包管理器管理的安装可设 `ARIADNE_DISABLE_SELF_UPDATE=1`，继续使用应用内的版本检查和更新日志。

这个程序不需要装 bun，运行起来和 `bun run start` 一样：数据在 `~/.ariadne`，环境变量相同，启动后自动打开页面。

`ariadne --version` 打印版本号，版本号来自根目录 `package.json`。

### 发布 alpha 版本

由 `.github/workflows/release.yml` 自动完成：

1. master 每有新提交，就把上次发版之后的提交整理进 release PR（分支 `release/alpha`）。PR 里把 `package.json` 的版本号改成下一个 alpha，并在 `CHANGELOG.md` 顶部加上这一版的提交列表。这个分支每次都从 master 重新生成，不要在上面手动提交。
2. 合入 release PR 后，构建五个平台的程序。在各目标平台核对 `--version` 并运行 `self-check`。全部通过后发布 `v<版本号>` 预发布版本，附上程序和这一版的更新日志。

需要在仓库的 Settings → Actions → General 里勾选 “Allow GitHub Actions to create and approve pull requests”，否则开不了 release PR。正式版本另走流程，还没有做。

测试换掉了外部服务和操作系统的部分，发布前在真实环境里按 [真实环境检查清单](./docs/real-env-checklist.md) 过一遍。模型接口的回放样本用 `bun run record:provider` 录制，见清单里的 DeepSeek 一节。

`bun run start` 之后会自动在浏览器里打开页面（已经有页面开着时不再开新的；没有图形界面的机器上不开，终端里照常打印带令牌的链接）。本地服务第一次启动时在 `~/.ariadne/token` 生成访问令牌。开发时把链接里的 `#token=…` 接到 `http://localhost:5173/` 后面打开即可。

第一次打开时先接上一个模型：本机已经登录的 Claude Code、Codex，或环境变量里的 Key（`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`DEEPSEEK_API_KEY`、`OPENROUTER_API_KEY`）会被列出来，点“用它”即可；也可以手动添加（OpenAI 兼容接口、Anthropic 接口，或 ACP agent），填好地址和 API Key 后模型列表会自动拉取，上下文、输出上限和价格按公开的模型目录填好，保存时会验证能不能连上。设置保存在 `~/.ariadne/config.json`。

“设置 → 联网搜索”显示可用的插件和模型搜索来源，也可以配置 Tavily 或 DeepSeek Search API 作为备用；自动搜索按插件、模型、备用 API 的顺序尝试。两个服务的 Key 和地址分别保存，地址留空使用官方默认值（程序追加 `/search`）；原有 Tavily Key 自动迁移。测试连接只发送一次搜索，不保存草稿；保存搜索设置时，未改动的模型不会重新请求验证。DeepSeek 请求格式见 [官方文档](https://api-docs.deepseek.com/api/search/)。

之后首屏就是一个输入框：写下想做的事，可以顺手选一个目录，发出去就建好了项目。项目名取目录名；没选目录时由协调器按第一句话起一个，右键项目可以随时重命名。

要让讨论串读写代码、运行命令，在顶栏的“项目设置”里给项目来源选一个本机目录、GitHub 仓库、GitLab 仓库。

- 本机目录：git 仓库会为每个讨论串建一个独立的 worktree（放在 `~/.ariadne/plugins/local/worktrees/` 下）和分支 `ariadne/…`，讨论串的提交留在这个分支上。讨论串面板上的“改动”逐个文件显示它相对基线分支改了什么，可在改动页合并到基线分支（默认压成一个提交，需要 git 2.38+），也可以直接“完成”交付。工作区按项目的保留策略回收，分支保留。
- GitHub 仓库：填 `owner/name`，访问令牌可以留空（会用 `GH_TOKEN`、`GITHUB_TOKEN` 或 `gh auth token`）。讨论串在自己的分支上工作，完成后推送分支、开 PR；PR 的检查失败时讨论串会被唤醒去修，合并由你决定。公开仓库第一次推送前会先问你一次，同意后整个项目有效。令牌存在 `~/.ariadne/config.json`，不进事件日志。
- GitLab 仓库：填 `group/project`（可以带子组）或项目地址；自建实例在“实例地址”里填 `https://gitlab.example.com`，或者直接填项目的完整地址。访问令牌需要 `api` 权限，可以留空：会用 `GITLAB_TOKEN`（只用于 `GITLAB_HOST` 指的实例，没设置时是 GitLab.com）或 `glab` 为这个实例保存的令牌。讨论串开的是 merge request（`!12`），流水线失败时同样会被唤醒去修；`internal` 项目对实例上所有人可见，第一次推送前也会先问你。

项目设置的“交付与托管”支持外部流程：填写集成平台、提票或预发布分支的交付要求后，讨论串按要求交付，AI 审阅通过后自动完成，不要求合并到默认分支。也可以直接告诉协调器修改交付方式或要求，它会按用户请求保存，不再额外确认；讨论串只读取这项配置。“工作区”中可单独选择自动回收、合入后回收或手动清理。

需要在远程机器上工作时，直接在那台机器部署 Ariadne，通过浏览器访问。

还要让它们读别的仓库或目录（共用的组件库、接口文档、另一个服务的代码），在项目设置的“项目来源”里添加参考，或者直接跟协调器说。参考是只读的，命令看不到它。

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

## 代码目录

| 目录                                                                    | 内容                                            |
| ----------------------------------------------------------------------- | ----------------------------------------------- |
| `cmd/ariadne`                                                           | Go 命令行和内嵌页面                             |
| `internal/server`、`internal/collab`                                    | HTTP、鉴权、SSE 和产物协作                      |
| `internal/core`、`internal/eventstore`                                  | 领域逻辑、SQLite 事件存储与投影                 |
| `internal/runner`、`internal/models`                                    | 协调器和讨论串、模型 provider、内置工具         |
| `internal/local`、`internal/hosting`                                    | 本地、GitHub 与 GitLab 项目来源                 |
| `internal/plugins`、`sdk`                                               | [Lua 插件](./docs/lua-plugins.md)宿主和能力接口 |
| `packages/web`、`packages/i18n`、`packages/core`、`packages/plugin-sdk` | 浏览器界面、翻译和前端使用的类型                |
