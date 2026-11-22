// @vitest-environment happy-dom
import type { ArtifactSummary, BackgroundView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { api } from '../api'
import { locale } from '../i18n'
import { stopBackground } from '../store'
import BackgroundCommand from './BackgroundCommand.vue'
import BackgroundTasks from './BackgroundTasks.vue'
import ConversationActivity from './ConversationActivity.vue'
import ConversationBar from './ConversationBar.vue'

const mocks = vi.hoisted(() => ({ openView: vi.fn() }))
vi.mock('../api', () => ({ api: { streamCommandOutput: vi.fn() } }))
vi.mock('../store', () => ({
  state: { stream: 'open' },
  stopBackground: vi.fn(),
  report: vi.fn(),
  endWait: vi.fn(),
}))
vi.mock('../opened-from', () => ({ useOpenView: () => mocks.openView }))

const commands: BackgroundView[] = [
  {
    id: 'cmd-1',
    command: 'bun run dev',
    startedAt: Date.now(),
    url: 'http://localhost:5173/',
    result: null,
  },
  { id: 'cmd-2', command: 'bun run test --watch', startedAt: Date.now(), url: null, result: null },
]
const wrappers: VueWrapper[] = []
async function* outputStream(
  command: BackgroundView,
  tail: string,
  signal?: AbortSignal,
): AsyncGenerator<{ command: BackgroundView; tail: string }> {
  yield { command, tail }
  await new Promise<void>((resolve) => {
    if (signal?.aborted) {
      resolve()
      return
    }
    signal?.addEventListener('abort', () => resolve(), { once: true })
  })
}

beforeEach(() => {
  locale.value = 'zh-CN'
  vi.mocked(api.streamCommandOutput).mockImplementation((_threadId, commandId, signal) => {
    const command = commands.find((item) => item.id === commandId) ?? commands[0]!
    return outputStream(command, 'Ready', signal)
  })
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  document.body.innerHTML = ''
  vi.useRealTimers()
  vi.clearAllMocks()
})
const global = { stubs: { DouCodeBlock: { props: ['code'], template: '<pre>{{ code }}</pre>' } } }

test('one task capsule opens details, switches output, and stops the selected task', async () => {
  const wrapper = mount(BackgroundTasks, {
    props: { conversationId: 'thr-1', commands },
    attachTo: document.body,
    global,
  })
  wrappers.push(wrapper)
  expect(wrapper.text()).toBe('2 个任务运行中')
  expect(api.streamCommandOutput).not.toHaveBeenCalled()
  await wrapper.get('button').trigger('click')
  await flushPromises()
  expect(api.streamCommandOutput).toHaveBeenCalledWith('thr-1', 'cmd-1', expect.any(AbortSignal))
  const firstSignal = vi.mocked(api.streamCommandOutput).mock.calls[0]![2]!
  const choice = [...document.querySelectorAll<HTMLButtonElement>('button')].find(
    (button) => button.textContent?.trim() === 'bun run test --watch',
  )!
  choice.click()
  await flushPromises()
  expect(firstSignal.aborted).toBe(true)
  expect(api.streamCommandOutput).toHaveBeenLastCalledWith(
    'thr-1',
    'cmd-2',
    expect.any(AbortSignal),
  )
  const stop = [...document.querySelectorAll<HTMLButtonElement>('button')].find(
    (button) => button.getAttribute('aria-label') === '停止',
  )!
  stop.click()
  await flushPromises()
  expect(stopBackground).toHaveBeenCalledWith('thr-1', 'cmd-2')
  await wrapper.get('button').trigger('click')
  await flushPromises()
  expect(vi.mocked(api.streamCommandOutput).mock.calls.at(-1)![2]!.aborted).toBe(true)
})

test('task output streams without polling and closing aborts the active stream', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  let push!: (value: { command: BackgroundView; tail: string }) => void
  vi.mocked(api.streamCommandOutput).mockImplementation((_threadId, _commandId, signal) =>
    (async function* () {
      const next = await new Promise<{ command: BackgroundView; tail: string }>((resolve) => {
        push = resolve
      })
      if (!signal?.aborted) yield next
    })(),
  )
  const wrapper = mount(BackgroundCommand, {
    props: { threadId: 'thr-1', command: commands[0]! },
    global,
  })
  wrappers.push(wrapper)
  await vi.advanceTimersByTimeAsync(5000)
  expect(api.streamCommandOutput).toHaveBeenCalledTimes(1)
  const signal = vi.mocked(api.streamCommandOutput).mock.calls[0]![2]!
  wrapper.unmount()
  expect(signal.aborted).toBe(true)
  push({ command: commands[0]!, tail: 'late output' })
  await flushPromises()
  await vi.advanceTimersByTimeAsync(5000)
  expect(api.streamCommandOutput).toHaveBeenCalledTimes(1)
})

test('changes and artifact capsules open their corresponding views', async () => {
  const artifact = { id: 'art-1', kind: 'document', title: '验收记录' } as ArtifactSummary
  const wrapper = mount(ConversationBar, {
    props: {
      conversationId: 'thr-1',
      changes: { threadId: 'thr-1', totals: { added: 3, removed: 1, files: 1 } },
      artifacts: [artifact],
      background: [],
    },
    attachTo: document.body,
    global,
  })
  wrappers.push(wrapper)
  await wrapper.findAll('button')[0]!.trigger('click')
  expect(mocks.openView).toHaveBeenCalledWith({ kind: 'changes', threadId: 'thr-1' })
  await wrapper.findAll('button')[1]!.trigger('click')
  await flushPromises()
  const item = [...document.querySelectorAll<HTMLButtonElement>('button')].find(
    (button) => button.textContent?.trim() === artifact.title,
  )!
  item.click()
  await flushPromises()
  expect(mocks.openView).toHaveBeenCalledWith({ kind: 'artifact', id: 'art-1' })
})

test('changes capsule hides zero-file diffs and falls back to label alongside a pull request', async () => {
  const pullRequest = {
    number: 42,
    url: 'https://github.com/example/repo/pull/42',
    title: 'Fix diff entry',
    state: 'open' as const,
    headSha: 'abc1234',
    checks: { state: 'success' as const, failed: [] },
  }
  const wrapper = mount(ConversationBar, {
    props: {
      conversationId: 'thr-1',
      changes: { threadId: 'thr-1', totals: { added: 0, removed: 0, files: 0 } },
      artifacts: [],
      background: [],
      thread: { id: 'thr-1', pullRequest },
    },
    attachTo: document.body,
    global,
  })
  wrappers.push(wrapper)
  expect(wrapper.find('button[aria-label="改动"]').exists()).toBe(false)
  expect(wrapper.text()).toContain('#42')

  await wrapper.setProps({ changes: { threadId: 'thr-1', totals: null } })
  const diffButton = wrapper.get('button[aria-label="改动"]')
  expect(diffButton.text()).toContain('改动')
  await diffButton.trigger('click')
  expect(mocks.openView).toHaveBeenCalledWith({ kind: 'changes', threadId: 'thr-1' })
})

test('the status slot survives working, queued, and idle states', async () => {
  const wrapper = mount(ConversationActivity, {
    props: {
      conversationId: 'thr-1',
      working: true,
      queued: false,
      live: null,
      activity: '正在检查',
      elapsed: '0:02',
      escArmed: false,
      wait: null,
    },
    global,
  })
  wrappers.push(wrapper)
  const slot = wrapper.element
  expect(wrapper.text()).toContain('正在检查')
  await wrapper.setProps({ working: false, queued: true })
  expect(wrapper.text()).toContain('排队中')
  await wrapper.setProps({ queued: false })
  expect(wrapper.text()).toBe('')
  expect(wrapper.element).toBe(slot)
})
