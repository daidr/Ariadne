import { Ariadne } from '@ariadne/core'
import { PluginHost, WorkspaceManager } from '@ariadne/agent'
import local from '@ariadne/plugin-local'
import { openAICompatibleProvider } from '@ariadne/plugin-openai-compatible'
import type { ChatMessage, ModelStreamPart } from '@ariadne/plugin-sdk'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, expect, test } from 'vitest'
import { createHandler } from '../app'
import { loadUserConfig } from '../settings'
import { MOCK_MODELS, startMockApi } from './mock-api'
import { seedSandbox } from './seed'

const mock = startMockApi()
const baseUrl = `http://127.0.0.1:${mock.port}/v1`
afterAll(() => mock.stop(true))

async function collect(
  model = 'mock',
  messages: ChatMessage[] = [{ role: 'user', content: '测试消息' }],
  tools = true,
) {
  const parts: ModelStreamPart[] = []
  for await (const part of openAICompatibleProvider.stream(
    { baseUrl, apiKey: 'dev-only' },
    {
      model,
      messages,
      tools: tools
        ? [{ name: 'send_message', description: 'Send a message', parameters: { type: 'object' } }]
        : [],
    },
  ))
    parts.push(part)
  return parts
}

test('the real provider reads models, streamed tool arguments, usage and the final stop', async () => {
  const models = (await (await fetch(`${baseUrl}/models`)).json()) as { data: { id: string }[] }
  expect(models.data.map(({ id }) => id)).toEqual([...MOCK_MODELS])
  const parts = await collect()
  const call = parts.find((part) => part.type === 'tool-call')!
  expect(call).toMatchObject({ name: 'send_message' })
  expect(JSON.parse(call.arguments).text).toContain('测试消息')
  expect(parts.some((part) => part.type === 'tool-input')).toBe(true)
  expect(parts).toContainEqual(expect.objectContaining({ type: 'usage', input: 100, output: 30 }))
  expect(parts.at(-1)).toMatchObject({ type: 'finish', reason: 'tool-calls' })
  const followup = await collect('mock', [
    { role: 'user', content: '测试消息' },
    { role: 'assistant', content: '', toolCalls: [call] },
    { role: 'tool', toolCallId: call.id, content: 'Sent' },
  ])
  expect(followup.some((part) => part.type === 'tool-call')).toBe(false)
  expect(followup.at(-1)).toMatchObject({ type: 'finish', reason: 'stop' })
  const plain = await collect('mock', undefined, false)
  expect(
    plain
      .filter((part) => part.type === 'text')
      .map((part) => part.text)
      .join(''),
  ).toContain('测试消息')
})

test('the error model reaches the provider as a non-retryable API failure', async () => {
  const parts = await collect('mock-error')
  expect(parts).toContainEqual(
    expect.objectContaining({
      type: 'finish',
      reason: 'error',
      error: expect.stringContaining('开发模拟'),
    }),
  )
  expect(
    parts.find((part) => part.type === 'finish' && part.reason === 'error'),
  ).not.toHaveProperty('retryable', true)
})

test('the sandbox has isolated settings and actual changes that can be merged', async () => {
  const home = await mkdtemp(join(tmpdir(), 'ariadne-dev-test-'))
  let app: Ariadne | undefined
  let workspaces: WorkspaceManager | undefined
  let host: PluginHost | undefined
  try {
    const projectId = await seedSandbox(home, baseUrl)
    const settings = loadUserConfig(join(home, 'config.json'))
    expect(settings.models.providers[0]?.settings).toEqual({ baseUrl, apiKey: 'dev-only' })
    expect(settings.models.roles.thread.model).toBe('mock')
    app = new Ariadne({ path: join(home, 'ariadne.sqlite') })
    const threads = app.listThreads(projectId)
    expect(threads.map((item) => item.stage).toSorted()).toEqual(['done', 'review', 'review'])
    const thread = threads.find((item) => item.workspace?.branch)!
    expect(thread.changes).toEqual({ files: 1, added: 3, removed: 0 })
    expect(app.getConversation(app.getProject(projectId).coordinatorId).pendingSeq).toBe(0)
    host = new PluginHost(join(home, 'plugins'), [home])
    await host.load(local)
    workspaces = new WorkspaceManager(app, host)
    const handle = createHandler({ app, token: 'test', workspaces })
    const merged = await handle(
      new Request(`http://localhost/api/threads/${thread.id}/merge`, {
        method: 'POST',
        headers: { authorization: 'Bearer test', 'content-type': 'application/json' },
        body: JSON.stringify({ message: '' }),
      }),
    )
    expect(await merged.json()).toMatchObject({ outcome: { kind: 'merged' } })
    expect(await readFile(join(home, 'example-repo/layout.txt'), 'utf8')).toContain('标题独立显示')
  } finally {
    await workspaces?.dispose()
    await host?.unloadAll()
    app?.close()
    await rm(home, { recursive: true, force: true })
  }
}, 30_000)
