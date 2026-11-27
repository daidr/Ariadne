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

function mountProjectList(
  options: { compact?: boolean; active?: boolean; surface?: 'sidebar' | 'popover' } = {},
) {
  const compact = ref(options.compact ?? false)
  const active = ref(options.active ?? true)
  const surface = ref<'sidebar' | 'popover'>(options.surface ?? 'sidebar')
  const onSelect = vi.fn()
  const Host = defineComponent({
    setup() {
      return () =>
        h(DouConfigProvider, { locale: douLocale.value }, () =>
          h(ProjectList, {
            compact: compact.value,
            active: active.value,
            surface: surface.value,
            onSelect,
          }),
        )
    },
  })
  const wrapper = mount(Host, { attachTo: document.body })
  wrappers.push(wrapper)
  return { wrapper, compact, active, surface, onSelect }
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

  test('does not open activity popover when touch click arrives after pointerup flushes', async () => {
    const { wrapper } = mountProjectList({ compact: false, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_2"]')

    button.element.dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, cancelable: true, pointerType: 'touch' }),
    )
    await nextTick()
    window.dispatchEvent(
      new PointerEvent('pointerup', { bubbles: true, cancelable: true, pointerType: 'touch' }),
    )
    await nextTick()
    button.element.dispatchEvent(
      new MouseEvent('click', { bubbles: true, cancelable: true, detail: 1 }),
    )
    await nextTick()
    vi.advanceTimersByTime(500)
    await flushPromises()

    expect(state.currentId).toBe('prj_2')
    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)
  })

  test('restores keyboard focus and first mouse hover after a touch pointerup without click', async () => {
    const { wrapper } = mountProjectList({ compact: false, active: true })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')

    // 1. Touch down and release on window (e.g. drag-sort or slide-off) -> first mouse hover works.
    button.element.dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, cancelable: true, pointerType: 'touch' }),
    )
    await nextTick()
    window.dispatchEvent(
      new PointerEvent('pointerup', { bubbles: true, cancelable: true, pointerType: 'touch' }),
    )
    await nextTick()

    button.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(450)
    await flushPromises()

    expect(getProjectActivity).toHaveBeenCalledWith('prj_1', expect.any(AbortSignal))
    expect(visiblePopovers()).toHaveLength(1)

    // Dismiss via mouse leave, then repeat touch down + window pointerup -> keyboard focus works.
    button.element.dispatchEvent(
      new PointerEvent('pointerleave', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(200)
    await flushPromises()
    expect(visiblePopovers()).toHaveLength(0)

    getProjectActivity.mockClear()
    button.element.dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, cancelable: true, pointerType: 'touch' }),
    )
    await nextTick()
    window.dispatchEvent(
      new PointerEvent('pointerup', { bubbles: true, cancelable: true, pointerType: 'touch' }),
    )
    await nextTick()

    const matchesSpy = vi
      .spyOn(button.element, 'matches')
      .mockImplementation((selector) => selector === ':focus-visible')
    button.element.dispatchEvent(new FocusEvent('focus'))
    await flushPromises()
    matchesSpy.mockRestore()

    expect(getProjectActivity).toHaveBeenCalledWith('prj_1', expect.any(AbortSignal))
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

  test('disables hover activity popover and emits select when rendered inside a popover', async () => {
    const { wrapper, onSelect } = mountProjectList({
      compact: false,
      active: true,
      surface: 'popover',
    })
    const button = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_2"]')

    expect(button.attributes('aria-expanded')).toBeUndefined()
    button.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(600)
    await flushPromises()

    expect(getProjectActivity).not.toHaveBeenCalled()
    expect(visiblePopovers()).toHaveLength(0)

    await button.trigger('click')
    await nextTick()
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  test('virtualizes long project lists above 40 items and keeps keyboard reordering working', async () => {
    state.projects = Array.from({ length: 60 }, (_, index) =>
      makeProjectSummary(`prj_${index + 1}`, `Project ${index + 1}`),
    )

    const { wrapper } = mountProjectList({ compact: false, active: true })
    expect(wrapper.find('.dou-virtual-list').exists()).toBe(true)

    const renderedButtons = wrapper.findAll('button[data-project-id]')
    expect(renderedButtons.length).toBeGreaterThan(0)
    expect(renderedButtons.length).toBeLessThan(60)

    const first = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')
    await first.trigger('keydown', { key: 'ArrowDown', altKey: true })
    await nextTick()

    expect(state.projects[0]?.id).toBe('prj_2')
    expect(state.projects[1]?.id).toBe('prj_1')
  })

  test('clears inspecting popover when virtualized project list scrolls away and back', async () => {
    state.projects = Array.from({ length: 60 }, (_, index) =>
      makeProjectSummary(`prj_${index + 1}`, `Project ${index + 1}`),
    )

    const { wrapper } = mountProjectList({ compact: false, active: true })
    const virtualEl = wrapper.find<HTMLElement>('.projects-virtual').element
    Object.defineProperty(virtualEl, 'clientHeight', { value: 240, configurable: true })
    Object.defineProperty(virtualEl, 'scrollHeight', { value: 2400, configurable: true })

    const first = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')
    first.element.dispatchEvent(
      new PointerEvent('pointerenter', { bubbles: false, pointerType: 'mouse' }),
    )
    vi.advanceTimersByTime(450)
    await flushPromises()

    expect(visiblePopovers()).toHaveLength(1)
    expect(getProjectActivity).toHaveBeenCalledTimes(1)

    // Scroll away so prj_1 unmounts from the virtual window.
    virtualEl.scrollTop = 1600
    virtualEl.dispatchEvent(new Event('scroll'))
    vi.advanceTimersByTime(50)
    await nextTick()
    await flushPromises()

    expect(visiblePopovers()).toHaveLength(0)
    expect(wrapper.find('button[data-project-id="prj_1"]').exists()).toBe(false)

    // Scroll back to top: prj_1 remounts without a stale auto-opened skeleton popover.
    virtualEl.scrollTop = 0
    virtualEl.dispatchEvent(new Event('scroll'))
    vi.advanceTimersByTime(50)
    await nextTick()
    await flushPromises()

    const remounted = wrapper.find<HTMLButtonElement>('button[data-project-id="prj_1"]')
    expect(remounted.exists()).toBe(true)
    expect(visiblePopovers()).toHaveLength(0)
    expect(document.querySelectorAll('.dou-popover .dou-skeleton')).toHaveLength(0)
  })

  test('omits the pinned gallery entry while clearing active project state in gallery view', async () => {
    const { wrapper } = mountProjectList({ compact: false, active: true })
    expect(wrapper.find('[data-gallery-entry]').exists()).toBe(false)
    expect(wrapper.find('button[data-project-id="prj_1"]').attributes('aria-current')).toBe('page')

    state.main = { kind: 'gallery' }
    await nextTick()
    expect(
      wrapper.find('button[data-project-id="prj_1"]').attributes('aria-current'),
    ).toBeUndefined()
  })
})
