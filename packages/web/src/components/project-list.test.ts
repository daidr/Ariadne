// @vitest-environment happy-dom
import type { ProjectActivity, ProjectView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { api } from '../api'
import { douLocale, locale } from '../i18n'
import { state } from '../store'
import ProjectList from './ProjectList.vue'

type ProjectResponse = Awaited<ReturnType<typeof api.getProject>>

function makeProjectSummary(id: string, name: string): ProjectView {
  return {
    id,
    name,
    coordinatorId: `crd_${id}`,
    needsMe: 0,
    runningTurns: 0,
    hasWork: false,
    updatedAt: Date.now(),
  } as unknown as ProjectView
}

function makeProjectDetail(id: string, name: string): ProjectResponse {
  return {
    project: {
      id,
      name,
      coordinatorId: `crd_${id}`,
      source: null,
      fileSources: [],
      references: [],
      mcp: [],
    },
    threads: [],
    needsMe: [],
    hasTracker: false,
    sync: { lastSeq: 0, cursor: '' },
  } as unknown as ProjectResponse
}

function visiblePopovers() {
  return [...document.querySelectorAll<HTMLElement>('.dou-popover')].filter(
    (el) => el.style.display !== 'none',
  )
}

function tapElement(el: HTMLElement) {
  el.dispatchEvent(
    new PointerEvent('pointerdown', { bubbles: true, cancelable: true, pointerType: 'touch' }),
  )
  el.dispatchEvent(
    new PointerEvent('pointerup', { bubbles: true, cancelable: true, pointerType: 'touch' }),
  )
  el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, detail: 1 }))
}

const wrappers: VueWrapper[] = []

function mountProjectList(options: { compact?: boolean; active?: boolean } = {}) {
  const compact = ref(options.compact ?? false)
  const active = ref(options.active ?? true)
  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () =>
          h(ProjectList, { compact: compact.value, active: active.value }),
        )
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return { wrapper, compact, active }
}

describe('ProjectList activity popover behavior', () => {
  let getProjectActivity: ReturnType<typeof vi.spyOn>
  let getProject: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    vi.useFakeTimers()
    locale.value = 'zh-CN'
    state.projects = [
      makeProjectSummary('prj_1', 'Ariadne'),
      makeProjectSummary('prj_2', 'Sandbox'),
    ]
    state.currentId = 'prj_1'
    state.detail = makeProjectDetail('prj_1', 'Ariadne')
    state.starting = false
    state.workspaceLevel = 'projects'
    state.main = { kind: 'conversation' }

    const activity: ProjectActivity = {
      coordinator: { status: 'idle' },
      threads: [],
    }
    getProjectActivity = vi.spyOn(api, 'getProjectActivity').mockResolvedValue(activity)
    getProject = vi
      .spyOn(api, 'getProject')
      .mockImplementation(async (id) =>
        makeProjectDetail(id, id === 'prj_2' ? 'Sandbox' : 'Ariadne'),
      )
    vi.spyOn(api, 'getConversation').mockResolvedValue({
      conversation: null,
      messages: [],
      more: false,
      model: null,
    } as unknown as Awaited<ReturnType<typeof api.getConversation>>)
    vi.spyOn(api, 'getPlan').mockResolvedValue({ nodes: [], edges: [] } as unknown as Awaited<
      ReturnType<typeof api.getPlan>
    >)
    vi.spyOn(api, 'listArtifacts').mockResolvedValue({ artifacts: [] } as unknown as Awaited<
      ReturnType<typeof api.listArtifacts>
    >)
    vi.spyOn(api, 'listMemories').mockResolvedValue({ memories: [] } as unknown as Awaited<
      ReturnType<typeof api.listMemories>
    >)
  })

  afterEach(() => {
    while (wrappers.length) wrappers.pop()?.unmount()
    document.body.innerHTML = ''
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  test('does not open activity popover when tapping a project in compact (mobile) mode', async () => {
    const { wrapper } = mountProjectList({ compact: true, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')

    expect(button.attributes('aria-expanded')).toBeUndefined()

    tapElement(button.element)
    await nextTick()
    vi.advanceTimersByTime(500)
    await flushPromises()

    expect(state.workspaceLevel).toBe('coordinator')
    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)
  })

  test('does not open activity popover on hover in compact mode', async () => {
    const { wrapper } = mountProjectList({ compact: true, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')

    button.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(600)
    await flushPromises()

    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)
  })

  test('does not open activity popover when tapping a project with touch on desktop layout', async () => {
    const { wrapper } = mountProjectList({ compact: false, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_2"]')

    tapElement(button.element)
    await nextTick()
    vi.advanceTimersByTime(500)
    await flushPromises()

    expect(state.currentId).toBe('prj_2')
    expect(getProject).toHaveBeenCalledWith('prj_2', undefined)
    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)

    // Subsequent mouse hover still works after a touch tap on hybrid devices.
    button.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(450)
    await flushPromises()

    expect(getProjectActivity).toHaveBeenCalledWith('prj_2', expect.any(AbortSignal))
    expect(visiblePopovers()).toHaveLength(1)
  })

  test('cancels pending hover timer when clicking a project on desktop', async () => {
    const { wrapper } = mountProjectList({ compact: false, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')

    button.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(200)
    button.element.dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, cancelable: true, pointerType: 'mouse' }),
    )
    button.element.dispatchEvent(
      new MouseEvent('click', { bubbles: true, cancelable: true, detail: 1 }),
    )
    await nextTick()
    vi.advanceTimersByTime(500)
    await flushPromises()

    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)
  })

  test('does not reopen activity popover on keyboard Enter click on desktop', async () => {
    const { wrapper } = mountProjectList({ compact: false, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')

    button.element.dispatchEvent(
      new MouseEvent('click', { bubbles: true, cancelable: true, detail: 0 }),
    )
    await nextTick()
    vi.advanceTimersByTime(500)
    await flushPromises()

    expect(state.workspaceLevel).toBe('coordinator')
    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)
  })

  test('shows activity popover on desktop mouse hover and closes it when sidebar becomes inactive', async () => {
    const { wrapper, active } = mountProjectList({ compact: false, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')

    button.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(450)
    await flushPromises()

    expect(getProjectActivity).toHaveBeenCalledWith('prj_1', expect.any(AbortSignal))
    expect(visiblePopovers()).toHaveLength(1)

    active.value = false
    await nextTick()
    await flushPromises()

    expect(visiblePopovers()).toHaveLength(0)
  })
})
