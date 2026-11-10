import { Ariadne } from '@ariadne/core'
import { Scheduler, UNSENT_HINT, type ResolvedModel } from '@ariadne/agent'
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

  const again = 'CALL send_message {"text":"二"}'
  const project = app.createProject('P')
  app.postMessage(project.id, {
    conversationId: project.coordinatorId,
    author: 'user',
    content:
      'CALL update_todo {"items":[{"label":"读需求","status":"doing"}]}\nCALL send_message {"text":"一"}',
  })
  await scheduler.idle()
  expect(app.getTodo(project.coordinatorId).items).toEqual([{ label: '读需求', status: 'doing' }])

  app.postMessage(project.id, {
    conversationId: project.coordinatorId,
    author: 'user',
    content: again,
  })
  await scheduler.idle()
  // It speaks through send_message; what it writes after is its steps' text alone.
  const replies = app
    .listMessages(project.coordinatorId)
    .filter((m) => m.author === 'coordinator')
    .map((m) => m.content)
  expect(replies).toEqual(['一', '二'])
  const ends = app
    .listTranscript(project.coordinatorId)
    .flatMap((e) => (e.kind === 'step' && !e.toolCalls.length ? [e.text] : []))
  // Each turn ends in text, which is not sent: it is told so once a turn, and goes on.
  expect(ends).toHaveLength(4)
  expect(ends[0]).toMatch(/^turn 1: /)
  expect(ends[1]).toMatch(new RegExp(`^turn 2: .* len ${UNSENT_HINT.length} `))
  // Same program, next turn: it was handed only what came after its last step: that its
  // text there went unsent, and the new message with its id.
  const handed = UNSENT_HINT.length + 2 + again.length + 52
  expect(ends[2]).toMatch(new RegExp(`^turn 3: .* len ${handed} `))
  expect(provider.sessions()).toBe(1)
  // Its usage and the cost it reported are the conversation's.
  expect(app.getConversation(project.coordinatorId).usage.input).toBeGreaterThan(0)
  expect(app.getConversation(project.coordinatorId).usage.cost.USD).toBeCloseTo(0.04)
})
