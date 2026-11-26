// @vitest-environment happy-dom
import type { ArtifactView } from '@ariadne/core'
import type { FlowNode } from '@dou-ui/flow'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, onBeforeUnmount, onScopeDispose, ref, shallowRef } from 'vue'
import * as Y from 'yjs'
import { api } from '../api'
import { setLocale } from '../i18n'
import { state } from '../store'
import type { DecisionGroupSummary } from './decision-groups'
import {
  clearPendingArtifactFeedback,
  clearPendingPrototypeTweakRollback,
  getPendingArtifactFeedback,
  getPendingPrototypeTweakRollbacks,
} from './feedback-pending'
import type {
  PrototypeNodeData,
  PrototypeTweakSpec,
  PrototypeTweakValues,
} from './prototype-tweaks'
import PrototypeNode from './PrototypeNode.vue'
import PrototypeTweaksPanel from './PrototypeTweaksPanel.vue'

interface MockSyncedRoomSession {
  id: string
  doc: Y.Doc
  ready: ReturnType<typeof ref<boolean>>
  connected: ReturnType<typeof ref<boolean>>
  disposed: boolean
  disconnect: () => void
  completeSync: () => void
  reconnect: () => void
}

interface MockServerRoom {
  serverDoc: Y.Doc
  deferReady?: boolean
  sessions: MockSyncedRoomSession[]
}

let currentSharedDoc: Y.Doc | null = null
const sharedDocsByArtifact = new Map<string, Y.Doc>()
const serverRoomsByArtifact = new Map<string, MockServerRoom>()
const culledNodeIds = ref<ReadonlySet<string>>(new Set())
let activeDecisionSummary: DecisionGroupSummary | null = null
let applyRecommendedSpy = vi.fn(() => 0)
const fakeProvider = { awareness: null, destroy: () => {} }

vi.mock('./shared', () => ({
  useSharedArtifact: (id: string) => {
    const serverRoom = serverRoomsByArtifact.get(id)
    if (serverRoom) {
      const clientDoc = new Y.Doc()
      const provider = { awareness: null, destroy: () => {} }
      const ready = ref(false)
      const connected = ref(true)
      let active = true
      let synced = false

      const onClientUpdate = (update: Uint8Array, origin: unknown) => {
        if (!active || !connected.value || origin === provider) return
        Y.applyUpdate(serverRoom.serverDoc, update, clientDoc)
      }
      const onServerUpdate = (update: Uint8Array, origin: unknown) => {
        if (!active || !connected.value || !synced || origin === clientDoc) return
        Y.applyUpdate(clientDoc, update, provider)
      }

      clientDoc.on('update', onClientUpdate)
      serverRoom.serverDoc.on('update', onServerUpdate)

      const completeSync = () => {
        if (!active || synced) return
        synced = true
        Y.applyUpdate(clientDoc, Y.encodeStateAsUpdate(serverRoom.serverDoc), provider)
        ready.value = true
      }
      const disconnect = () => {
        connected.value = false
        ready.value = false
        synced = false
      }
      const reconnect = () => {
        if (!active) return
        connected.value = true
        synced = true
        // Simulate real y-websocket sync step 1 & 2 (bidirectional exchange BEFORE ready flips true).
        const clientDiff = Y.encodeStateAsUpdate(
          clientDoc,
          Y.encodeStateVector(serverRoom.serverDoc),
        )
        const serverDiff = Y.encodeStateAsUpdate(
          serverRoom.serverDoc,
          Y.encodeStateVector(clientDoc),
        )
        if (clientDiff.length > 0) Y.applyUpdate(serverRoom.serverDoc, clientDiff, clientDoc)
        if (serverDiff.length > 0) Y.applyUpdate(clientDoc, serverDiff, provider)
        ready.value = true
      }

      const session: MockSyncedRoomSession = {
        id,
        doc: clientDoc,
        ready,
        connected,
        disposed: false,
        disconnect,
        completeSync,
        reconnect,
      }
      serverRoom.sessions.push(session)

      if (!serverRoom.deferReady) {
        completeSync()
      }

      onScopeDispose(() => {
        active = false
        session.disposed = true
        connected.value = false
        ready.value = false
        clientDoc.off('update', onClientUpdate)
        serverRoom.serverDoc.off('update', onServerUpdate)
        clientDoc.destroy()
      })

      return {
        doc: clientDoc,
        awareness: null,
        ready,
        connected,
        provider: shallowRef(provider),
      }
    }

    const doc = sharedDocsByArtifact.get(id) ?? currentSharedDoc ?? new Y.Doc()
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

      const updateNodeData = (nodeId: string, patch: Partial<PrototypeNodeData>): boolean => {
        if (!props.shared) return false
        const doc = props.shared.doc as Y.Doc
        const map = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
        if (!map.has(nodeId)) return false
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
        return true
      }

      expose({
        markedPictures: async () => [],
        markHeard: () => {},
        getHeard: () => new Map(),
        setHeard: () => {},
        currentSnapshot: () => new Map(),
        applyRecommendedPicks: () => applyRecommendedSpy(),
        setPrototypeTweakValues: (nodeId: string, tweakValues: PrototypeTweakValues | undefined) =>
          updateNodeData(nodeId, { tweakValues }),
      })

      return () =>
        h(
          'div',
          { class: 'mock-whiteboard' },
          nodes.value
            .filter((node) => node.type === 'prototype' && !culledNodeIds.value.has(node.id))
            .map((node) =>
              h(PrototypeNode, {
                key: node.id,
                node,
                data: (node.data ?? {}) as PrototypeNodeData,
                ports: [],
                selected: false,
                readonly: props.readonly,
                // Simulate Vue 3 NodeView emit silently dropping updates once the node is unmounted.
                updateData: (patch: Partial<PrototypeNodeData>) => {
                  if (culledNodeIds.value.has(node.id)) return
                  updateNodeData(node.id, patch)
                },
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

function readNodeData(doc: Y.Doc, nodeId = 'proto-1'): PrototypeNodeData | undefined {
  const fields = (doc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get(nodeId)
  return fields?.get('data') as PrototypeNodeData | undefined
}

function readNodeTweakValues(doc: Y.Doc, nodeId = 'proto-1'): PrototypeTweakValues | undefined {
  return readNodeData(doc, nodeId)?.tweakValues
}

function boardDetail(id: string, version = 1, updatedBy: 'agent' | 'user' = 'agent') {
  const artifact: ArtifactView = {
    id,
    projectId: 'prj_1',
    kind: 'whiteboard',
    title: '原型评审画板',
    nodeId: null,
    content: JSON.stringify({ version: 1, nodes: [], edges: [] }),
    version,
    createdBy: 'agent',
    updatedBy,
    createdAt: 1000,
    updatedAt: 1000,
    commentedAt: 0,
    openComments: 0,
    preview: { kind: 'whiteboard', items: 1, connectors: 0, texts: [] },
  }
  return {
    artifact,
    comments: [],
    versions: [{ version, title: artifact.title, author: updatedBy, reason: '', createdAt: 1000 }],
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
    sharedDocsByArtifact.clear()
    serverRoomsByArtifact.clear()
    culledNodeIds.value = new Set()
    activeDecisionSummary = null
    applyRecommendedSpy = vi.fn(() => 0)
    clearPendingArtifactFeedback('art_board_1')
    clearPendingArtifactFeedback('art_board_2')
    clearPendingPrototypeTweakRollback('art_board_1')
    clearPendingPrototypeTweakRollback('art_board_2')
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    sharedDocsByArtifact.clear()
    serverRoomsByArtifact.clear()
    culledNodeIds.value = new Set()
    clearPendingArtifactFeedback('art_board_1')
    clearPendingArtifactFeedback('art_board_2')
    clearPendingPrototypeTweakRollback('art_board_1')
    clearPendingPrototypeTweakRollback('art_board_2')
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

  test('preserves unsent local preview tweaks across PrototypeNode unmount and remount', async () => {
    const doc = seedBoardDoc()
    let docUpdates = 0
    doc.on('update', () => {
      docUpdates++
    })

    const { wrapper } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 28, dense: true, cta: '试用一下' })
    await flushPromises()

    expect(docUpdates).toBe(0)
    expect(wrapper.find('.proto-tweak-count').text()).toBe('4')

    // Simulate viewport culling / simpleZoom < 0.4 / collapsed parent group unmounting PrototypeNode.
    culledNodeIds.value = new Set(['proto-1'])
    await flushPromises()
    expect(wrapper.findComponent(PrototypeNode).exists()).toBe(false)

    // Pan/zoom back in so a fresh PrototypeNode instance mounts.
    culledNodeIds.value = new Set()
    await flushPromises()

    expect(docUpdates).toBe(0)
    expect(wrapper.findComponent(PrototypeNode).exists()).toBe(true)
    expect(wrapper.find('.proto-tweak-count').text()).toBe('4')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )
    // Newly built sandboxedDoc already carries the restored local preview values.
    const srcdoc = wrapper.find('iframe.proto-iframe').attributes('srcdoc') ?? ''
    expect(srcdoc).toContain('#10b981')
    expect(srcdoc).toContain('试用一下')

    wrapper.unmount()
  })

  test('rolls back Yjs tweakValues via WhiteboardBody even after PrototypeNode unmounts before withdraw', async () => {
    const doc = seedBoardDoc()
    const { wrapper, withdrawFeedback } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()

    expect(readNodeTweakValues(doc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('撤回')

    // Unmount PrototypeNode during the 60s withdraw window (e.g. zoomed out or panned away).
    culledNodeIds.value = new Set(['proto-1'])
    await flushPromises()
    expect(wrapper.findComponent(PrototypeNode).exists()).toBe(false)

    // Click withdraw while the node is unmounted: WhiteboardBody must still roll back Yjs silently.
    const withdrawBtn = wrapper
      .findAll('.artifact-feedback-bar button')
      .find((btn) => btn.text().includes('撤回'))
    expect(withdrawBtn).toBeDefined()
    await withdrawBtn!.trigger('click')
    await flushPromises()

    expect(withdrawFeedback).toHaveBeenCalledWith('art_board_1', 1)
    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Remount PrototypeNode: restored to unsent local preview, send button enabled again.
    culledNodeIds.value = new Set()
    await flushPromises()

    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )
    const srcdoc = wrapper.find('iframe.proto-iframe').attributes('srcdoc') ?? ''
    expect(srcdoc).toContain('#10b981')

    wrapper.unmount()
  })

  test('rolls back Yjs tweakValues via WhiteboardBody when send fails after PrototypeNode unmounts mid-flight', async () => {
    const doc = seedBoardDoc()
    const { wrapper, sendFeedback } = await mountBoardEditor(doc)

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()

    // Yjs already received the silent write and sendFeedback is in flight.
    expect(readNodeTweakValues(doc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(sendFeedback).toHaveBeenCalledTimes(1)

    // Unmount PrototypeNode before the request settles.
    culledNodeIds.value = new Set(['proto-1'])
    await flushPromises()
    expect(wrapper.findComponent(PrototypeNode).exists()).toBe(false)

    // Fail the request: rollback must still clear Yjs via WhiteboardBody.
    rejectSend(new Error('network timeout'))
    await clickSend
    await flushPromises()

    expect(readNodeTweakValues(doc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Remount PrototypeNode: local preview preserved, ready to retry.
    culledNodeIds.value = new Set()
    await flushPromises()

    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('clears stale room-level preview cache when remote tweakValues change while node is unmounted', async () => {
    const doc = seedBoardDoc()
    const { wrapper } = await mountBoardEditor(doc)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 28, dense: false, cta: '立即开始' })
    await flushPromises()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')

    // Unmount PrototypeNode.
    culledNodeIds.value = new Set(['proto-1'])
    await flushPromises()

    // Remote collaborator / AI updates tweakValues on proto-1 while it is unmounted.
    const nodes = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
    doc.transact(() => {
      const fields = nodes.get('proto-1')!
      const prev = fields.get('data') as PrototypeNodeData
      fields.set('data', { ...prev, tweakValues: { dense: true } })
    }, fakeProvider)
    await flushPromises()

    // Remount: stale local preview must be discarded in favor of remote saved values.
    culledNodeIds.value = new Set()
    await flushPromises()

    expect(wrapper.find('.proto-tweak-count').text()).toBe('1')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('当前参数已发送')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(true)

    wrapper.unmount()
  })

  test('clears room-level preview cache when leaving or switching artifact rooms', async () => {
    const doc1 = seedBoardDoc()
    const doc2 = seedBoardDoc()
    sharedDocsByArtifact.set('art_board_1', doc1)
    sharedDocsByArtifact.set('art_board_2', doc2)

    const { wrapper, getArtifact } = await mountBoardEditor(doc1, 'art_board_1')
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')

    // Switch to another artifact (triggers leave() and join('art_board_2')).
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)

    // Switch back to art_board_1: previous room's local preview cache was cleared on leave().
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(true)

    wrapper.unmount()
  })

  test('rolls back server Yjs and restores retry preview when switching artifacts mid-send and request fails', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    // Click "将当前参数发给 AI" so Yjs receives the silent write and sendFeedback is in flight.
    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(server1.sessions).toHaveLength(1)
    expect(server1.sessions[0]!.disposed).toBe(false)

    // Switch to art_board_2 before sendFeedback settles.
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)
    // Source room stays alive while its tweak send is in flight so rollback can flush to server.
    expect(server1.sessions[0]!.disposed).toBe(false)

    // Fail the in-flight request while viewing art_board_2.
    rejectSend(new Error('network timeout'))
    await clickSend
    await flushPromises()

    // Server Y.Doc for art_board_1 was immediately rolled back and retained room was disposed cleanly.
    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(server1.sessions[0]!.disposed).toBe(true)
    expect(readNodeTweakValues(server2.serverDoc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toHaveLength(1)

    // Switch back to art_board_1: shared doc is clean AND local preview is restored ready for retry.
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toHaveLength(0)
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )
    const srcdoc = wrapper.find('iframe.proto-iframe').attributes('srcdoc') ?? ''
    expect(srcdoc).toContain('#10b981')

    // Retrying from the restored state sends the tweaks successfully.
    const retryClick = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await retryClick
    await flushPromises()

    expect(sendFeedback).toHaveBeenCalledTimes(2)
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('第 1 轮反馈已发送')

    wrapper.unmount()
  })

  test('reconciles server Yjs rollback on return when WebSocket was offline during mid-flight failure', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })

    // Switch to art_board_2 and drop art_board_1's WebSocket before the request fails so the
    // immediate rollback cannot reach server1.
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    server1.sessions[0]!.disconnect()

    rejectSend(new Error('offline'))
    await clickSend
    await flushPromises()

    // Server still holds the dirty tweakValues while offline, and a pending rollback is recorded.
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })
    expect(server1.sessions[0]!.disposed).toBe(true)
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toHaveLength(1)

    // Switch back to art_board_1 with an async initial room sync (like a real WebSocket handshake).
    server1.deferReady = true
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()
    expect(server1.sessions).toHaveLength(2)
    expect(server1.sessions[1]!.ready.value).toBe(false)

    // Completing sync reconciles the rollback silently without triggering auto-send pending state.
    server1.sessions[1]!.completeSync()
    await flushPromises()
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toHaveLength(0)
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('rolls back tweaks and preserves pre-existing pending edits when empty feedback returns after switching artifacts', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    // Create a normal board edit so art_board_1 has pendingChanges = 1 before tweaking prototype.
    const session1Doc = server1.sessions[0]!.doc
    session1Doc.transact(() => {
      const sticky = new Y.Map<unknown>()
      sticky.set('type', 'sticky')
      sticky.set('data', { text: '先记一条待同步修改' })
      session1Doc.getMap('nodes').set('sticky-1', sticky)
    })
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)

    let resolveFirst!: (val: { feedback: null }) => void
    sendFeedback
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockResolvedValueOnce({ feedback: null })

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()

    // Switch to art_board_2 mid-flight.
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()

    // Both initial send and 250ms retry return empty feedback.
    resolveFirst({ feedback: null })
    await vi.advanceTimersByTimeAsync(300)
    await clickSend
    await flushPromises()

    expect(sendFeedback).toHaveBeenCalledTimes(2)
    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toEqual({
      pendingChanges: 1,
      pendingNotes: 0,
      paused: true,
    })

    // Returning to art_board_1 restores both the paused feedback bar and retryable local tweaks.
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('does not overwrite concurrent remote/AI tweakValues when a switched-away send fails', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()

    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()

    // Remote collaborator / AI updates proto-1 tweakValues on server1 before the send fails.
    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      const prev = fields.get('data') as PrototypeNodeData
      fields.set('data', { ...prev, tweakValues: { dense: true } })
    })

    rejectSend(new Error('send failed'))
    await clickSend
    await flushPromises()

    // Remote tweakValues must remain intact and no stale rollback should be queued.
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ dense: true })
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toHaveLength(0)

    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.proto-tweak-count').text()).toBe('1')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('当前参数已发送')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(true)

    wrapper.unmount()
  })

  test('recovers cleanly when user switches away and returns to original board before send fails', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()

    // Switch to art_board_2 and then immediately back to art_board_1 while still in flight.
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    // Button stays disabled while the original send is still in flight for art_board_1.
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(true)

    rejectSend(new Error('network error'))
    await clickSend
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(server1.sessions[0]!.disposed).toBe(true)
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect(wrapper.find('.tweaks-footer button').text()).toContain('将当前参数发给 AI')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('rolls back server Yjs and restores latest preview when switching away during in-flight withdraw', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, withdrawFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })

    // Adjust locally again during the 60s withdraw window.
    panel.vm.$emit('change', { accent: '#10b981', gap: 30, dense: true, cta: '立即开始' })
    await flushPromises()

    let resolveWithdraw!: (val: { withdrawn: boolean }) => void
    withdrawFeedback.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveWithdraw = resolve
        }),
    )

    const withdrawBtn = wrapper
      .findAll('.artifact-feedback-bar button')
      .find((btn) => btn.text().includes('撤回'))
    const clickWithdraw = withdrawBtn!.trigger('click')
    await flushPromises()

    // Switch to art_board_2 while withdraw is still in flight.
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    expect(server1.sessions[0]!.disposed).toBe(false)

    resolveWithdraw({ withdrawn: true })
    await clickWithdraw
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(server1.sessions[0]!.disposed).toBe(true)

    // Switch back to art_board_1: latest local preview (3 modified tweaks) is restored for retry.
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('3')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )
    const srcdoc = wrapper.find('iframe.proto-iframe').attributes('srcdoc') ?? ''
    expect(srcdoc).toContain('#10b981')
    expect(srcdoc).toContain('30')

    wrapper.unmount()
  })

  test('rolls back server Yjs and restores preview when switching artifacts during the 120ms pre-send delay', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))
    sendFeedback.mockRejectedValueOnce(new Error('timeout'))

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    // Click send, then immediately switch away before the 120ms settle wait expires.
    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })

    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(150)
    await clickSend
    await flushPromises()

    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(server1.sessions[0]!.disposed).toBe(true)

    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('does not mutate disconnected Y.Doc on same-board failure so reconnect never overwrites concurrent AI edits', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const session = server1.sessions[0]!
    // Ensure clientDoc would win a concurrent Y.Map LWW conflict if an offline write ever occurred.
    session.doc.clientID = server1.serverDoc.clientID + 100

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })

    // Lose connection on the active board while sendFeedback is in flight.
    session.disconnect()
    let offlineClientWrites = 0
    const countOfflineWrite = () => {
      offlineClientWrites++
    }
    session.doc.on('update', countOfflineWrite)

    // Meanwhile AI updates the prototype on the server while the client is offline.
    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      fields.set('data', {
        ...(fields.get('data') as PrototypeNodeData),
        html: '<!doctype html><html><body>AI v2 baked</body></html>',
        tweakValues: { accent: '#f97316' },
      })
    })

    // sendFeedback rejects while disconnected.
    rejectSend(new Error('network offline'))
    await clickSend
    await flushPromises()

    session.doc.off('update', countOfflineWrite)
    // Crucial: zero local Y.Doc mutations occurred while disconnected.
    expect(offlineClientWrites).toBe(0)

    // Reconnect and sync bidirectional state: AI's update must survive intact.
    session.reconnect()
    await flushPromises()
    await flushPromises()

    expect(readNodeData(server1.serverDoc)?.html).toBe(
      '<!doctype html><html><body>AI v2 baked</body></html>',
    )
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#f97316' })
    expect(readNodeData(session.doc)?.html).toBe(
      '<!doctype html><html><body>AI v2 baked</body></html>',
    )
    expect(readNodeTweakValues(session.doc)).toEqual({ accent: '#f97316' })
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toEqual([])

    wrapper.unmount()
  })

  test('defers offline same-board rollback until reconnect and reconciles cleanly when server is unchanged', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSend = reject
        }),
    )

    const session = server1.sessions[0]!
    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 24, dense: false, cta: '立即开始' })
    await flushPromises()

    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981', gap: 24 })

    session.disconnect()
    let offlineClientWrites = 0
    const countOfflineWrite = () => {
      offlineClientWrites++
    }
    session.doc.on('update', countOfflineWrite)

    rejectSend(new Error('network offline'))
    await clickSend
    await flushPromises()

    session.doc.off('update', countOfflineWrite)
    expect(offlineClientWrites).toBe(0)
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toHaveLength(1)

    // Reconnect while server still holds the unsent tweak values -> reconcile rolls back and restores retry.
    session.reconnect()
    await flushPromises()
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(getPendingPrototypeTweakRollbacks('art_board_1')).toEqual([])
    expect(wrapper.find('.proto-tweak-count').text()).toBe('2')
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    wrapper.unmount()
  })

  test('clears and restores pending count consistently when switching away and back during 120ms wait', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) => Promise.resolve(boardDetail(artifactId)))

    let rejectFirstSend!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectFirstSend = reject
        }),
    )

    // Create 2 unrelated pending board edits first.
    const addStickyEdit = (text: string) => {
      const doc = server1.sessions[server1.sessions.length - 1]!.doc
      doc.transact(() => {
        const nodes = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
        const sticky = new Y.Map<unknown>()
        sticky.set('id', 'sticky-1')
        sticky.set('type', 'sticky')
        sticky.set('data', { text })
        nodes.set('sticky-1', sticky)
      })
    }
    addStickyEdit('edit 1')
    addStickyEdit('edit 2')
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)

    const panel = wrapper.findComponent(PrototypeTweaksPanel)
    panel.vm.$emit('change', { accent: '#10b981', gap: 16, dense: false, cta: '立即开始' })
    await flushPromises()

    // Click send, then immediately switch away and switch right back BEFORE 120ms expires.
    const clickSend = wrapper.find('.tweaks-footer button').trigger('click')
    await flushPromises()

    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    expect(getPendingArtifactFeedback('art_board_1')?.pendingChanges).toBe(2)

    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()
    // Returning recreated a new room and restored pendingChanges = 2 on the active view.
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)

    // Let 120ms settle window finish -> active pending bar must be cleared before sendFeedback runs.
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Case 1: sendFeedback fails -> pendingChanges must restore to 2 (not double to 4!).
    rejectFirstSend(new Error('network error'))
    await clickSend
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(getPendingArtifactFeedback('art_board_1')?.pendingChanges).toBe(2)
    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').text()).toBe('1')

    // Case 2: retry send, again switch away and back within 120ms, this time succeed -> no lingering pending count.
    let resolveSecondSend!: (value: {
      feedback: {
        artifactId: string
        round: number
        version: number
        notify: string
        createdAt: number
        items: []
        images: []
        doneBy: null
        doneAt: null
        summary: null
      }
    }) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSecondSend = resolve
        }),
    )

    const retrySend = wrapper.find('.tweaks-footer button').trigger('click')
    await flushPromises()

    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()
    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    resolveSecondSend({
      feedback: {
        artifactId: 'art_board_1',
        round: 2,
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
    await retrySend
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    // Leaving the board must not resurrect stale pendingChanges in feedback-pending.
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    expect(getPendingArtifactFeedback('art_board_1')).toBeUndefined()

    wrapper.unmount()
  })

  test('does not re-apply old tweaks when reset-to-default fails and AI later updates the prototype before returning', async () => {
    const server1: MockServerRoom = { serverDoc: seedBoardDoc({ accent: '#10b981' }), sessions: [] }
    const server2: MockServerRoom = { serverDoc: seedBoardDoc(), sessions: [] }
    serverRoomsByArtifact.set('art_board_1', server1)
    serverRoomsByArtifact.set('art_board_2', server2)

    let board1Version = 1
    let board1UpdatedBy: 'agent' | 'user' = 'agent'
    const { wrapper, getArtifact, sendFeedback } = await mountBoardEditor(
      server1.serverDoc,
      'art_board_1',
    )
    getArtifact.mockImplementation((artifactId) =>
      Promise.resolve(
        artifactId === 'art_board_1'
          ? boardDetail(artifactId, board1Version, board1UpdatedBy)
          : boardDetail(artifactId),
      ),
    )

    // Case 1: Online rollback already reverted server to #10b981; AI then clears tweakValues
    // (even without changing HTML or bumping artifact version yet) -> returning must not overwrite.
    let rejectOnlineReset!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectOnlineReset = reject
        }),
    )

    wrapper.findComponent(PrototypeTweaksPanel).vm.$emit('reset')
    await flushPromises()
    expect((wrapper.find('.tweaks-footer button').element as HTMLButtonElement).disabled).toBe(
      false,
    )

    const clickOnlineReset = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()

    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()

    rejectOnlineReset(new Error('network error'))
    await clickOnlineReset
    await flushPromises()
    expect(readNodeTweakValues(server1.serverDoc)).toEqual({ accent: '#10b981' })

    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      const nextData = { ...(fields.get('data') as PrototypeNodeData) }
      delete nextData.tweakValues
      fields.set('data', nextData)
    })

    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)

    // Case 2: Offline reset failure (needsDocRollback = true); AI bakes changes into HTML and
    // clears tweakValues without bumping version yet -> definitionKey guard protects AI's HTML.
    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      fields.set('data', {
        ...(fields.get('data') as PrototypeNodeData),
        tweakValues: { accent: '#10b981' },
      })
    })
    await flushPromises()

    let rejectOfflineHtmlReset!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectOfflineHtmlReset = reject
        }),
    )

    wrapper.findComponent(PrototypeTweaksPanel).vm.$emit('reset')
    await flushPromises()
    const clickOfflineHtmlReset = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()

    const heldSessionHtml = server1.sessions[server1.sessions.length - 1]!
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()

    heldSessionHtml.disconnect()
    rejectOfflineHtmlReset(new Error('offline'))
    await clickOfflineHtmlReset
    await flushPromises()

    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      const nextData = { ...(fields.get('data') as PrototypeNodeData) }
      nextData.html = '<!doctype html><html><body>AI baked accent into HTML</body></html>'
      delete nextData.tweakValues
      fields.set('data', nextData)
    })

    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(readNodeData(server1.serverDoc)?.html).toBe(
      '<!doctype html><html><body>AI baked accent into HTML</body></html>',
    )
    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)

    // Case 3: Offline reset failure (needsDocRollback = true); AI bumps artifact to v2 (agent)
    // and clears tweakValues without changing HTML/tweaks -> non-user version guard prunes rollback.
    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      fields.set('data', {
        ...(fields.get('data') as PrototypeNodeData),
        tweakValues: { accent: '#10b981' },
      })
    })
    await flushPromises()

    let rejectOfflineVersionReset!: (err: Error) => void
    sendFeedback.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectOfflineVersionReset = reject
        }),
    )

    wrapper.findComponent(PrototypeTweaksPanel).vm.$emit('reset')
    await flushPromises()
    const clickOfflineVersionReset = wrapper.find('.tweaks-footer button').trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await flushPromises()

    const heldSessionVersion = server1.sessions[server1.sessions.length - 1]!
    await wrapper.setProps({ id: 'art_board_2' })
    await flushPromises()
    await flushPromises()

    heldSessionVersion.disconnect()
    rejectOfflineVersionReset(new Error('offline'))
    await clickOfflineVersionReset
    await flushPromises()

    server1.serverDoc.transact(() => {
      const fields = (server1.serverDoc.getMap('nodes') as Y.Map<Y.Map<unknown>>).get('proto-1')!
      const nextData = { ...(fields.get('data') as PrototypeNodeData) }
      delete nextData.tweakValues
      fields.set('data', nextData)
    })
    board1Version = 2
    board1UpdatedBy = 'agent'
    state.artifacts = [{ ...boardDetail('art_board_1', 2, 'agent').artifact }]
    await flushPromises()

    await wrapper.setProps({ id: 'art_board_1' })
    await flushPromises()
    await flushPromises()

    expect(readNodeTweakValues(server1.serverDoc)).toBeUndefined()
    expect(wrapper.find('.proto-tweak-count').exists()).toBe(false)

    wrapper.unmount()
  })
})
