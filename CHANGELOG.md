# 更新日志

## 0.1.0-alpha.26 (2026-11-27)

- feat(web): 精简顶栏，将产物画廊与协作时间线收纳至 Logo 菜单 (#280)
- refactor(pulse): 对齐 DouUI 组件并简化协作时间线 UI/UX (#277)
- fix(web): 修复产物画廊速览信息面板左侧标签换行问题 (#278)
- Merge pull request #266 from daidr/release/alpha

## 0.1.0-alpha.25 (2026-11-27)

- feat(whiteboard): 支持画板与文档内嵌画板编辑颜色与无边框 (#276)
- feat(pulse): 实现右侧详情抽屉、跨项目切换与窄屏降级视图 (#275)
- chore(web): 升级 DouUI 至 0.1.0-alpha.30 并适配宽表滚动视口 (#274)
- feat(pulse): 实现桌面端 Pulse 主视图与实时泳道画布 (#272)
- feat: support custom env for CLI providers and per-project model overrides (#271)
- fix(workspaces): 优化无 commit 的 Git 目录与非 Git 目录的工作区处理 (#273)
- refactor(runner): 移除协调器的 research 工具以保持不阻塞 (#270)
- feat: add cross-project artifact gallery (/gallery) (#267)
- feat(pulse): 实现 Pulse 后端事件投影、30 天滚动清理与 GetPulse 接口 (#268)
- feat(threads): 支持调研讨论串汇报空闲态并优化已完成讨论串的恢复交互 (#264)
- fix(artifacts): 修复原型参数面板恢复默认按钮显隐导致的标题高度跳变 (#265)
- Merge pull request #253 from daidr/release/alpha

## 0.1.0-alpha.24 (2026-11-26)

- chore(web): 升级 DouUI 至 0.1.0-alpha.29 并修复 MCP 工具行分段控件收缩问题 (#263)
- fix(artifacts): 原型参数面板长枚举选项改用可折行单选标签组避免截断 (#261)
- fix(collab): support CJK-friendly emphasis and strikethrough in document Markdown (#257)
- fix(artifacts): 修复原型参数发送中切换产物失败后残留脏参数与丢失重试状态 (#262)
- feat(artifacts,i18n): 原型参数调整改为默认本地预览并支持手动发送 (#259)
- feat(web,i18n): 产物视图点击引用产物改为就地切换并支持 Header 前进后退 (#260)
- fix(artifacts,runner): 修复画板原型节点与决策选项卡片的重叠和溢出问题 (#258)
- chore(web): 升级 DouUI 至 0.1.0-alpha.28 并清理浮层与选择器 scoped 样式兼容 (#256)
- refactor(runner): avoid duplicating thread cards and inline thread links in prompts (#255)
- feat: 重设计自动重启空闲等待逻辑并修复讨论串排空阻塞与并发边界 (#254)
- feat(web,i18n): 文档支持左侧目录及宽表左滚碰撞自动收起 (#249)
- feat(notifications,plugins,web): 实现服务端离线通知三通道与通知设置页 (#252)
- fix(artifacts): 隐藏已提交且无变更的文档内联回答提交栏 (#250)
- feat(core,server,web): 支持跨项目“需我关注”聚合队列与 ⌘K 跨项目搜索 (#251)
- Merge pull request #247 from daidr/release/alpha

## 0.1.0-alpha.23 (2026-11-25)

- Merge pull request #248 from daidr/ariadne/5ef7c6071e43
- fix(web): 修复设置弹窗模型行按钮换行与展开详情表单排版
- chore(web): 升级 DouUI 至 0.1.0-alpha.27 并接入 Dock 合并与按需截断提示 (#246)
- feat(runner,local,agentruntime,settings): 落地 7 项 Agent Harness 工具与运行时优化 (#245)
- Merge pull request #230 from daidr/release/alpha

## 0.1.0-alpha.22 (2026-11-24)

- fix(local,runner,workspaces): 修复多仓内嵌 Submodule 与无共同祖先场景 Diff 及清理异常 (#244)
- perf(web): virtualize long lists, lazy-render board thumbnails, and optimize runtime hotspots (#243)
- perf(web,i18n): 收敛前端首屏分包并剥离非首屏图表、样式与语言包依赖 (#242)
- chore(web): 升级 DouUI 至 0.1.0-alpha.25 并接入 Worker 池与 Shiki 离主线程高亮 (#241)
- perf(collab): 优化 Yjs 协作房间增量编码、草稿复用与看板变更检测 (#238)
- fix(web): 统一修复前端 UI/UX 排查出的 12 项交互、视觉与响应式问题 (#235)
- perf(repowatch,core): PR 并发限流轮询与 GetProjectDetail 跨整点增量优化 (#240)
- perf(realtime,eventstore,web): 消除推送重复 JSON 编解码并支持增量草稿与项目级唤醒 (#239)
- perf(web): deduplicate Shiki runtime and grammar chunks between main thread and worker (#236)
- perf(server,files,web): 静态资源启用 Gzip/Brotli 压缩与 ETag 缓存，优化附件 304 与 Range 传输 (#237)
- perf(core,eventstore,scheduler): 优化搜索按需查元数据、SQLite PRAGMA 与调度器事件过滤 (#234)
- feat(web): 引入 Web Worker 池异步处理代码语法高亮 (#233)
- fix(web): 优化计划图空状态文案并移除中间重复的显示已完成按钮 (#232)
- fix(runner,transcript,agentruntime): 历史压缩与回放截断后保留当前 Todo List 上下文 (#231)
- feat(web): 优化聊天框气泡栏滚轮横向滚动与两侧边缘渐变 (#229)
- Merge pull request #228 from daidr/release/alpha

## 0.1.0-alpha.21 (2026-11-24)

- fix(runner,acp): 收紧摘要重放预算并增加跨压缩无进展循环护栏 (#227)
- Merge pull request #225 from daidr/release/alpha

## 0.1.0-alpha.20 (2026-11-24)

- feat(workspaces): 支持多仓库回合快照与改动页分组 Diff 展示 (#226)
- feat(artifacts): whiteboard decision groups, visible feedback bar, and withdraw (#218)
- fix(workspaces): 纳入 CoW 同级工作区存储统计并保护内嵌 .git 多仓工作区 (#224)
- Merge pull request #214 from daidr/release/alpha

## 0.1.0-alpha.19 (2026-11-24)

- chore(web): 升级 DouUI 至 0.1.0-alpha.24 并接入 Diff 悬浮行动作 (#223)
- fix(web): 文件无审查评论时不再占位空的 review-comments 行号槽 (#221)
- fix(web): 网络连接恢复时仅关闭断连 loading toast，不再弹出成功提示 (#222)
- fix(web): 改用 max() 计算底部安全区留白以消除移动端与 PWA 输入框下方多余空隙 (#220)
- fix(settings): 优化工作区列表响应式布局并修复窄屏文字重叠 (#219)
- refactor(runner): 按协调器与讨论串角色拆分 update_todo 工具描述与提示词 (#217)
- feat(runner): support proactive coordinator routing for unrelated thread messages (#216)
- refactor(settings): 移除每个讨论串与整个项目的用量上限设置 (#215)
- fix(windows): 非管理员权限下改用 HKCU Run 注册用户级自启后台服务 (#212)
- chore(web): 升级 DouUI 至 0.1.0-alpha.23 并补接用户消息发送中状态指示 (#213)
- Merge pull request #204 from daidr/release/alpha

## 0.1.0-alpha.18 (2026-11-23)

- feat(cli): cross-platform installers, global CLI, service management, and safe restart (#193)
- feat(web): 为用户发送的消息实现乐观更新与平滑合并 (#210)
- feat(runner): restore parallel execution for adjacent read-only tool calls (#211)
- feat(web): 为设置与项目设置弹窗的标签栏添加图标并适配移动端全屏布局 (#209)
- feat(runner): 补回文件读写状态追踪、重复读去重与检查失败折叠 (#208)
- feat(runner): restore coordinator look limits, tighten research, and add todo reminders (#207)
- refactor(web): 移除讨论串次 header 并将操作收拢至更多菜单与底部状态槽 (#206)
- feat(settings): 协调器在沿用支持低思考强度的模型时默认选用低思考强度 (#203)
- Merge pull request #200 from daidr/release/alpha

## 0.1.0-alpha.17 (2026-11-23)

- fix(runner,acp): 校准压缩触发判断、保底重放摘要并回收取消会话的 ACP 用量 (#202)
- refactor(runner): tighten coordinator delegation prompt (#201)
- feat(web): enable Shiki syntax highlighting for chat Markdown code blocks (#199)
- Merge pull request #147 from daidr/release/alpha

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
