import { afterEach, beforeEach, expect, test } from 'vitest'
import { Ariadne } from './ariadne'
import {
  MAX_PROJECT_INSTRUCTIONS,
  parseProjectSettings,
  storedProjectSettings,
} from './project-settings'

let app: Ariadne
let projectId: string
let threadId: string
beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
  projectId = app.createProject('交付').id
  threadId = app.createThread(projectId, { title: '准备预发布' }).id
  app.recordWorkspace(threadId, { label: '/work', branch: 'task/a', base: 'pre-release' })
})
afterEach(() => app.close())
const report = { kind: 'ready' as const, headline: '变更已交付', summary: '已提交并创建集成提票单' }
const external = (review = true) =>
  app.setProjectSettings(
    projectId,
    {
      delivery: { mode: 'external', instructions: '创建集成提票单' },
      review: { enabled: review },
    },
    [],
  )

test('completion accepts a delivery while preserving its unmerged branch', () => {
  app.reportThread(threadId, report)
  expect(app.completeThread(threadId)).toMatchObject({
    stage: 'done',
    merged: null,
    workspace: { branch: 'task/a', removed: false },
  })
  app.postMessage(projectId, { conversationId: threadId, author: 'user', content: '再补一个场景' })
  expect(app.getThread(threadId).stage).toBe('queued')
})

test('external handover without AI review finishes without a merge or another confirmation', () => {
  external(false)
  expect(app.reportThread(threadId, report, 'h1', { reviewable: true, head: 'h1' })).toMatchObject({
    stage: 'done',
    lastReport: 'done',
    merged: null,
  })
})

test('external changes still get AI review and finish automatically only after a pass', () => {
  external()
  const pending = app.reportThread(threadId, report, 'h1', { reviewable: true, head: 'h1' })
  expect(pending.stage).toBe('working')
  app.submitReview(pending.review!.reviewerId, {
    verdict: 'pass',
    summary: '通过',
    comments: [],
    headline: '审阅通过',
  })
  expect(app.getThread(threadId)).toMatchObject({
    stage: 'done',
    merged: null,
    review: { rounds: 1, reviewing: false },
  })
  expect(app.getProject(projectId).needsMe).toBe(0)
})

test('external review comments are never silently accepted', () => {
  external()
  const pending = app.reportThread(threadId, report, 'h1', { reviewable: true, head: 'h1' })
  app.submitReview(pending.review!.reviewerId, {
    verdict: 'changes',
    summary: '需要修复',
    headline: '有问题',
    comments: [{ path: 'a.ts', line: 1, quote: '', kind: 'bug', body: '处理空值' }],
  })
  expect(app.getThread(threadId).stage).not.toBe('done')
})

test('stopping an external reviewer does not complete the delivery', () => {
  external()
  const pending = app.reportThread(threadId, report, 'h1', { reviewable: true, head: 'h1' })
  app.stopThread(pending.review!.reviewerId, { reason: '暂停审阅', by: 'user' })
  expect(app.getThread(threadId).stage).not.toBe('done')
})

test('old projects keep their delivery and cleanup defaults; new policies are validated', () => {
  const old = storedProjectSettings(
    JSON.stringify({ workspace: { setup: 'make', shell: 'auto', check: '' } }),
  )
  expect(old.delivery).toEqual({ mode: 'merge', instructions: '' })
  expect(old.workspace.retention).toBe('auto')
  const settings = parseProjectSettings(
    {
      delivery: { mode: 'external', instructions: ' 创建提票单 ' },
      workspace: { retention: 'keep' },
    },
    [],
  )
  expect(storedProjectSettings(JSON.stringify(settings))).toEqual(settings)
  expect(settings.delivery.instructions).toBe('创建提票单')
  expect(() => parseProjectSettings({ workspace: { retention: 'discard' } }, [])).toThrow()
  expect(() => parseProjectSettings({ delivery: { mode: 'discard' } }, [])).toThrow()
})

test('delivery edits preserve omitted fields and every unrelated project setting', () => {
  app.setProjectSettings(
    projectId,
    {
      instructions: '用中文',
      tools: ['fetch'],
      budget: { thread: 1000, project: 5000 },
      workspace: { retention: 'keep', setup: 'bun install', auto: { setup: false, check: true } },
      review: { enabled: true },
      autopilot: { enabled: true, method: 'merge' },
    },
    ['fetch'],
  )
  const before = app.getProject(projectId).settings
  const delivery = { mode: 'external' as const, instructions: '创建集成提票单' }
  expect(app.setProjectDelivery(projectId, delivery).settings).toEqual({ ...before, delivery })
  expect(
    app.setProjectDelivery(projectId, { instructions: '  改交预发布分支  ' }).settings,
  ).toEqual({
    ...before,
    delivery: { ...delivery, instructions: '改交预发布分支' },
  })
  expect(app.setProjectDelivery(projectId, { mode: 'merge' }).settings.delivery).toEqual({
    mode: 'merge',
    instructions: '改交预发布分支',
  })
  expect(app.setProjectDelivery(projectId, { instructions: '' }).settings.delivery).toEqual({
    mode: 'merge',
    instructions: '',
  })
  const seq = app.lastSeq()
  app.setProjectDelivery(projectId, { mode: 'merge', instructions: '' })
  expect(app.lastSeq()).toBe(seq)
})

test('invalid delivery edits fail without changing the project', () => {
  external()
  const before = app.getProject(projectId).settings
  const seq = app.lastSeq()
  for (const input of [
    { mode: 'discard' },
    { mode: null },
    { instructions: null },
    { instructions: false },
    { instructions: 'x'.repeat(MAX_PROJECT_INSTRUCTIONS + 1) },
  ]) {
    expect(() => app.setProjectDelivery(projectId, input)).toThrow()
    expect(app.getProject(projectId).settings).toEqual(before)
    expect(app.lastSeq()).toBe(seq)
  }
})
