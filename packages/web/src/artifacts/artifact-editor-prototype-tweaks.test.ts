// @vitest-environment happy-dom
import type { ArtifactView } from '@ariadne/core'
import type { FlowNode } from '@dou-ui/flow'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, onBeforeUnmount, ref, shallowRef } from 'vue'
import * as Y from 'yjs'
import { api } from '../api'
import { setLocale } from '../i18n'
import { state } from '../store'
import type { DecisionGroupSummary } from './decision-groups'
import { clearPendingArtifactFeedback, getPendingArtifactFeedback } from './feedback-pending'
import type {
  PrototypeNodeData,
  PrototypeTweakSpec,
  PrototypeTweakValues,
} from './prototype-tweaks'
import PrototypeNode from './PrototypeNode.vue'
import PrototypeTweaksPanel from './PrototypeTweaksPanel.vue'

let currentSharedDoc: Y.Doc | null = null
let activeDecisionSummary: DecisionGroupSummary | null = null
let applyRecommendedSpy = vi.fn(() => 0)
const fakeProvider = { awareness: null, destroy: () => {} }

vi.mock('./shared', () => ({
  useSharedArtifact: () => {
    const doc = currentSharedDoc ?? new Y.Doc()
    currentSharedDoc = doc
    return {
      doc,
      awareness: null,
      ready: ref(true),
      connected: ref(true),
      provider: shallowRef(fakeProvider),
    }
  },
}))

vi.mock('./WhiteboardBody.vue', () => ({
  __esModule: true,
  default: defineComponent({
    name: 'WhiteboardBody',
    props: {
      shared: { type: Object, default: undefined },
      content: { type: String, default: '' },
      readonly: { type: Boolean, default: false },
    },
    emits: ['decisionSummary', 'comment'],
    setup(props, { emit, expose }) {
      const nodes = ref<FlowNode[]>([])

      const readNodes = () => {
        if (!props.shared) return []
        const map = props.shared.doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
        const out: FlowNode[] = []
        map.forEach((fields, id) => {
          out.push({ ...(fields.toJSON() as Record<string, unknown>), id } as FlowNode)
        })
        return out
      }

      if (props.shared) {
        const map = props.shared.doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
        const sync = () => {
          nodes.value = readNodes()
        }
        sync()
        map.observeDeep(sync)
        onBeforeUnmount(() => map.unobserveDeep(sync))
      }

      if (activeDecisionSummary) {
        emit('decisionSummary', activeDecisionSummary)
      }

      const updateNodeData = (nodeId: string, patch: Partial<PrototypeNodeData>) => {
        if (!props.shared) return
        const doc = props.shared.doc as Y.Doc
        const map = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
        doc.transact(() => {
          const fields = map.get(nodeId)
          if (!fields) return
          const prev = (fields.get('data') as Record<string, unknown> | undefined) ?? {}
          const next: Record<string, unknown> = { ...prev }
          for (const [k, v] of Object.entries(patch)) {
            if (v === undefined) delete next[k]
            else next[k] = v
          }
          fields.set('data', next)
        })
      }

      expose({
        markedPictures: async () => [],
        markHeard: () => {},
        getHeard: () => new Map(),
        setHeard: () => {},
        currentSnapshot: () => new Map(),
        applyRecommendedPicks: () => applyRecommendedSpy(),
      })

      return () =>
        h(
          'div',
          { class: 'mock-whiteboard' },
          nodes.value
            .filter((node) => node.type === 'prototype')
            .map((node) =>
              h(PrototypeNode, {
                key: node.id,
                node,
                data: (node.data ?? {}) as PrototypeNodeData,
                ports: [],
                selected: false,
                readonly: props.readonly,
                updateData: (patch: Partial<PrototypeNodeData>) => updateNodeData(node.id, patch),
              }),
            ),
        )
    },
  }),
}))

import ArtifactEditor from './ArtifactEditor.vue'

const SAMPLE_SPECS: PrototypeTweakSpec[] = [
  {
    key: 'accent',
    label: '主色',
    kind: 'color',
    default: '#2563eb',
    section: '外观',
    swatches: ['#2563eb', '#10b981'],
  },
  {
    key: 'gap',
    label: '间距',
    kind: 'range',
    default: 16,
    min: 8,
    max: 32,
    step: 2,
    unit: 'px',
    section: '布局',
  },
  {
    key: 'dense',
    label: '紧凑模式',
    kind: 'boolean',
    default: false,
    section: '布局',
  },
  {
    key: 'cta',
    label: '按钮文案',
    kind: 'text',
    default: '立即开始',
  },
]

function seedBoardDoc(tweakValues?: PrototypeTweakValues) {
  const doc = new Y.Doc()
  const nodes = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
  const fields = new Y.Map<unknown>()
  fields.set('type', 'prototype')
  fields.set('x', 40)
  fields.set('y', 40)
  fields.set('width', 480)
  fields.set('height', 336)
  const data: PrototypeNodeData = {
    title: '落地页原型',
    html: '<!doctype html><html><body><button>Go</button></body></html>',
    viewportWidth: 1280,
    viewportHeight: 800,
    tweaks: SAMPLE_SPECS,
    ...(tweakValues ? { tweakValues } : {}),
  }
  fields.set('data', data)
  nodes.set('proto-1', fields)
  return doc
}

function readNodeTweakValues(doc: Y.Doc, nodeId = 'proto-1'): PrototypeTweakValues | undefined {
  const fields = (doc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get(nodeId)
  const data = fields?.get('data') as PrototypeNodeData | undefined
  return data?.tweakValues
}

function boardDetail(id: string) {
  const artifact: ArtifactView = {
    id,
    projectId: 'prj_1',
    kind: 'whiteboard',
    title: '原型评审画板',
    nodeId: null,
    content: JSON.stringify({ version: 1, nodes: [], edges: [] }),
    version: 1,
    createdBy: 'agent',
    updatedBy: 'agent',
    createdAt: 1000,
    updatedAt: 1000,
    commentedAt: 0,
    openComments: 0,
    preview: { kind: 'whiteboard', items: 1, connectors: 0, texts: [] },
  }
  return {
    artifact,
    comments: [],
    versions: [{ version: 1, title: artifact.title, author: 'agent', reason: '', createdAt: 1000 }],
    askBaseline: null,
  }
}

const DouPopoverStub = defineComponent({
  name: 'DouPopover',
  props: { open: Boolean },
  emits: ['update:open'],
  template: '<div><slot /><slot name="content" :close="() => {}" /></div>',
})

async function mountBoardEditor(doc: Y.Doc, id = 'art_board_1') {
  currentSharedDoc = doc
  const getArtifact = vi.spyOn(api, 'getArtifact').mockResolvedValue(boardDetail(id))
  const sendFeedback = vi.spyOn(api, 'sendFeedback').mockResolvedValue({
    feedback: {
      artifactId: id,
      round: 1,
      version: 2,
      notify: 'thr_1',
      createdAt: 2000,
      items: [],
      images: [],
      doneBy: null,
      doneAt: null,
      summary: null,
    },
  })
  const withdrawFeedback = vi.spyOn(api, 'withdrawFeedback').mockResolvedValue({ withdrawn: true })

  const wrapper = mount(ArtifactEditor, {
    props: { id, place: 'full' },
    global: {
      stubs: {
        ViewActions: true,
        ArtifactHistory: true,
        CommentList: true,
        DouPopover: DouPopoverStub,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  return { wrapper, getArtifact, sendFeedback, withdrawFeedback }
}

describe('ArtifactEditor prototype tweaks local preview & explicit send', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setLocale('zh-CN')
    state.artifacts = []
    currentSharedDoc = null
    activeDecisionSummary = null
    applyRecommendedSpy = vi.fn(() => 0)
    clearPendingArtifactFeedback('art_board_1')
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    clearPendingArtifactFeedback('art_board_1')
  })

  test('adjusting tweaks previews locally without adding pending changes or auto-sending', async () => {
    const doc = seedBoardDoc()
    let docUpdates = 0
    doc.on('update', () => {
      docUpdates++
    })

    const { wrapper, sendFeedback } = await mountBoardEditor(doc)

    // Spy on postMessage to the sandboxed prototype iframe.
    const iframe = wrapper.find('iframe.proto-iframe').element as HTMLIFrameElement
    const postMessage = vi.fn()
    Object.defineProperty(iframe, 'contentWindow', {
      value: { postMessage },
      configurable: true,
    })

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    const sendBtn = () => wrapper.find('.tweaks-footer button')

    // At default values, the send button is disabled and no feedback bar is visible.
    expect(sendBtn().exists()).toBe(true)
    expect((sendBtn().element as HTMLButtonElement).disabled).toBe(true)
    expect(sendBtn().text()).toContain('将当前参数发给 AI')
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Simulate dozens of rapid tweak previews (slider drag, toggles, color/text edits).
    for (let gap = 18; gap <= 32; gap += 2) {
      panel.vm.$emit('change', {
        accent: '#10b981',
        gap,
        dense: gap % 4 === 0,
        cta: '免费试用',
      })
      await flushPromises()
    }

    // Preview updated the iframe live, header badge shows modified count, and send button enabled.
    expect(postMessage).toHaveBeenCalled()
    const lastPayload = postMessage.mock.calls.at(-1)?.[0] as {
      type: string
      values: PrototypeTweakValues
    }
    expect(lastPayload.type).toBe('ariadne:tweaks')
    expect(lastPayload.values).toEqual({
      accent: '#10b981',
      gap: 32,
      dense: true,
      cta: '免费试用',
    })
    expect(wrapper.find('.proto-tweak-count').text()).toBe('4')
    expect((sendBtn().element as HTMLButtonElement).disabled).toBe(false)

    // Crucially: zero Yjs writes, zero pending changes, no bottom bar, no auto-send countdown.
    expect(docUpdates).toBe(0)
    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Even after waiting longer than the 15s quiet countdown, nothing auto-sends.
    await vi.advanceTimersByTimeAsync(25_000)
    await flushPromises()
    expect(sendFeedback).not.toHaveBeenCalled()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Resetting defaults stays local too and disables the send button again.
    panel.vm.$emit('reset')
    await flushPromises()
    expect(docUpdates).toBe(0)
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)
    expect((sendBtn().element as HTMLButtonElement).disabled).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    wrapper.unmount()
  })

  test('explicitly clicking send commits final tweak values once and submits feedback cleanly', async () => {
    const doc = seedBoardDoc()
    const { wrapper, sendFeedback } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: true, cta: '立即开始' })
    await flushPromises()

    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Click the explicit "将当前参数发给 AI" button.
    const sendBtn = wrapper.find('.tweaks-footer button')
    const clickPromise = sendBtn.trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickPromise
    await flushPromises()

    // Only changed keys (diff against defaults) were written once to Yjs and sent.
    expect(readNodeTweakValues(doc)).toEqual({
      accent: '#10b981',
      gap: 24,
      dense: true,
    })
    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(sendFeedback).toHaveBeenCalledWith('art_board_1', [])
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Feedback bar shows sent round (with withdraw), never pending countdown.
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('第 1 轮反馈已发送')
    expect(wrapper.find('.artifact-feedback-bar').text()).not.toContain('待同步')

    // Panel button switches to disabled "当前参数已发送" until further tweaks are made.
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(true)
    expect(wrapper.find('.tweaks-footer button').text()).toContain('当前参数已发送')

    // Making a further local tweak re-enables the button without triggering pending bar.
    panel.vm.$emit('change', { accent: '#10b981', gap: 28, dense: true, cta: '立即开始' })
    await flushPromises()
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')

    wrapper.unmount()
  })

  test('withdrawing sent tweaks silently reverts Yjs without triggering auto-send countdown', async () => {
    const doc = seedBoardDoc()
    const { wrapper, sendFeedback, withdrawFeedback } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()
    expect(readNodeTweakValues(doc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('第 1 轮反馈已发送')

    // Click withdraw in the bottom bar.
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()

    expect(withdrawFeedback).toHaveBeenCalledWith('art_board_1', 1)
    // Yjs tweakValues rolled back silently; no pending countdown bar appears.
    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Local preview stays at user's adjusted values and send button is re-enabled.
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    await vi.advanceTimersByTimeAsync(25_000)
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  test('rolls back Yjs tweakValues silently when explicit send returns empty or fails', async () => {
    const doc = seedBoardDoc()
    const { wrapper, sendFeedback } = await mountBoardEditor(doc)
    sendFeedback.mockResolvedValue({ feedback: null })

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    // 120ms initial wait + 250ms retry wait.
    await vi.advanceTimersByTimeAsync(450)
    await clickSend
    await flushPromises()

    expect(sendFeedback).toHaveBeenCalledTimes(2)
    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()
    // User's local preview and send button remain intact for retry.
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('sending prototype tweaks does not auto-pick recommended options in active decision groups', async () => {
    activeDecisionSummary = {
      hasActiveDecisionGroups: true,
      totalGroups: 1,
      pickedGroups: 0,
      recommendedFallbackGroups: 1,
      remainingGroups: 0,
    }
    const doc = seedBoardDoc()
    const { wrapper, sendFeedback } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()

    expect(applyRecommendedSpy).not.toHaveBeenCalled()
    expect(readNodeTweakValues(doc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(sendFeedback).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  test('preserves local preview tweaks across unrelated board node updates', async () => {
    const doc = seedBoardDoc()
    const { wrapper } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: true, cta: '立即开始' })
    await flushPromises()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('3')

    // Simulate an unrelated change on the whiteboard (adding another sticky node).
    const nodes = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
    doc.transact(() => {
      const sticky = new Y.Map<unknown>()
      sticky.set('type', 'sticky')
      sticky.set('x', 600)
      sticky.set('y', 40)
      sticky.set('data', { text: '备注' })
      nodes.set('sticky-1', sticky)
    }, fakeProvider)
    await flushPromises()

    // Prototype's local preview tweaks remain untouched.
    expect(wrapper.find('.proto-tweak-count').text()).toBe('3')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('panel controls update preview locally and resetting after send allows sending defaults back', async () => {
    const doc = seedBoardDoc()
    let docUpdates = 0
    doc.on('update', () => {
      docUpdates++
    })

    const { wrapper, sendFeedback } = await mountBoardEditor(doc)

    // Interact with actual panel controls: toggle switch & update text input.
    const switchEl = wrapper.find('[role="switch"]')
    await switchEl.trigger('click')
    await flushPromises()

    const textInput = wrapper.find('.tweaks-panel input')
    await textInput.setValue('立即体验')
    await flushPromises()

    // Purely local preview: 0 Yjs updates, no pending feedback bar.
    expect(docUpdates).toBe(0)
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Explicitly send final tweaks.
    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()

    expect(docUpdates).toBe(1)
    expect(readNodeTweakValues(doc)).toEqual({ dense: true, cta: '立即体验' })
    expect(sendFeedback).toHaveBeenCalledTimes(1)

    // Click "恢复默认" in panel header -> stays local, does not write Yjs or trigger countdown,
    // and re-enables "将当前参数发给 AI" because live preview now differs from previously sent values.
    const resetBtn = wrapper.find('.tweaks-header button')
    await resetBtn.trigger('click')
    await flushPromises()

    expect(docUpdates).toBe(1)
    expect(readNodeTweakValues(doc)).toEqual({ dense: true, cta: '立即体验' })
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    // Explicitly sending again writes cleared tweakValues (undefined) so AI receives the reset.
    sendFeedback.mockResolvedValueOnce({
      feedback: {
        artifactId: 'art_board_1',
        round: 2,
        version: 3,
        notify: 'thr_1',
        createdAt: 3000,
        items: [],
        images: [],
        doneBy: null,
        doneAt: null,
        summary: null,
      },
    })
    const clickSendReset = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSendReset
    await flushPromises()

    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(sendFeedback).toHaveBeenCalledTimes(2)
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(true)

    wrapper.unmount()
  })

  test('rolls back Yjs silently when api.sendFeedback throws an error', async () => {
    const doc = seedBoardDoc()
    const { wrapper, sendFeedback } = await mountBoardEditor(doc)
    sendFeedback.mockRejectedValueOnce(new Error('network down'))

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()

    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })
})
