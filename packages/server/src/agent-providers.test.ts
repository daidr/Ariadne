import { Ariadne } from '@ariadne/core'
import { Scheduler, type ResolvedModel } from '@ariadne/agent'
import { agentProvider, claudeCode, ToolBridge, type AgentKind } from '@ariadne/plugin-agents'
import type { ModelProvider } from '@ariadne/plugin-sdk'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, expect, test } from 'vitest'

const fakeClaude = `${process.execPath} ${join(import.meta.dirname, '../../plugin-agents/src/fakes/fake-claude.ts')}`

let cleanup: (() => Promise<void>) | null = null
afterEach(async () => {
  await cleanup?.()
  cleanup = null
})

// The agent loop with an agent program for a model: its steps, results and later turns all
// go to one running session, as long as the conversation's history goes on from it.
test('a coordinator runs on an agent program across steps and turns', async () => {
  const app = new Ariadne({ path: ':memory:' })
  const dir = mkdtempSync(join(tmpdir(), 'ariadne-agent-loop-'))
  const bridge = new ToolBridge()
  const provider = agentProvider(claudeCode as AgentKind<unknown>, {
    dataDir: dir,
    sandbox: { kind: null, wrap: (argv) => argv },
    bridge,
  })
  const model: ResolvedModel = {
    provider: provider as ModelProvider<never>,
    settings: { command: fakeClaude },
    model: 'default',
    label: 'claude-code / default',
  }
  const scheduler = new Scheduler({ app, resolveModel: () => model, locale: () => 'zh-CN' })
  scheduler.start()
  cleanup = async () => {
    await scheduler.stop()
    await provider.dispose()
    bridge.stop()
    app.close()
    rmSync(dir, { recursive: true, force: true })
  }

  const project = app.createProject('P')
  app.postMessage(project.id, {
    conversationId: project.coordinatorId,
    author: 'user',
    content: 'CALL update_todo {"items":[{"label":"读需求","status":"doing"}]}',
  })
  await scheduler.idle()
  expect(app.getTodo(project.coordinatorId).items).toEqual([{ label: '读需求', status: 'doing' }])

  app.postMessage(project.id, {
    conversationId: project.coordinatorId,
    author: 'user',
    content: 'again',
  })
  await scheduler.idle()
  const replies = app
    .listMessages(project.coordinatorId)
    .filter((m) => m.author === 'coordinator')
    .map((m) => m.content)
  expect(replies).toHaveLength(2)
  expect(replies[0]).toMatch(/^turn 1: /)
  // Same program, second turn: it was handed only the new message.
  expect(replies[1]).toMatch(/^turn 2: {2}len 5 /)
  expect(provider.sessions()).toBe(1)
  // Its usage and the cost it reported are the conversation's.
  expect(app.getConversation(project.coordinatorId).usage.input).toBeGreaterThan(0)
})
