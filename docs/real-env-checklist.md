# 真实环境检查清单

测试里换掉了外部服务和操作系统的部分，需要在真实环境里手动过一遍。每项写了怎么做和看到什么算对；不对的地方记下现象和日志，开 issue。

## 开始之前

- 用一个新的 `ARIADNE_HOME`（例如 `ARIADNE_HOME=~/.ariadne-check bun run start`），不碰日常用的数据。
- 准备一个小仓库当项目来源，里面有 `package.json` 和几行代码就够了。

## Windows

在 Windows 10/11 上，用 PowerShell 启动 `bun run start`。

- [ ] **写时复制**：仓库放在 Dev Drive（ReFS）上、`~/.ariadne` 在 C 盘时，新讨论串的 worktree 建在仓库旁边的 `.ariadne-worktrees\\` 下；设置的“工作区”页这个项目名旁的图标提示“写时复制，仅占用改动部分”，开三个讨论串后卷的已用空间基本不变（Windows 11 24H2 及以上）。仓库在 NTFS 盘上时，worktree 在 `~/.ariadne` 下，“工作区”页项目名旁的图标提示 Dev Drive。检测用的是 PowerShell 的 `(Get-Volume -DriveLetter D).FileSystemType`，看它不弹窗口、返回 `ReFS`。
- [ ] **不弹窗口**：让讨论串跑几条命令（`git status`、`bun --version`、一条 `Start-Sleep 3`），整个过程中不弹出黑色控制台窗口。合并、看改动、拉 PR 状态时也不弹。
- [ ] **默认 shell**：装了 PowerShell 7 时用 `pwsh`，没装时用 Windows 自带的 `powershell`；在讨论串里让它运行 `$PSVersionTable.PSVersion`，看追踪里的命令结果。中文输出不乱码。
- [ ] **bash**：项目设置里把 shell 换成 Git Bash 的 `bash.exe`，`echo 你好 && ls` 能跑，输出不乱码。
- [ ] **停止命令**：让讨论串在后台启动 `bun -e "setInterval(() => {}, 1000)"`，在状态栏里停掉；任务管理器里这个 bun 进程和它的子进程都没了。
- [ ] **命令超时**：把命令超时调到 10 秒，让讨论串运行 `Start-Sleep 60`；10 秒后结束，任务管理器里没有残留的 powershell。
- [ ] **停止讨论串**：讨论串跑着后台命令时点停止，后台命令一起结束。
- [ ] **崩溃后清理**：讨论串在后台跑着开发服务器时，在任务管理器里直接结束 Ariadne 的 bun 进程；再启动，终端打印“上次退出时还在跑的 N 个进程已结束”，开发服务器的端口已经空出来。
- [ ] **MCP 进程**：配一个本机 stdio MCP 服务，用过一次后结束 Ariadne 进程再启动，旧的 MCP 进程没有留下。
- [ ] **路径**：协调器的只读来源视图里打开子目录里的文件（`src\a.ts` 这种），能看到内容；文件名带中文和空格的也行。看改动、引用文件（`@src/a.ts`）、讨论串里点开图片都正常。
- [ ] **worktree**：本机目录来源下，讨论串的 worktree 建在 `%USERPROFILE%\.ariadne\plugins\local\worktrees\` 下，合并后自动移除；删除项目后目录也清掉。
- [ ] **界面**：Edge 和 Chrome 里各看一遍：快捷键里 `⌘/Ctrl` 显示为 Ctrl，输入框里 `Ctrl + Enter` 按设置发送，滚动条不挡内容。

## GitLab

用 GitLab.com 上的一个私有测试项目，再找一个自建实例（有的话）。

- [ ] **连接**：项目来源填 `group/project`，令牌留空时用 `GITLAB_TOKEN` 或 `glab` 的令牌；填错令牌时提示清楚，不是一串堆栈。
- [ ] **子组和自建实例**：`group/sub/project` 能连上；自建实例填完整地址也能连上。
- [ ] **merge request**：讨论串推分支、开 MR，讨论串里的 `!12` 链接悬停能看到 MR 卡片，草稿 MR 有标记。
- [ ] **流水线**：故意让流水线失败，讨论串被唤醒去修；修好后流水线变绿。
- [ ] **internal 项目**：第一次推送前问一次，同意后同一个项目不再问。
- [ ] **删除分支**：在 GitLab 上合并并删除源分支后，下次同步时本地的 `origin/…` 分支也删掉了。

## DeepSeek

在设置里加 DeepSeek（OpenAI 兼容接口，地址 `https://api.deepseek.com`），`deepseek-chat` 和 `deepseek-reasoner` 各加一个模型。

- [ ] **录样本**：`bun run record:provider` 列出设置里的模型；`bun run record:provider <模型 id> deepseek-reasoner` 录一遍，每个场景都是 ✓。`deepseek-chat` 也录一遍。录下的文件在 `packages/server/fixtures/providers/` 下，搜一下里面没有 API Key，然后提交。之后 `bun run check` 会回放它们。
- [ ] **思考内容**：用 `deepseek-reasoner` 跑一个要调用工具的讨论串，几步之后不报“缺少 reasoning_content”这类错误；追踪里能看到每一步的思考。
- [ ] **输出被截断**：给模型设一个很小的“单次最多输出”（比如 200），让它写一个长文件；它被截断后接着写，追踪里那一步标着被截断，连续三次才失败。
- [ ] **清理之后的缓存**：一个长对话触发“清理旧的工具结果”之后，下一个请求的缓存命中（追踪里的“缓存”）先掉一次，之后的请求又能命中。
- [ ] **备用模型**：把主模型的地址改成一个连不上的地址，备用模型设成 `deepseek-chat`；重试用完之后讨论串换到备用模型接着做，追踪里有换模型的一行。

## Anthropic 接口

- [ ] **录样本**：`bun run record:provider <模型 id> <名字>` 录一遍，提交。
- [ ] **思考和签名**：打开思考的模型跑带工具的讨论串，几步之后不报签名错误。

## 联网搜索

- [ ] **Anthropic 自带搜索**：只配一个官方地址的 Anthropic 模型，让讨论串查一件最近的事；追踪里 `web_search` 的结果有一段说明和“Pages”列表，用量比这一步的请求多出搜索那一次。
- [ ] **OpenAI 自带搜索**：只配官方地址的 OpenAI 模型，同上（走的是 `responses` 端点）。
- [ ] **搜索 Key**：只配 DeepSeek 时，「设置 → 模型」底部出现“联网搜索”，没填 Key 时模型没有 `web_search`、问到要搜索时会让你去填；填上 Tavily 的 Key 之后能搜到结果。
- [ ] **接口拒绝搜索**：Anthropic 组织在 Console 里关掉联网搜索（或 OpenAI 配一个不支持搜索工具的模型）后搜一次；之后「设置 → 模型」底部出现“联网搜索”，下一回合起模型不再有 `web_search`（没填 Key 时）。重新保存模型设置后会再试一次。
- [ ] **搜索的按次费用**：估算花费只算了搜索请求的 token，没算接口按次收的搜索费（Anthropic 的响应里有 `usage.server_tool_use.web_search_requests`）。
- [ ] **中转地址**：Anthropic 或 OpenAI 协议配成中转地址时不走自带搜索，表现和 DeepSeek 一样。

## 外部 agent

先在终端里把对应的程序登录好（`claude`、`codex login`，ACP agent 按它自己的说明）。在设置里分别加 Claude Code、Codex 和一个 ACP agent 的提供方，模型名可以填 `default`。

- [ ] **Claude Code**：用它跑一个要读写文件、跑命令的讨论串；追踪里每个工具调用都在；讨论串里回复一次之后再说一句，追踪里第二回合的输入只多了这一句（同一个进程接着做）。用量和“估算花费”有数。
- [ ] **Codex**：同上；它不会自己跑命令（追踪里看不到它绕开工具的动作），思考深度改成“高”之后它照常工作。
- [ ] **Codex + SSH 工作区**：项目接 SSH 工作区，模型选 Codex（含代码模式的模型，比如 gpt-6.1-sol），让它查主机名和系统（`hostname; uname -a`），回答应是远端那台；让它跑一个只装在本机的命令，应在远端报找不到，而不是在本机跑起来。
- [ ] **步数**：Claude Code 对会改东西的工具是一个一个调用的，一次回复里的几个调用会拆成几步。看一下长任务里步数离上限和循环检测有多近。
- [ ] **ACP agent**：同上；它要用自己需要权限的工具时被拒绝，Ariadne 的工具照常放行（工作目录在 `~/.ariadne/` 下也不会误放行）。不请求权限的自带工具关不掉。不支持 HTTP 的 MCP 的 agent 也能调到工具（走 `mcp-stdio.ts` 转发）。
- [ ] **停下与重启**：回合进行中点停止，`ps` 里那个 agent 进程没了；服务重启之后再发消息，开了新进程，它能接着之前的对话说。
- [ ] **没登录**：退出登录后发消息，讨论串以失败结束，原因里写着要登录，不会反复重试。
- [ ] **Windows**：`claude`、`codex` 是 `.cmd` 包装时能不能启动（没在 Windows 上试过）。

## 花费估算

- [ ] **从目录填入**：模型价格里点“从 models.dev 填入”，Anthropic、OpenAI 兼容（DeepSeek、OpenRouter 地址）的模型都能填上标价。
- [ ] **显示**：打开“用量里显示估算花费”，项目用量和输入框旁圆环的弹层里有“估算花费”；一个人民币价格的模型和一个美元价格的模型都用过之后，两种币种并列。

## 可靠性

- [ ] **回合中途退出**：讨论串正在请求模型时按 Ctrl+C 停掉服务再启动；那个回合显示为被中断，讨论串接着做，不会卡在“工作中”。
- [ ] **审阅中途退出**：审阅讨论串交出结论前停掉服务再启动；审阅接着做完，或者在“需要我”里看到它没有结论就结束了，不会一直显示审阅中。
- [ ] **两个服务**：同一个 `ARIADNE_HOME` 启动第二个服务时，第二个提示“另一个 Ariadne 还在用这个目录”，第一个的后台命令不受影响。第二个服务的进程只记在内存里：它之后崩溃的话，留下的进程不会在下次启动时清理。

## 浏览器工具

- [ ] **本机浏览器**：装了 Chrome 或 Edge 的 Windows 和 macOS 上，让讨论串用 `browser` 打开一个页面并截图；追踪里看到截图，`~/.ariadne/browser` 下没有下载内容。
- [ ] **自动下载**：在没有 Chrome、Edge、Chromium 的机器（或临时改名浏览器）上用一次，第一次调用会下载 Chrome for Testing 的 headless shell 到 `~/.ariadne/browser`，之后直接用；Windows 上用系统自带的 `tar` 解压，Linux 和 macOS 用 `unzip`。
- [ ] **SSH 端口**：SSH 项目（远端没装 `ariadne-daemon`）里，让讨论串在远端后台起一个开发服务器，再用 `browser` 打开 `http://localhost:<端口>`，页面能打开，热更新的 WebSocket 能连上。这条路径用的是 `ssh -W localhost:<端口>` 加 `ClearAllForwardings=yes`，要确认它没有被拦下；再试一个只监听 `::1` 的服务器（比如 Vite 在部分系统上的默认值）。远端装的是旧版 `ariadne-daemon` 时也应走这条路径。
- [ ] **daemon 端口**：daemon 项目里做同样的事，走 relay 的 `tcp` 通道。

## 发版程序

- [ ] **Windows 产物**：确认未经过 UPX 压缩，并能运行 `--version` 和 `self-check`（发版流程的 Windows 自检会验证这两项）；UPX 仅用于 Linux。
- [ ] **压缩后的程序**：直接运行压缩后的程序，页面自动打开，能建项目、跑一个讨论串、打开文档（文档的协同编辑走 WebSocket）。
- [ ] **ACP 的工具桥**：用发版程序跑一个只支持本地 MCP 程序的 ACP agent，追踪里 Ariadne 的工具调用照常（程序用 `ariadne mcp-stdio <地址>` 启动自己做桥）。
- [ ] **macOS**：`darwin-arm64` 程序在 Mac 上能启动（没有签名时第一次要在“隐私与安全性”里放行）。
- [ ] **发版流程**：master 合入一个 PR 后出现 release PR，版本号和更新日志正确；再合入一个 PR，release PR 被重写，更新日志里有两条提交。合入 release PR 后 Actions 里三个系统的自检都通过，Releases 里出现 alpha 预发布版本，附带五个程序。
