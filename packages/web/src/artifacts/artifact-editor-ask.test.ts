// @vitest-environment happy-dom
import type {
  ArtifactVersionView,
  ArtifactView,
  DocumentAskAnswerBaseline,
  DocumentAskBaseline,
} from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, onBeforeUnmount, ref, shallowRef, watch, type Ref } from 'vue'
import * as Y from 'yjs'
import { api } from '../api'
import { setLocale } from '../i18n'
import { state } from '../store'
import type { AskBlockProps } from './document-block-data'
import {
  applyRecommendedDocumentAsks,
  readSharedDocumentAsks,
  snapshotDocumentAsks,
} from './document-asks'
import { clearPendingArtifactFeedback, getPendingArtifactFeedback } from './feedback-pending'

let currentSharedDoc: Y.Doc | null = null
let currentSharedReady = ref(true)
const sharedByArtifact = new Map<string, { doc: Y.Doc; ready: Ref<boolean> }>()
const readonlyAsksByContent = new Map<string, AskBlockProps[]>()
const fakeProvider = { awareness: null, destroy: () => {} }

vi.mock('./shared', () => ({
  useSharedArtifact: (artifactId: string) => {
    const entry = sharedByArtifact.get(artifactId)
    if (entry) {
      return {
        doc: entry.doc,
        awareness: null,
        ready: entry.ready,
        connected: ref(true),
        provider: shallowRef(fakeProvider),
      }
    }
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
          : (readonlyAsksByContent.get(props.content) ?? [])
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
      } else {
        watch(() => props.content, emitAsks, { immediate: true })
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

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

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

function docDetail(
  id: string,
  version: number,
  askBaseline: DocumentAskBaseline | null,
  options: { content?: string; versions?: ArtifactVersionView[] } = {},
) {
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
      content: options.content ?? '# 方案评审',
    } as ArtifactView,
    versions: options.versions ?? [],
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
    sharedByArtifact.clear()
    readonlyAsksByContent.clear()
    state.artifacts = []
    clearPendingArtifactFeedback('art_doc_1')
    clearPendingArtifactFeedback('art_doc_2')
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    sharedByArtifact.clear()
    readonlyAsksByContent.clear()
    clearPendingArtifactFeedback('art_doc_1')
    clearPendingArtifactFeedback('art_doc_2')
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

  test('does not let in-flight sendFeedback from a previous artifact overwrite baseline or state after switching tabs', async () => {
    const doc1 = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '文档一问题？',
        pick: 'one',
        options: [
          { id: 'redis', label: 'Redis' },
          { id: 'mem', label: '内存' },
        ],
        selected: ['redis'],
      },
    ])
    const doc2 = seedDoc([
      {
        key: 'b1',
        id: 'ask-2',
        question: '文档二问题？',
        pick: 'one',
        options: [
          { id: 'docker', label: 'Docker' },
          { id: 'bin', label: '单二进制' },
        ],
        selected: ['docker'],
      },
    ])
    sharedByArtifact.set('art_doc_1', { doc: doc1.doc, ready: ref(true) })
    sharedByArtifact.set('art_doc_2', { doc: doc2.doc, ready: ref(true) })

    const doc2DetailDeferred = createDeferred<ReturnType<typeof docDetail>>()
    vi.spyOn(api, 'getArtifact').mockImplementation((id) => {
      if (id === 'art_doc_1') {
        return Promise.resolve(
          docDetail('art_doc_1', 1, {
            version: 1,
            asks: [{ id: 'ask-1', selected: [], note: '' }],
          }),
        )
      }
      return doc2DetailDeferred.promise
    })

    const sendDeferred = createDeferred<NonNullable<Awaited<ReturnType<typeof api.sendFeedback>>>>()
    vi.spyOn(api, 'sendFeedback').mockImplementation(() => sendDeferred.promise)

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

    // Start submitting art_doc_1 while it is still pending.
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    const clickPromise = wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()

    // Switch tab to art_doc_2 before art_doc_1's sendFeedback resolves.
    await wrapper.setProps({ id: 'art_doc_2' })
    await flushPromises()

    // Now art_doc_1's sendFeedback finishes while art_doc_2's getArtifact is still in flight.
    sendDeferred.resolve({
      feedback: {
        artifactId: 'art_doc_1',
        round: 1,
        version: 2,
        notify: 'thr_1',
        items: [],
        images: [],
        doneBy: null,
        doneAt: null,
        summary: null,
        createdAt: Date.now(),
      },
    })
    await clickPromise
    await flushPromises()

    // art_doc_2 should not inherit art_doc_1's sent round banner or bumped baseline epoch.
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // art_doc_2's load completes with its already-submitted baseline -> submit bar stays hidden.
    doc2DetailDeferred.resolve(
      docDetail('art_doc_2', 2, {
        version: 2,
        asks: [{ id: 'ask-2', selected: ['docker'], note: '' }],
      }),
    )
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    wrapper.unmount()
  })

  test('persists failed sendFeedback pending count to the original artifact without polluting the newly opened tab', async () => {
    const doc1 = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '文档一问题？',
        pick: 'one',
        options: [{ id: 'redis', label: 'Redis' }],
        selected: ['redis'],
      },
    ])
    const doc2 = seedDoc([
      {
        key: 'b1',
        id: 'ask-2',
        question: '文档二问题？',
        pick: 'one',
        options: [{ id: 'docker', label: 'Docker' }],
        selected: ['docker'],
      },
    ])
    sharedByArtifact.set('art_doc_1', { doc: doc1.doc, ready: ref(true) })
    sharedByArtifact.set('art_doc_2', { doc: doc2.doc, ready: ref(true) })

    vi.spyOn(api, 'getArtifact').mockImplementation((id) => {
      if (id === 'art_doc_1') {
        return Promise.resolve(
          docDetail('art_doc_1', 1, {
            version: 1,
            asks: [{ id: 'ask-1', selected: [], note: '' }],
          }),
        )
      }
      return Promise.resolve(
        docDetail('art_doc_2', 2, {
          version: 2,
          asks: [{ id: 'ask-2', selected: ['docker'], note: '' }],
        }),
      )
    })

    const sendDeferred = createDeferred<NonNullable<Awaited<ReturnType<typeof api.sendFeedback>>>>()
    vi.spyOn(api, 'sendFeedback').mockImplementation(() => sendDeferred.promise)

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

    // Make a prose edit on art_doc_1 so initialPending > 0, then click submit.
    doc1.doc.transact(() => doc1.paraText.insert(doc1.paraText.length, ' 补充内容'))
    await flushPromises()
    const clickPromise = wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()

    // Switch to art_doc_2 before sendFeedback fails.
    await wrapper.setProps({ id: 'art_doc_2' })
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    sendDeferred.reject(new Error('network error'))
    await clickPromise
    await flushPromises()

    // art_doc_2 stays clean while art_doc_1 keeps its restored pending count in store.
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_doc_1')).toEqual({
      pendingChanges: 1,
      pendingNotes: 0,
      paused: true,
    })

    wrapper.unmount()
  })

  test('does not let in-flight withdrawSentFeedback overwrite the new artifact baseline or pending state after switching tabs', async () => {
    const doc1 = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '文档一问题？',
        pick: 'one',
        options: [{ id: 'redis', label: 'Redis' }],
        selected: ['redis'],
      },
    ])
    const doc2 = seedDoc([
      {
        key: 'b1',
        id: 'ask-2',
        question: '文档二问题？',
        pick: 'one',
        options: [{ id: 'docker', label: 'Docker' }],
        selected: ['docker'],
      },
    ])
    sharedByArtifact.set('art_doc_1', { doc: doc1.doc, ready: ref(true) })
    sharedByArtifact.set('art_doc_2', { doc: doc2.doc, ready: ref(true) })

    const doc2DetailDeferred = createDeferred<ReturnType<typeof docDetail>>()
    vi.spyOn(api, 'getArtifact').mockImplementation((id) => {
      if (id === 'art_doc_1') {
        return Promise.resolve(
          docDetail('art_doc_1', 1, {
            version: 1,
            asks: [{ id: 'ask-1', selected: [], note: '' }],
          }),
        )
      }
      return doc2DetailDeferred.promise
    })
    vi.spyOn(api, 'sendFeedback').mockResolvedValue({
      feedback: {
        artifactId: 'art_doc_1',
        round: 1,
        version: 2,
        notify: 'thr_1',
        items: [],
        images: [],
        doneBy: null,
        doneAt: null,
        summary: null,
        createdAt: Date.now(),
      },
    })
    const withdrawDeferred = createDeferred<{ withdrawn: boolean }>()
    vi.spyOn(api, 'withdrawFeedback').mockImplementation(() => withdrawDeferred.promise)

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

    // Edit prose + submit art_doc_1 so hadPending = 1, then trigger withdraw.
    doc1.doc.transact(() => doc1.paraText.insert(doc1.paraText.length, ' 补充内容'))
    await flushPromises()
    await wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('第 1 轮')

    const withdrawPromise = wrapper.find('.artifact-feedback-bar button').trigger('click')
    await flushPromises()

    // Switch tab to art_doc_2 while withdraw is still in flight.
    await wrapper.setProps({ id: 'art_doc_2' })
    await flushPromises()

    withdrawDeferred.resolve({ withdrawn: true })
    await withdrawPromise
    await flushPromises()

    // Resolve art_doc_2 load after art_doc_1 withdraw settled: art_doc_2 keeps its real baseline and no pending bar.
    doc2DetailDeferred.resolve(
      docDetail('art_doc_2', 2, {
        version: 2,
        asks: [{ id: 'ask-2', selected: ['docker'], note: '' }],
      }),
    )
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)
    expect(getPendingArtifactFeedback('art_doc_1')).toEqual({
      pendingChanges: 1,
      pendingNotes: 0,
      paused: true,
    })

    wrapper.unmount()
  })

  test('clears stale historical asks when returning from an older version before the live Y.Doc becomes ready', async () => {
    const liveReady = ref(true)
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
        selected: ['redis'],
      },
    ])
    sharedByArtifact.set('art_doc_1', { doc, ready: liveReady })
    readonlyAsksByContent.set('# 旧版本 v1', [
      {
        id: 'ask-1',
        question: '缓存选型？',
        description: '',
        pick: 'one',
        options: [
          { id: 'redis', label: 'Redis' },
          { id: 'mem', label: '内存' },
        ],
        allowNote: true,
        selected: [],
        note: '',
        confirmed: false,
      },
    ])

    const versions: ArtifactVersionView[] = [
      { version: 2, title: '方案评审', author: 'user', reason: 'answered', createdAt: 2000 },
      { version: 1, title: '方案评审', author: 'agent', reason: 'initial', createdAt: 1000 },
    ]
    vi.spyOn(api, 'listArtifactVersions').mockResolvedValue({ versions })
    vi.spyOn(api, 'getArtifact').mockImplementation((_id, opts) => {
      if (opts?.version === 1) {
        return Promise.resolve(
          docDetail('art_doc_1', 1, null, { content: '# 旧版本 v1', versions }),
        )
      }
      return Promise.resolve(
        docDetail('art_doc_1', 2, {
          version: 2,
          asks: [{ id: 'ask-1', selected: ['redis'], note: '' }],
        }),
      )
    })

    const DouPopoverStub = defineComponent({
      name: 'DouPopover',
      props: { open: Boolean },
      emits: ['update:open'],
      template: '<div><slot /><slot name="content" :close="() => {}" /></div>',
    })
    const ArtifactHistoryStub = defineComponent({
      name: 'ArtifactHistory',
      emits: ['select', 'retry'],
      template: '<button class="pick-v1" @click="$emit(\'select\', 1)">v1</button>',
    })

    const wrapper = mount(ArtifactEditor, {
      props: { id: 'art_doc_1', place: 'full' },
      global: {
        stubs: {
          ViewActions: true,
          CommentList: true,
          DouPopover: DouPopoverStub,
          ArtifactHistory: ArtifactHistoryStub,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    // Latest version is fully submitted -> bar hidden.
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Switch to historical version 1 (read-only DocumentBody emits unanswered ask-1).
    await wrapper.find('.artifact-menu-item').trigger('click')
    await flushPromises()
    await wrapper.find('.pick-v1').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('.history-context').exists()).toBe(true)
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Return to latest while the new Yjs room is still syncing (ready === false).
    liveReady.value = false
    const returnLatestBtn = wrapper.find('.history-actions button')
    await returnLatestBtn.trigger('click')
    await flushPromises()
    await flushPromises()

    // No stale historical ask bar may flash before live Y.Doc finishes syncing.
    expect(wrapper.find('.history-context').exists()).toBe(false)
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Once Y.Doc becomes ready, latest answered asks match baseline -> bar stays hidden.
    liveReady.value = true
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    // Editing the live ask now shows the submit bar as expected.
    doc.transact(() => propsById.get('ask-1')!.set('selected', ['mem']))
    await flushPromises()
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')

    wrapper.unmount()
  })

  test('syncs askBaseline from server when explicit document submit returns empty feedback after retry', async () => {
    const { doc } = seedDoc([
      {
        key: 'b1',
        id: 'ask-1',
        question: '缓存选型？',
        pick: 'one',
        options: [
          { id: 'redis', label: 'Redis' },
          { id: 'mem', label: '内存' },
        ],
        selected: ['redis'],
      },
    ])

    let serverBaseline: DocumentAskAnswerBaseline[] = [{ id: 'ask-1', selected: [], note: '' }]
    const getArtifact = vi
      .spyOn(api, 'getArtifact')
      .mockImplementation(() =>
        Promise.resolve(docDetail('art_doc_1', 2, { version: 2, asks: serverBaseline })),
      )
    const sendFeedback = vi.spyOn(api, 'sendFeedback').mockResolvedValue({ feedback: null })
    currentSharedDoc = doc

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

    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')

    // Case 1: WebSocket update has not reached server yet -> server still has old baseline -> bar remains visible.
    const firstClick = wrapper.find('.artifact-feedback-bar button').trigger('click')
    await vi.advanceTimersByTimeAsync(260)
    await firstClick
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(2)
    expect(getArtifact).toHaveBeenCalledTimes(2)
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    expect(
      (wrapper.find('.artifact-feedback-bar button').element as HTMLButtonElement).disabled,
    ).toBe(false)

    // Case 2: Already submitted elsewhere / previous request landed on server -> server baseline matches -> bar hides.
    serverBaseline = [{ id: 'ask-1', selected: ['redis'], note: '' }]
    const secondClick = wrapper.find('.artifact-feedback-bar button').trigger('click')
    await vi.advanceTimersByTimeAsync(260)
    await secondClick
    await flushPromises()
    expect(sendFeedback).toHaveBeenCalledTimes(4)
    expect(getArtifact).toHaveBeenCalledTimes(3)
    expect(wrapper.find('.artifact-feedback-bar').exists()).toBe(false)

    wrapper.unmount()
  })
})
