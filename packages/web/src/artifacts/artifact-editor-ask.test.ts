// @vitest-environment happy-dom
import type { ArtifactView, DocumentAskAnswerBaseline, DocumentAskBaseline } from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import * as Y from 'yjs'
import { api } from '../api'
import { setLocale } from '../i18n'
import { state } from '../store'
import {
  applyRecommendedDocumentAsks,
  readSharedDocumentAsks,
  snapshotDocumentAsks,
} from './document-asks'
import { clearPendingArtifactFeedback } from './feedback-pending'

let currentSharedDoc: Y.Doc | null = null
let currentSharedReady = ref(true)
const fakeProvider = { awareness: null, destroy: () => {} }

vi.mock('./shared', () => ({
  useSharedArtifact: () => {
    const doc = currentSharedDoc ?? new Y.Doc()
    currentSharedDoc = doc
    return {
      doc,
      awareness: null,
      ready: currentSharedReady,
      connected: ref(true),
      provider: shallowRef(fakeProvider),
    }
  },
}))

vi.mock('./DocumentBody.vue', () => ({
  __esModule: true,
  default: defineComponent({
    name: 'DocumentBody',
    props: {
      shared: { type: Object, default: undefined },
      content: { type: String, default: '' },
      readonly: { type: Boolean, default: false },
    },
    emits: ['asks', 'comment'],
    setup(props, { emit, expose }) {
      const readAsks = () =>
        props.shared
          ? readSharedDocumentAsks(props.shared.doc.getMap('document')).map((e) => e.ask)
          : []
      const emitAsks = () => emit('asks', readAsks())

      if (props.shared) {
        const root = props.shared.doc.getMap('document')
        const onChange = () => {
          if (props.shared!.ready.value) emitAsks()
        }
        root.observeDeep(onChange)
        watch(
          () => props.shared!.ready.value,
          (now) => now && emitAsks(),
          { immediate: true },
        )
        onBeforeUnmount(() => root.unobserveDeep(onChange))
      }

      expose({
        applyRecommendedPicks: () =>
          props.shared ? applyRecommendedDocumentAsks(props.shared.doc) : 0,
        currentAskSnapshot: () => snapshotDocumentAsks(readAsks()),
      })

      return () => null
    },
  }),
}))

import ArtifactEditor from './ArtifactEditor.vue'

function seedDoc(
  asks: Array<{
    key: string
    id: string
    question: string
    pick?: 'one' | 'many' | 'text'
    options?: Array<{ id: string; label: string; recommended?: boolean }>
    selected?: string[]
    note?: string
    confirmed?: boolean
  }>,
) {
  const doc = new Y.Doc()
  const root = doc.getMap('document')
  const propsById = new Map<string, Y.Map<unknown>>()
  for (const item of asks) {
    const block = new Y.Map<unknown>()
    block.set('type', 'ask')
    const p = new Y.Map<unknown>()
    p.set('id', item.id)
    p.set('question', item.question)
    p.set('pick', item.pick ?? 'one')
    p.set('options', item.options ?? [])
    p.set('selected', item.selected ?? [])
    p.set('note', item.note ?? '')
    p.set('confirmed', item.confirmed ?? false)
    block.set('props', p)
    root.set(item.key, block)
    propsById.set(item.id, p)
  }
  const para = new Y.Map<unknown>()
  para.set('type', 'paragraph')
  const paraText = new Y.Text('初始正文')
  para.set('text', paraText)
  root.set('p1', para)
  return { doc, propsById, paraText }
}

function docDetail(id: string, version: number, askBaseline: DocumentAskBaseline | null) {
  return {
    artifact: {
      id,
      projectId: 'proj_1',
      kind: 'document' as const,
      title: '方案评审',
      nodeId: null,
      version,
      createdBy: 'agent',
      updatedBy: 'agent',
      createdAt: 1000,
      updatedAt: 1000,
      preview: { kind: 'document' as const, excerpt: '' },
      openComments: 0,
      commentedAt: 0,
      content: '# 方案评审',
    } as ArtifactView,
    versions: [],
    comments: [],
    askBaseline,
  }
}

async function mountEditor(doc: Y.Doc, baseline: DocumentAskAnswerBaseline[], version = 1) {
  currentSharedDoc = doc
  const getArtifact = vi
    .spyOn(api, 'getArtifact')
    .mockResolvedValue(docDetail('art_doc_1', version, { version, asks: baseline }))
  const sendFeedback = vi.spyOn(api, 'sendFeedback').mockResolvedValue({
    feedback: {
      artifactId: 'art_doc_1',
      round: 1,
      version: version + 1,
      notify: 'thr_1',
      items: [],
      images: [],
      doneBy: null,
      doneAt: null,
      summary: null,
      createdAt: Date.now(),
    },
  })
  const withdrawFeedback = vi.spyOn(api, 'withdrawFeedback').mockResolvedValue({
    withdrawn: true,
  })

  const wrapper = mount(ArtifactEditor, {
    props: { id: 'art_doc_1', place: 'full' },
    global: {
      stubs: {
        ViewActions: true,
        ArtifactHistory: true,
        CommentList: true,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  return { wrapper, getArtifact, sendFeedback, withdrawFeedback }
}

describe('ArtifactEditor inline ask submit button visibility', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setLocale('zh-CN')
    currentSharedReady = ref(true)
    currentSharedDoc = null
    state.artifacts = []
    clearPendingArtifactFeedback('art_doc_1')
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    clearPendingArtifactFeedback('art_doc_1')
  })

  test('hides submit bar after all asks are submitted and unchanged, and restores on edits or withdraw', async () => {
    const { doc, propsById, paraText } = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '缓存选型？',
        pick: 'one',
        options: [
          { id: 'redis', label: 'Redis' },
          { id: 'mem', label: '内存' },
        ],
      },
      {
        key: 'b2',
        id: 'ask-2',
        question: '补充说明？',
        pick: 'text',
      },
    ])

    const { wrapper, sendFeedback, withdrawFeedback } = await mountEditor(doc, [
      { id: 'ask-1', selected: [], note: '' },
      { id: 'ask-2', selected: [], note: '' },
    ])

    // 1. Initial unanswered state: ask bar visible, submit button disabled.
    const initialBtn = wrapper.find('.artifact-feedback-bar button')
    expect(initialBtn.exists()).toBe(true)
    expect(initialBtn.text()).toContain('提交回答')
    expect((initialBtn.element as HTMLButtonElement).disabled).toBe(true)

    // 2. Answer both asks: submit button becomes enabled.
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['redis']))
    doc.transact(() => propsById.get('ask-2')!.set('note', '优先多实例'))
    await flushPromises()
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(false)

    // 3. Click submit: enters 60s sent/withdraw state.
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('第 1 轮')

    // 4. Withdraw during 60s window: immediately restores submit button.
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()
    expect(withdrawFeedback).toHaveBeenCalledWith('art_doc_1', 1)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(false)

    // 5. Submit again and let the 60s withdraw window expire -> bar hides completely!
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('第 1 轮')

    vi.advanceTimersByTime(61_000)
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // 6. Modify a submitted option -> submit button reappears!
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['mem']))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(false)

    // 7. Revert back to the submitted option -> submit button hides again with no phantom pending bar!
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['redis']))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // 8. Modify note -> submit button reappears -> revert note -> hides again!
    doc.transact(() => propsById.get('ask-2')!.set('note', '单机即可'))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    doc.transact(() => propsById.get('ask-2')!.set('note', '优先多实例'))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // 9. Prose edit while asks are already submitted & unchanged -> shows normal pending bar, NOT ask submit.
    doc.transact(() => paraText.insert(4, '补充一句'))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('立即发送')
    expect(wrapper.find('.artifact-feedback-bar').text()).not.toContain('提交回答')

    wrapper.unmount()
  })

  test('stays hidden on reload when server baseline already matches all answered asks', async () => {
    const { doc, propsById } = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '数据库选型？',
        pick: 'one',
        options: [
          { id: 'pg', label: 'PostgreSQL', recommended: true },
          { id: 'mysql', label: 'MySQL' },
        ],
        selected: ['pg'],
        note: '默认主库',
      },
    ])

    const { wrapper } = await mountEditor(
      doc,
      [{ id: 'ask-1', selected: ['pg'], note: '默认主库' }],
      2,
    )

    // On initial mount after reload, all asks are answered & match baseline -> hidden immediately!
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Editing the answer brings back the submit button.
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['mysql']))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')

    wrapper.unmount()
  })

  test('stays visible on reload when answers were edited but never submitted', async () => {
    const { doc } = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '数据库选型？',
        pick: 'one',
        options: [
          { id: 'pg', label: 'PostgreSQL' },
          { id: 'mysql', label: 'MySQL' },
        ],
        selected: ['pg'],
      },
    ])

    const { wrapper } = await mountEditor(doc, [{ id: 'ask-1', selected: [], note: '' }], 2)

    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(false)

    wrapper.unmount()
  })

  test('applies recommended fallback on submit, hides after withdraw window, and restores on edit', async () => {
    const { doc, propsById } = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '缓存选型？',
        pick: 'one',
        options: [
          { id: 'redis', label: 'Redis', recommended: true },
          { id: 'mem', label: '内存' },
        ],
      },
    ])

    const { wrapper, sendFeedback } = await mountEditor(doc, [
      { id: 'ask-1', selected: [], note: '' },
    ])

    // Recommended option allows submitting directly without clicking the option first.
    const submitBtn = wrapper.find('.artifact-feedback-bar button')
    expect(submitBtn.exists()).toBe(true)
    expect((submitBtn.element as HTMLButtonElement).disabled).toBe(false)

    const clickPromise = submitBtn.trigger('click')
    await vi.advanceTimersByTimeAsync(150)
    await clickPromise
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(1)
    expect(propsById.get('ask-1')!.get('selected')).toEqual(['redis'])

    // After withdraw window ends, all asks are answered & unchanged -> bar hides.
    vi.advanceTimersByTime(61_000)
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Changing away from the recommended pick restores the submit button; changing back hides it.
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['mem']))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')

    doc.transact(() => propsById.get('ask-1')!.set('selected', ['redis']))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    wrapper.unmount()
  })

  test('keeps progress bar visible with disabled submit button after partial submission until all asks are answered', async () => {
    const { doc, propsById } = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '缓存选型？',
        pick: 'one',
        options: [
          { id: 'redis', label: 'Redis' },
          { id: 'mem', label: '内存' },
        ],
      },
      {
        key: 'b2',
        id: 'ask-2',
        question: '部署方式？',
        pick: 'one',
        options: [
          { id: 'docker', label: 'Docker' },
          { id: 'bin', label: '单二进制' },
        ],
      },
    ])

    const { wrapper } = await mountEditor(doc, [
      { id: 'ask-1', selected: [], note: '' },
      { id: 'ask-2', selected: [], note: '' },
    ])

    // Answer only ask-1 and submit round 1.
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['redis']))
    await flushPromises()
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()

    // Let withdraw window expire: ask-2 is still unanswered, so bar stays visible with disabled submit.
    vi.advanceTimersByTime(61_000)
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('1/2')
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(true)

    // Answer ask-2 -> submit button becomes enabled; after submitting & expiry, bar hides.
    doc.transact(() => propsById.get('ask-2')!.set('selected', ['docker']))
    await flushPromises()
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(false)
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()
    vi.advanceTimersByTime(61_000)
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    wrapper.unmount()
  })
})
