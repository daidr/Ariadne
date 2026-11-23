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
