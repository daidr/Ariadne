// @vitest-environment happy-dom
import type { ProjectView, ThreadView } from '@ariadne/core'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { DouConfigProvider, DouDropdownItem, toast } from 'dou-ui'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { Comment, createCommentVNode, defineComponent, Fragment, h, nextTick, ref } from 'vue'
import { douLocale, locale, t } from '../i18n'
import FullView from '../full/FullView.vue'
import SideLayout from '../side/SideLayout.vue'
import * as side from '../side/layout'
import { setSide, state } from '../store'
import ThreadTools from './ThreadTools.vue'

const wrappers: VueWrapper[] = []

function makeThread(over: Partial<ThreadView> & { id: string; title: string }): ThreadView {
  return {
    projectId: 'prj_1',
    nodeId: null,
    stage: 'done',
    lastReport: null,
    lastSummary: null,
    lastHeadline: null,
    lastArtifacts: [],
    workspace: null,
    merged: null,
    archived: false,
    changes: null,
    pullRequest: null,
    aside: null,
    stopped: null,
    references: [],
    endedReferences: [],
    model: null,
    effort: null,
    branchSlug: null,
    usage: {
      input: 0,
      output: 0,
      cached: 0,
      cacheWrite: 0,
      reasoning: 0,
      cost: { USD: 0, CNY: 0 },
    },
    progress: null,
    doing: null,
    createdAt: 1000,
    updatedAt: 1000,
    activeAt: 1000,
    reviewOf: null,
    review: null,
    auto: false,
    autopilotHeld: null,
    ...over,
  }
}

function openMenuElement(): HTMLElement {
  const menus = [...document.querySelectorAll<HTMLElement>('[role="menu"]')].filter(
    (el) => el.style.display !== 'none',
  )
  expect(menus).toHaveLength(1)
  return menus[0]!
}

function isHiddenByDouCss(el: Element): boolean {
  if (el.getAttribute('role') !== 'separator') return false
  // DouUI natively hides leading, trailing, and consecutive dropdown separators via CSS.
  if (!el.previousElementSibling || !el.nextElementSibling) return true
  if (el.previousElementSibling.getAttribute('role') === 'separator') return true
  return false
}

function visibleSeparators(menu: HTMLElement): Element[] {
  return [...menu.querySelectorAll('[role="separator"]')].filter((el) => !isHiddenByDouCss(el))
}

function menuChildrenSummary(menu: HTMLElement): string[] {
  return [...menu.children]
    .filter(
      (el) =>
        (el.getAttribute('role') === 'separator' && !isHiddenByDouCss(el)) ||
        el.getAttribute('role')?.startsWith('menuitem') ||
        el.classList.contains('dou-dropdown__sub'),
    )
    .map((el) => (el.getAttribute('role') === 'separator' ? '---' : (el.textContent?.trim() ?? '')))
}

beforeEach(() => {
  locale.value = 'zh-CN'
})

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  state.detail = null
  state.artifacts = []
  state.full = null
  setSide(side.defaultLayout())
  toast.closeAll()
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('ThreadTools divider behavior', () => {
  test('omits the top divider when the default slot renders only hidden nodes', async () => {
    const showLeading = ref(false)
    const thread = ref(makeThread({ id: 'thr_1', title: 'Done thread', stage: 'done' }))

    const Host = defineComponent({
      setup() {
        return () =>
          h(DouConfigProvider, { locale: douLocale.value }, () =>
            h(
              ThreadTools,
              { thread: thread.value, label: '更多操作' },
              {
                default: () => [
                  createCommentVNode('v-if'),
                  h(
                    Fragment,
                    null,
                    showLeading.value
                      ? [h(DouDropdownItem, { value: 'a' }, () => '置顶操作')]
                      : [h(Comment, 'v-if')],
                  ),
                ],
              },
            ),
          )
      },
    })

    const wrapper = mount(Host, { attachTo: document.body })
    wrappers.push(wrapper)

    await wrapper.get('button[aria-label="更多操作"]').trigger('click')
    await nextTick()

    const menu = openMenuElement()
    expect(visibleSeparators(menu)).toHaveLength(0)
    expect(menuChildrenSummary(menu)).toEqual([t('trace.open')])

    // Even in review stage (two thread items), no leading divider appears when the slot is empty.
    thread.value = makeThread({ id: 'thr_1', title: 'Review thread', stage: 'review' })
    await nextTick()
    expect(visibleSeparators(menu)).toHaveLength(0)
    expect(menuChildrenSummary(menu)).toEqual([t('changes.skipMerge'), t('trace.open')])

    // Once the leading slot renders a real item, exactly one divider separates the two groups.
    showLeading.value = true
    await nextTick()
    expect(visibleSeparators(menu)).toHaveLength(1)
    expect(menuChildrenSummary(menu)).toEqual([
      '置顶操作',
      '---',
      t('changes.skipMerge'),
      t('trace.open'),
    ])

    // Switching back to non-review keeps a single divider above trace.
    thread.value = makeThread({ id: 'thr_1', title: 'Done thread', stage: 'done' })
    await nextTick()
    expect(visibleSeparators(menu)).toHaveLength(1)
    expect(menuChildrenSummary(menu)).toEqual(['置顶操作', '---', t('trace.open')])

    // Hiding the leading item again removes the divider.
    showLeading.value = false
    await nextTick()
    expect(visibleSeparators(menu)).toHaveLength(0)
    expect(menuChildrenSummary(menu)).toEqual([t('trace.open')])
  })

  test('renders no divider on mobile SideLayout thread menu and keeps separators on desktop', async () => {
    const compact = ref(true)
    const thread = makeThread({
      id: 'thr_mobile',
      title: '补充接口 /api/files',
      stage: 'done',
    })
    state.detail = {
      project: { id: 'prj_1', name: 'Ariadne', coordinatorId: 'crd_1' } as ProjectView,
      threads: [thread],
      needsMe: [],
      hasTracker: false,
    }
    setSide(side.open(side.defaultLayout(), side.threadKey(thread.id)))

    const Host = defineComponent({
      setup() {
        return () =>
          h(DouConfigProvider, { locale: douLocale.value }, () =>
            h(SideLayout, { compact: compact.value, active: true }),
          )
      },
    })

    const wrapper = mount(Host, {
      attachTo: document.body,
      global: {
        stubs: {
          ArtifactList: true,
          ConversationPanel: true,
          OpenedView: true,
          PlanPanel: true,
          ThreadList: true,
        },
      },
    })
    wrappers.push(wrapper)
    await nextTick()

    await wrapper.get(`button[aria-label="${t('thread.more')}"]`).trigger('click')
    await nextTick()

    const menu = openMenuElement()
    expect(visibleSeparators(menu)).toHaveLength(0)
    expect(menuChildrenSummary(menu)).toEqual([t('trace.open')])

    // On desktop, the side group items appear above the thread section, separated cleanly.
    compact.value = false
    await nextTick()
    const desktopSummary = menuChildrenSummary(menu)
    expect(desktopSummary[0]).not.toBe('---')
    expect(desktopSummary.at(-1)).toBe(t('trace.open'))
    expect(desktopSummary.at(-2)).toBe('---')
    // No consecutive separators anywhere in the menu.
    for (let i = 1; i < desktopSummary.length; i += 1) {
      expect(desktopSummary[i] === '---' && desktopSummary[i - 1] === '---').toBe(false)
    }
  })

  test('copies workspace path or git switch command from ThreadTools with toast feedback', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })
    const successSpy = vi.spyOn(toast, 'success')
    const dangerSpy = vi.spyOn(toast, 'danger')

    const thread = ref(
      makeThread({
        id: 'thr_ws',
        title: '移除次 header',
        stage: 'review',
        workspace: {
          label: '/worktrees/thr_ws',
          branch: 'feat/remove-header',
          base: 'main',
          removed: false,
          parked: false,
        },
      }),
    )

    const Host = defineComponent({
      setup() {
        return () =>
          h(DouConfigProvider, { locale: douLocale.value }, () =>
            h(
              ThreadTools,
              { thread: thread.value, label: t('thread.more') },
              {
                default: () => [h(DouDropdownItem, { value: 'split' }, () => '向右分栏')],
              },
            ),
          )
      },
    })

    const wrapper = mount(Host, { attachTo: document.body })
    wrappers.push(wrapper)

    await wrapper.get(`button[aria-label="${t('thread.more')}"]`).trigger('click')
    await nextTick()

    const menu = openMenuElement()
    expect(visibleSeparators(menu)).toHaveLength(1)
    const items = menuChildrenSummary(menu)
    expect(items[0]).toBe('向右分栏')
    expect(items[1]).toBe('---')
    expect(items[2]).toContain(t('thread.copyWorkspacePath'))
    expect(items[2]).toContain('feat/remove-header')

    const wsItem = [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) =>
      el.textContent?.includes(t('thread.copyWorkspacePath')),
    )!
    wsItem.click()
    await flushPromises()
    expect(writeText).toHaveBeenLastCalledWith('/worktrees/thr_ws')
    expect(successSpy).toHaveBeenLastCalledWith(t('thread.copiedWorkspacePath'))

    // When worktree is removed (parked or cleaned), switches to git switch <branch>.
    thread.value = makeThread({
      ...thread.value,
      workspace: {
        ...thread.value.workspace!,
        removed: true,
        parked: true,
      },
    })
    await nextTick()
    await wrapper.get(`button[aria-label="${t('thread.more')}"]`).trigger('click')
    await nextTick()

    const menuAfter = openMenuElement()
    const switchItem = [...menuAfter.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (el) => el.textContent?.includes(t('thread.copyBranchSwitch')),
    )!
    expect(switchItem.textContent).toContain('git switch feat/remove-header')
    switchItem.click()
    await flushPromises()
    expect(writeText).toHaveBeenLastCalledWith('git switch feat/remove-header')
    expect(successSpy).toHaveBeenLastCalledWith(t('thread.copiedBranchSwitch'))

    // Reports failure toast if clipboard write rejects.
    writeText.mockRejectedValueOnce(new Error('denied'))
    await wrapper.get(`button[aria-label="${t('thread.more')}"]`).trigger('click')
    await nextTick()
    const failItem = [...openMenuElement().querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (el) => el.textContent?.includes(t('thread.copyBranchSwitch')),
    )!
    failItem.click()
    await flushPromises()
    expect(dangerSpy).toHaveBeenCalledWith(t('thread.copyFailed'))
  })

  test('navigates to the reviewed author thread from ThreadTools in both SideLayout and FullView', async () => {
    const author = makeThread({ id: 'thr_author', title: '重构标题栏布局', stage: 'review' })
    const reviewer = makeThread({
      id: 'thr_reviewer',
      title: '审阅：重构标题栏布局',
      stage: 'working',
      reviewOf: author.id,
    })
    state.detail = {
      project: { id: 'prj_1', name: 'Ariadne', coordinatorId: 'crd_1' } as ProjectView,
      threads: [author, reviewer],
      needsMe: [],
      hasTracker: false,
    }
    state.full = {
      view: { kind: 'changes', threadId: author.id },
      threadId: reviewer.id,
      fromSide: false,
    }

    const Host = defineComponent({
      setup() {
        return () =>
          h(DouConfigProvider, { locale: douLocale.value }, () => h(FullView, { compact: false }))
      },
    })

    const wrapper = mount(Host, {
      attachTo: document.body,
      global: {
        stubs: {
          ConversationPanel: { template: '<div data-stub-conversation />' },
          OpenedView: true,
        },
      },
    })
    wrappers.push(wrapper)
    await nextTick()

    const header = document.querySelector('.full-thread-header')
    expect(header).not.toBeNull()
    expect(document.querySelector('.conversation-header')).toBeNull()

    const moreBtn = header!.querySelector<HTMLButtonElement>(
      `button[aria-label="${t('thread.more')}"]`,
    )!
    moreBtn.click()
    await nextTick()

    const menu = openMenuElement()
    expect(visibleSeparators(menu)).toHaveLength(0)
    const authorItem = [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) =>
      el.textContent?.includes(t('review.author')),
    )!
    expect(authorItem.textContent).toContain(author.title)
    authorItem.click()
    await flushPromises()

    // In FullView, openThread replaces the companion thread in place.
    expect(state.full?.threadId).toBe(author.id)
  })
})
