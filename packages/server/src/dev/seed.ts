import { Ariadne } from '@ariadne/core'
import { PluginHost, WorkspaceManager } from '@ariadne/agent'
import local from '@ariadne/plugin-local'
import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { MOCK_MODELS } from './mock-api'

const git = async (cwd: string, ...args: string[]) => {
  const child = Bun.spawn(['git', '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=', ...args], {
    cwd,
    windowsHide: true,
    stdout: 'pipe',
    stderr: 'pipe',
    env: {
      ...process.env,
      GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null',
      GIT_CONFIG_NOSYSTEM: '1',
    },
  })
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ])
  if (code) throw new Error(`Sandbox git ${args[0]}: ${stderr}`)
  return stdout.trim()
}

/** Only writes under the new sandbox home; no user repository or settings are used. */
export async function seedSandbox(home: string, baseUrl: string) {
  await writeFile(
    join(home, 'config.json'),
    JSON.stringify(
      {
        locale: 'zh-CN',
        search: {
          provider: 'deepseek',
          services: {
            tavily: { baseUrl, apiKey: 'dev-only' },
            deepseek: { baseUrl, apiKey: 'dev-only' },
          },
        },
        models: {
          providers: [
            {
              id: 'dev',
              kind: 'openai-compatible',
              name: '开发模拟 API',
              settings: { baseUrl, apiKey: 'dev-only' },
            },
          ],
          models: MOCK_MODELS.map((model) => ({
            id: model,
            provider: 'dev',
            model,
            contextTokens: 128_000,
          })),
          roles: { thread: { model: 'mock', effort: null, fallback: null } },
        },
      },
      null,
      2,
    ),
    { mode: 0o600 },
  )
  const repo = join(home, 'example-repo')
  await mkdir(repo)
  await git(repo, 'init', '-q', '-b', 'main')
  await git(repo, 'config', 'user.name', 'Ariadne Dev')
  await git(repo, 'config', 'user.email', 'dev@example.invalid')
  await writeFile(join(repo, 'README.md'), '# 开发沙箱\n\n这里是一个独立的示例仓库。\n')
  await git(repo, 'add', '.')
  await git(repo, 'commit', '-qm', 'Initial sandbox')

  const app = new Ariadne({ path: join(home, 'ariadne.sqlite') })
  const host = new PluginHost(join(home, 'plugins'), [home])
  await host.load(local)
  const workspaces = new WorkspaceManager(app, host)
  try {
    const project = app.createProject('开发沙箱', {
      source: { kind: 'local-directory', settings: { path: repo } },
    })
    const thread = app.createThread(project.id, {
      title: '修复讨论串卡片标题被变更统计、合并和还要改按钮挤压的问题',
    })
    const workspace = await workspaces.forThread(project.id, thread.id)
    await workspace.writeFile('layout.txt', '标题独立显示\n变更和审核操作另起一行\n窄屏允许换行\n')
    await workspace.exec(
      'git add . && git -c user.name="Ariadne Dev" -c user.email=dev@example.invalid commit -qm "Improve thread layout"',
    )
    const turn = app.startTurn(thread.id, 'mock')
    app.recordTurnChanges(thread.id, turn, {
      from: await git(repo, 'rev-parse', 'HEAD'),
      to: (await workspace.exec('git rev-parse HEAD')).stdout.trim(),
      files: [{ path: 'layout.txt', previousPath: null, status: 'added', added: 3, removed: 0 }],
      branch: { files: 1, added: 3, removed: 0 },
    })
    app.reportThread(thread.id, {
      kind: 'ready',
      headline: '布局调整已完成，等待查看并合并',
      summary: '标题和操作分行，窄屏下自动换行。',
    })
    app.finishTurn(thread.id, turn, 'completed')
    const research = app.createThread(project.id, {
      title: '检查长标题在侧栏、聊天内容区和窄屏中的可读性',
    })
    app.reportThread(research.id, {
      kind: 'ready',
      headline: '浏览器检查结果待确认',
      summary: '检查标题完整显示和按钮可操作性。',
    })
    const done = app.createThread(project.id, { title: '配置独立临时 settings 和本地模拟 AI 接口' })
    app.reportThread(done.id, {
      kind: 'done',
      headline: '开发环境已就绪',
      summary: '可以发送消息验证真实接口链路。',
    })
    app.postMessage(project.id, {
      conversationId: project.coordinatorId,
      author: 'coordinator',
      content:
        '这里是开发沙箱。下面的讨论串可打开、查看变更和合并；发送消息可测试模拟 AI 的流式回复。',
      blocks: [thread, research, done].map(({ id }) => ({ type: 'thread', threadId: id })),
    })
    // Consume the fixture reports before starting the real scheduler: the first live
    // request should answer the developer's message rather than echoing setup notices.
    const conversation = app.getConversation(project.coordinatorId)
    const coordinatorTurn = app.startTurn(project.coordinatorId, 'mock')
    app.recordStep(project.coordinatorId, coordinatorTurn, {
      text: '',
      reasoning: [],
      toolCalls: [],
      sawSeq: conversation.pendingSeq,
      usage: null,
      input: { system: '', tools: '[]', call: '{}', images: false },
      timing: { startedAt: Date.now(), firstOutputAt: null },
    })
    app.finishTurn(project.coordinatorId, coordinatorTurn, 'completed')
    return project.id
  } finally {
    await workspaces.dispose()
    await host.unloadAll()
    app.close()
  }
}
