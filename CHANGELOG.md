# 更新日志

## 0.1.0-alpha.16 (2026-11-23)

- feat(web): 优化 PWA 安全区、缩放体验及 iPhone Duo 右侧导航栏与折叠屏适配 (#197)
- feat(web): 升级 DouUI 至 0.1.0-alpha.22，接入原生能力并清理 Workaround (#198)
- fix(runner,antigravity): 移除旧工具结果原地清理并为 session/new 瞬时竞态增加重试 (#196)
- fix(web): support Shiki fallback syntax highlighting in Diff panel (#195)
- feat(models): 移除备用模型，子调研与聊天框模型切换支持思考力度级联选择 (#194)
- feat(web): add medium responsive layout with project popover (#190)
- fix(web): 修复空标签栏拖拽指示线被裁切并支持产物卡片与行内链接拖拽到标签栏 (#191)
- fix(web): avoid orphan top divider in ThreadTools dropdown when slot is empty (#189)
- fix(web): prevent project activity popover from opening on mobile and touch taps (#186)
- feat(rpc): migrate file sheet preview to ConnectRPC/Protobuf (#187)
- fix(web): 提高聊天输入框底部渐变遮罩高度以覆盖底部圆角 (#188)
- fix(browser): stabilize headless screenshot capture and Windows CI test timeouts (#185)
- feat(plugins): split Agent CLI model providers into built-in plugins and decouple acp/antigravity (#184)
- fix: 回填并同步讨论串分支增删行数统计，修复改动胶囊只显示「改动」 (#183)
- feat(rpc): migrate event stream to ConnectRPC streaming, eliminate polling, and remove legacy JSON routes (#182)
- feat(plugins): built-in plugin toggles, HTTP model provider plugins, and Lua SSE/networkOrigin support (#180)
- fix(local): normalize CRLF in worktree snapshots and reconcile branch diff totals (#181)
- feat(rpc): migrate projects, plans, artifacts, and issues to ConnectRPC/Protobuf (#174)
- fix(agents): bound history replay to preserve newest turns and triggering user message (#163)
- fix: 修复 GitHub 项目气泡缺少 Diff 入口并优化多文件 Diff 面板加载性能 (#179)
- feat(rpc): migrate settings, plugins, updates, and meta domain to ConnectRPC (#172)
- fix: update and display thread context usage before first visible message (#178)
- feat(rpc): migrate threads, conversations, reviews, memory, and workspaces to ConnectRPC (#177)
- feat(search): unify built-in Tavily/DeepSeek and external search plugins (#170)
- feat(web): add dedicated unauthorized screen and manual token dialog (#173)
- feat(plan): 支持计划图分组、自动收起已完成项并精简交互 (#175)
- feat(web): upgrade DouUI to 0.1.0-alpha.21 and refine chat Markdown rhythm (#171)
- perf: speed up Windows storage and ReFS exclusive scans (#176)
- feat: update document whiteboard guidance and add code block syntax highlighting (#169)
- feat(web): replace connection status top banners with persistent toasts (#168)
- fix: preserve coordinator and thread chat drafts across view and project switches (#166)
- feat(runner): allow discussion threads to update memory on direct user input (#165)
- fix(web): improve submitted and active FormBlock card layout (#167)
- feat: 搭建 ConnectRPC 与 Protobuf 基础设施并完成 DataService 试点 (#150)
- feat: surface pull request merge conflicts in PR pill and hover card (#164)
- fix: handle Antigravity CLI subscription quota reached errors (#161)
- ci: 移除 compatibility CI 并替换为轻量 race 任务 (#160)
- refactor(web): remove plan node display and selector from thread header (#162)
- refactor: tighten coordinator, thread, and reviewer system prompts (#157)
- feat: add Ariadne SVG logo, icon generator, and PWA support (#151)
- fix: allow waiting read_command_output polls without tripping loop guard (#156)
- feat: 支持粘贴与附加更多类型文件并提供附件读取与落盘工具 (#159)
- fix(web): keep thread list order and row height stable during concurrent runs (#158)
- feat(web): support mobile pinch-to-zoom in image previews (#153)
- feat: support linking and unlinking plan nodes on existing threads (#149)
- fix(web): remove inner vertical scrollbar from diff editors (#155)
- perf: 修复 4 个 P0 接口查询、增量同步与 SSE 性能瓶颈 (#154)
- feat: apply coordinator plan edits without confirmation (#148)
- fix: canonicalize workspace and hosted storage paths across platforms (#152)
- fix: unify pull request links across workspace views
- fix: suppress scrolling tab marker during pinned drags
- feat: pin workspace tabs and virtualize conversation history
- fix: clean up orphaned worktrees and support Windows long paths
- feat: restore workspace routes and recent tabs
- feat: add single-pane mobile workspace navigation
- feat: add issue status links and previews
- fix: scope pull request approvals and render chat breaks
- feat: estimate reclaimable workspace space on ReFS
- feat: improve discussion delivery, provider integration and command output
- feat: configure hosted source plugins and repository storage
- Merge pull request #146 from daidr/release/alpha

## 0.1.0-alpha.15 (2026-11-20)

- feat: export trace diagnostics and record ACP failures
- Merge pull request #145 from daidr/release/alpha

## 0.1.0-alpha.14 (2026-11-20)

- fix: polish document history, comments, and table layout
- fix: improve agent tool reliability and project activity display
- Merge pull request #144 from daidr/release/alpha

## 0.1.0-alpha.13 (2026-11-19)

- fix: improve Antigravity and ACP provider compatibility
- Merge pull request #143 from daidr/release/alpha

## 0.1.0-alpha.12 (2026-11-19)

- feat: add in-app updates and download progress
- feat: support multiple writable project sources
- fix(web): scroll full-height conversations with a bottom fade
- fix: anchor conversation todos in the message timeline
- fix: streamline reference approvals and chat questions
- fix: keep discussion replies in their thread
- Merge pull request #142 from daidr/release/alpha

## 0.1.0-alpha.11 (2026-11-19)

- fix: list only registered Git worktrees
- fix(web): correct artifact history navigation and simplify settings
- Merge pull request #141 from daidr/release/alpha

## 0.1.0-alpha.10 (2026-11-19)

- perf: reduce live refresh, transcript, and storage costs
- feat: stream artifact edits and simplify review workflows
- feat: preview attachments and refine workspace controls
- Merge pull request #140 from daidr/release/alpha

## 0.1.0-alpha.9 (2026-11-18)

- fix: complete archived threads and improve agent reliability
- fix: estimate reclaimable workspace space on ext4
- Merge pull request #139 from daidr/release/alpha

## 0.1.0-alpha.8 (2026-11-18)

- fix: speed up workspace storage statistics
- feat: let users reorder projects in the sidebar
- fix: load data settings before worktree size scans
- fix: improve thread handoffs and model settings
- Merge pull request #138 from daidr/release/alpha

## 0.1.0-alpha.7 (2026-11-18)

- fix: coalesce MCP refreshes and avoid stale scheduler dispatch
- fix: finish closing detached MCP sessions on reconfigure
- fix: accept workspace root aliases on Windows and macOS
- feat: migrate the Ariadne backend to Go
- fix: clarify agent writing and polish composer controls
- feat: adopt dou-ui alpha.18 and refine conversation overlays
- Merge pull request #137 from daidr/release/alpha

## 0.1.0-alpha.6 (2026-11-16)

- fix: preserve Codex sessions and streamline conversation status
- fix: secure files, bound caches and isolate coordinator scheduling
- fix: prevent project badges from being clipped by long names
- Merge pull request #136 from daidr/release/alpha

## 0.1.0-alpha.5 (2026-11-16)

- feat: separate completion from merging and support external delivery
- Merge pull request #135 from daidr/release/alpha

## 0.1.0-alpha.4 (2026-11-16)

- ci: disable UPX for all release targets
- Merge pull request #134 from daidr/release/alpha

## 0.1.0-alpha.3 (2026-11-16)

- feat: add isolated dev sandbox and improve search settings
- Merge pull request #133 from daidr/release/alpha

## 0.1.0-alpha.2 (2026-11-13)

- fix: disable UPX for Windows release binaries
- Merge pull request #126 from daidr/release/alpha

## 0.1.0-alpha.1 (2026-11-12)

- 系统指令和工具的变化追加到对话末尾，请求开头在一段对话里保持不变 (#132)
- 长命令超时转后台，压缩时保留用户原话，MCP 工具多时按需查看，GPT 系模型用 apply_patch 改文件 (#131)
- 降低讨论串的 token 成本：大上下文模型的压缩线按至多 40 万算，搜索分组输出并修复漏搜隐藏目录，重复读取和重复检查报错只回一行 (#130)
- 讨论串和协调器在长任务中途也发消息，产物建好先发链接，消息改为多分段、母语者的自然语气 (#129)
- 文档里可以放画板：页面和服务端都认白板块，agent 用条目和连线写文档里的画板，服务端排版并按 id 合并 (#128)
- 消息列表和输入框之间去掉空带，消息直接滑到输入框边上；白板去掉外框和圆角，贴合所在的面板 (#127)
- 新建项目改为主区域里的表单：来源、名称和参考来源一起填，全部连通后才建项目 (#125)
