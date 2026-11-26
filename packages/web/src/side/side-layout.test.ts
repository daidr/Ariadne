// @vitest-environment happy-dom
import type { ArtifactSummary, ProjectView, ThreadView } from '@ariadne/core'
import { mount } from '@vue/test-utils'
import { afterEach, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import { state } from '../store'
import { dragArtifact, sideDock } from './dock'
import * as side from './layout'
import SideLayout from './SideLayout.vue'

afterEach(() => {
  sideDock.value = null
  state.currentId = null
  state.detail = null
  state.artifacts = []
  state.side = side.defaultLayout()
  document.querySelectorAll('.dou-tabs-ghost').forEach((el) => el.remove())
  document.documentElement.classList.remove('dou-tabs-dragging')
  vi.restoreAllMocks()
})

test('registers project artifacts in dock panels so unopened artifacts drag with their title', async () => {
  state.currentId = 'prj_1'
  state.detail = {
    project: { id: 'prj_1', name: 'Demo', coordinatorId: 'crd_1' } as ProjectView,
    threads: [{ id: 'thr_1', title: 'Thread One', stage: 'working' } as ThreadView],
    needsMe: [],
    hasTracker: false,
  }
  state.artifacts = [
    {
      id: 'art_1',
      kind: 'document',
      title: 'Design Spec',
      nodeId: null,
      version: 1,
      updatedAt: 1000,
      preview: { kind: 'document', lead: '# Design', excerpt: 'Design' },
    } as ArtifactSummary,
  ]
  state.side = side.activate(side.defaultLayout(), 'artifacts')

  const wrapper = mount(SideLayout, {
    props: { compact: false, active: true },
    global: {
      stubs: {
        PlanPanel: true,
        ThreadList: true,
        ArtifactList: true,
        ConversationPanel: true,
        OpenedView: true,
        ThreadTools: true,
      },
    },
    attachTo: document.body,
  })
  try {
    await nextTick()
    expect(sideDock.value).not.toBeNull()

    const source = document.createElement('button')
    document.body.append(source)
    try {
      vi.spyOn(source, 'getBoundingClientRect').mockReturnValue(new DOMRect(100, 200, 180, 120))
      const down = new PointerEvent('pointerdown', {
        bubbles: true,
        button: 0,
        clientX: 120,
        clientY: 220,
      })
      Object.defineProperty(down, 'currentTarget', { value: source })
      dragArtifact(down, 'art_1')

      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 140, clientY: 160 }))
      await nextTick()

      const ghost = document.querySelector('.dou-tabs-ghost')
      expect(ghost?.textContent).toBe('Design Spec')

      window.dispatchEvent(new PointerEvent('pointercancel'))
      await nextTick()
      expect(document.querySelector('.dou-tabs-ghost')).toBeNull()
    } finally {
      source.remove()
    }

    const pinnedButtons = wrapper.findAll('.dou-tabs__pinned [role="tab"]')
    expect(pinnedButtons).toHaveLength(3)
    expect(pinnedButtons.every((btn) => btn.classes().includes('is-pinned'))).toBe(true)
    expect(pinnedButtons.every((btn) => btn.classes().includes('is-icon-alone'))).toBe(true)
    expect(pinnedButtons.every((btn) => !btn.find('.dou-tabs__close').exists())).toBe(true)
  } finally {
    wrapper.unmount()
  }
})

test('highlights and merges a secondary list copy into the first group pinned tab via mergeId', async () => {
  state.currentId = 'prj_1'
  state.detail = {
    project: { id: 'prj_1', name: 'Demo', coordinatorId: 'crd_1' } as ProjectView,
    threads: [{ id: 'thr_1', title: 'Thread One', stage: 'working' } as ThreadView],
    needsMe: [],
    hasTracker: false,
  }
  let layout = side.open(side.defaultLayout(), side.threadKey('thr_1'))
  layout = side.split(layout, 'plan', 'horizontal')
  state.side = layout
  const [first, second] = side.groupsOf(state.side.root)
  expect(first?.active).toBe('thread:thr_1')
  expect(second?.tabs).toEqual(['list:plan:1'])

  const wrapper = mount(SideLayout, {
    props: { compact: false, active: true },
    global: {
      stubs: {
        PlanPanel: true,
        ThreadList: true,
        ArtifactList: true,
        ConversationPanel: true,
        OpenedView: true,
        ThreadTools: true,
      },
    },
    attachTo: document.body,
  })
  try {
    await nextTick()
    const firstGroupEl = wrapper.find(`[data-dock-group="${first!.id}"]`).element as HTMLElement
    const firstHeaderEl = wrapper.find(`[data-dock-group="${first!.id}"] .dou-tabs__header`)
      .element as HTMLElement
    const secondGroupEl = wrapper.find(`[data-dock-group="${second!.id}"]`).element as HTMLElement
    vi.spyOn(firstGroupEl, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 600, 440))
    vi.spyOn(firstHeaderEl, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 600, 36))
    vi.spyOn(secondGroupEl, 'getBoundingClientRect').mockReturnValue(new DOMRect(600, 0, 400, 440))

    const copyTab = wrapper.find(`[data-dock-group="${second!.id}"] .dou-tabs__list [role="tab"]`)
      .element as HTMLElement
    vi.spyOn(copyTab, 'getBoundingClientRect').mockReturnValue(new DOMRect(610, 4, 80, 28))

    const origElementFromPoint = document.elementFromPoint
    const origRaf = window.requestAnimationFrame
    const origCancelRaf = window.cancelAnimationFrame
    let hovered: Element | null = null
    let pendingRaf: FrameRequestCallback | null = null
    const flushRaf = (time: number) => {
      const cb = pendingRaf
      pendingRaf = null
      cb?.(time)
    }
    document.elementFromPoint = () => hovered
    window.requestAnimationFrame = (cb) => {
      pendingRaf = cb
      return 1
    }
    window.cancelAnimationFrame = () => {
      pendingRaf = null
    }

    try {
      copyTab.dispatchEvent(
        new PointerEvent('pointerdown', {
          bubbles: true,
          button: 0,
          clientX: 630,
          clientY: 16,
        }),
      )
      // Move past drag threshold onto the first group's tab bar.
      hovered = firstHeaderEl
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 120, clientY: 16 }))
      flushRaf(16)
      await nextTick()

      const pinnedButtons = wrapper.findAll(
        `[data-dock-group="${first!.id}"] .dou-tabs__pinned [role="tab"]`,
      )
      expect(pinnedButtons[0]?.classes()).toContain('is-merge-target')

      // Moving to the center of the first group keeps the pinned merge highlight.
      hovered = firstGroupEl
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 300, clientY: 220 }))
      flushRaf(32)
      await nextTick()
      expect(pinnedButtons[0]?.classes()).toContain('is-merge-target')

      // Moving to the right split edge clears the merge highlight.
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 590, clientY: 220 }))
      flushRaf(48)
      await nextTick()
      expect(pinnedButtons[0]?.classes()).not.toContain('is-merge-target')

      // Moving back to center and dropping merges into the pinned `plan` tab.
      window.dispatchEvent(new PointerEvent('pointermove', { clientX: 300, clientY: 220 }))
      flushRaf(64)
      await nextTick()
      window.dispatchEvent(new PointerEvent('pointerup', { clientX: 300, clientY: 220 }))
      await nextTick()

      const groups = side.groupsOf(state.side.root)
      expect(groups).toHaveLength(1)
      expect(groups[0]?.id).toBe(first!.id)
      expect(groups[0]?.active).toBe('plan')
      expect(state.side.focused).toBe(first!.id)
    } finally {
      document.elementFromPoint = origElementFromPoint
      window.requestAnimationFrame = origRaf
      window.cancelAnimationFrame = origCancelRaf
    }
  } finally {
    wrapper.unmount()
  }
})

test('shows native title tooltip only when a tab label is actually truncated', async () => {
  state.currentId = 'prj_1'
  state.detail = {
    project: { id: 'prj_1', name: 'Demo', coordinatorId: 'crd_1' } as ProjectView,
    threads: [
      {
        id: 'thr_short',
        title: '短标题',
        stage: 'working',
      } as ThreadView,
      {
        id: 'thr_long',
        title: 'A very long discussion thread title that overflows the tab width',
        stage: 'working',
      } as ThreadView,
    ],
    needsMe: [],
    hasTracker: false,
  }
  state.artifacts = [
    {
      id: 'art_short',
      kind: 'document',
      title: 'Notes',
      nodeId: null,
      version: 1,
      updatedAt: 1000,
      preview: { kind: 'document', lead: '# Notes', excerpt: 'Notes' },
    } as ArtifactSummary,
    {
      id: 'art_long',
      kind: 'document',
      title: 'A comprehensive architecture specification document title',
      nodeId: null,
      version: 1,
      updatedAt: 1000,
      preview: { kind: 'document', lead: '# Spec', excerpt: 'Spec' },
    } as ArtifactSummary,
  ]
  let layout = side.open(side.defaultLayout(), side.threadKey('thr_short'))
  layout = side.open(layout, side.threadKey('thr_long'))
  layout = side.open(layout, side.artifactKey('art_short'))
  layout = side.open(layout, side.artifactKey('art_long'))
  state.side = layout

  const wrapper = mount(SideLayout, {
    props: { compact: false, active: true },
    global: {
      stubs: {
        PlanPanel: true,
        ThreadList: true,
        ArtifactList: true,
        ConversationPanel: true,
        OpenedView: true,
        ThreadTools: true,
      },
    },
    attachTo: document.body,
  })
  try {
    await nextTick()
    const tabs = wrapper.findAll('.dou-tabs__list [role="tab"]')
    expect(tabs).toHaveLength(4)

    // No hardcoded title attributes on label slot nodes.
    expect(wrapper.find('.pane-thread-label').attributes('title')).toBeUndefined()

    const [shortThreadTab, longThreadTab, shortArtTab, longArtTab] = tabs

    // Simulate ripple pseudo-element enlarging DouStatusDot's scrollWidth on both working threads.
    for (const tab of [shortThreadTab!, longThreadTab!]) {
      const dotEl = tab.find('.dou-status-dot__dot').element as HTMLElement
      Object.defineProperty(dotEl, 'clientWidth', { value: 8, configurable: true })
      Object.defineProperty(dotEl, 'scrollWidth', { value: 14, configurable: true })
    }

    const shortThreadTitleEl = shortThreadTab!.find('.pane-thread-title').element as HTMLElement
    Object.defineProperty(shortThreadTitleEl, 'clientWidth', { value: 60, configurable: true })
    Object.defineProperty(shortThreadTitleEl, 'scrollWidth', { value: 60, configurable: true })

    const longThreadTitleEl = longThreadTab!.find('.pane-thread-title').element as HTMLElement
    Object.defineProperty(longThreadTitleEl, 'clientWidth', { value: 180, configurable: true })
    Object.defineProperty(longThreadTitleEl, 'scrollWidth', { value: 320, configurable: true })

    const shortLabelEl = shortArtTab!.find('.dou-tabs__label').element as HTMLElement
    Object.defineProperty(shortLabelEl, 'clientWidth', { value: 60, configurable: true })
    Object.defineProperty(shortLabelEl, 'scrollWidth', { value: 60, configurable: true })

    const longLabelEl = longArtTab!.find('.dou-tabs__label').element as HTMLElement
    Object.defineProperty(longLabelEl, 'clientWidth', { value: 180, configurable: true })
    Object.defineProperty(longLabelEl, 'scrollWidth', { value: 340, configurable: true })

    await shortThreadTab!.trigger('pointerenter')
    await longThreadTab!.trigger('pointerenter')
    await shortArtTab!.trigger('pointerenter')
    await longArtTab!.trigger('pointerenter')

    expect(shortThreadTab!.attributes('title')).toBeUndefined()
    expect(longThreadTab!.attributes('title')).toBe(
      'A very long discussion thread title that overflows the tab width',
    )
    expect(shortArtTab!.attributes('title')).toBeUndefined()
    expect(longArtTab!.attributes('title')).toBe(
      'A comprehensive architecture specification document title',
    )
  } finally {
    wrapper.unmount()
  }
})
