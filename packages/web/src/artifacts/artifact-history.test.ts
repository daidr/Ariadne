// @vitest-environment happy-dom
import type { ArtifactSummary, ArtifactView } from '@ariadne/core'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { defineComponent, h, onBeforeUnmount, ref, shallowRef, watch, type Ref } from 'vue'
import * as Y from 'yjs'
import { api } from '../api'
import { setLocale } from '../i18n'
import * as side from '../side/layout'
import {
  artifactNavigation,
  closeFull,
  closePane,
  fullScreenPane,
  fullToSide,
  navigateArtifactInPlace,
  openView,
  setSide,
  state,
  stepArtifactNavigation,
} from '../store'
import { artifactFocusTarget, clearAllArtifactFocus } from './artifact-focus'
import {
  artifactHistoryNeighbors,
  createArtifactHistory,
  parseArtifactHistoryStack,
  pruneArtifactHistory,
  pushArtifactHistory,
  referencedArtifactId,
  stepArtifactHistoryStack,
  syncArtifactHistoryCursor,
  type ArtifactNavigationTarget,
} from './artifact-history'
import { readSharedDocumentAsks, snapshotDocumentAsks } from './document-asks'
import type { AskBlockProps } from './document-block-data'
import { clearPendingArtifactFeedback, getPendingArtifactFeedback } from './feedback-pending'
import RefBlockView from './RefBlockView.vue'

const sharedByArtifact = new Map<string, { doc: Y.Doc; ready: Ref<boolean> }>()
const fakeProvider = { awareness: null, destroy: () => {} }

vi.mock('./shared', () => ({
  useSharedArtifact: (artifactId: string) => {
    let entry = sharedByArtifact.get(artifactId)
    if (!entry) {
      entry = { doc: new Y.Doc(), ready: ref(true) }
      sharedByArtifact.set(artifactId, entry)
    }
    return {
      doc: entry.doc,
      awareness: null,
      ready: entry.ready,
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
      projectId: { type: String, default: '' },
      readonly: { type: Boolean, default: false },
    },
    emits: ['asks', 'comment', 'navigate'],
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
        currentAskSnapshot: () => snapshotDocumentAsks(readAsks()),
      })

      return () => {
        // Parse an embedded ref JSON line in test fixtures and render a real RefBlockView.
        const match = /```ref\n([^\n]+)\n```/.exec(props.content)
        const refData = match ? (JSON.parse(match[1]!) as Record<string, unknown>) : null
        return h('div', { class: 'mock-document-body' }, [
          refData
            ? h(RefBlockView as never, {
                block: {
                  id: 'blk-ref',
                  type: 'ref',
                  props: refData,
                  content: [],
                  children: [],
                },
                editor: {} as never,
                updateProps: vi.fn(),
                selected: false,
                editable: !props.readonly,
                navigate: (target: ArtifactNavigationTarget) => emit('navigate', target),
              })
            : null,
          h(
            'button',
            {
              type: 'button',
              class: 'mock-inline-link',
              onClick: () => {
                const targetId = referencedArtifactId(
                  `/projects/${props.projectId}?modal=artifact:art_dash`,
                  props.projectId,
                )
                if (targetId) emit('navigate', { artifactId: targetId })
              },
            },
            'inline-link',
          ),
        ])
      }
    },
  }),
}))

vi.mock('./WhiteboardBody.vue', () => ({
  __esModule: true,
  default: defineComponent({
    name: 'WhiteboardBody',
    props: {
      artifactId: { type: String, default: '' },
    },
    setup(props) {
      return () => h('div', { class: 'mock-whiteboard-body', 'data-artifact-id': props.artifactId })
    },
  }),
}))

vi.mock('./DashboardBody.vue', () => ({
  __esModule: true,
  default: defineComponent({
    name: 'DashboardBody',
    props: {
      artifactId: { type: String, default: '' },
    },
    setup(props) {
      return () => h('div', { class: 'mock-dashboard-body', 'data-artifact-id': props.artifactId })
    },
  }),
}))

import ArtifactEditor from './ArtifactEditor.vue'

const sideTabs = () => side.groupsOf(state.side.root).flatMap(side.tabsOf)

function seedAskDoc(doc: Y.Doc, ask: AskBlockProps) {
  const root = doc.getMap('document')
  const block = new Y.Map<unknown>()
  block.set('type', 'ask')
  const propsMap = new Y.Map<unknown>()
  for (const [key, value] of Object.entries(ask)) propsMap.set(key, value)
  block.set('props', propsMap)
  root.set('blk-ask-1', block)
}

function makeSummary(id: string, kind: ArtifactSummary['kind'], title: string): ArtifactSummary {
  return {
    id,
    projectId: 'prj_1',
    nodeId: null,
    kind,
    title,
    version: 2,
    updatedBy: 'agent',
    updatedAt: 1000,
    commentedAt: 0,
  } as ArtifactSummary
}

function makeArtifactView(
  id: string,
  kind: ArtifactView['kind'],
  title: string,
  content: string,
): ArtifactView {
  return {
    id,
    projectId: 'prj_1',
    nodeId: null,
    kind,
    title,
    content,
    version: 2,
    updatedBy: 'agent',
    updatedAt: 1000,
    commentedAt: 0,
  } as ArtifactView
}

beforeEach(() => {
  setLocale('zh-CN')
  state.currentId = 'prj_1'
  state.artifacts = []
  state.artifactsLoaded = false
  setSide(side.defaultLayout())
  closeFull()
  state.threads = {
    thr_1: {
      conversation: null,
      messages: [],
      notices: [],
      todo: null,
      model: null,
      more: false,
      from: null,
      loadingEarlier: false,
      seq: null,
    },
  }
  clearAllArtifactFocus()
  sharedByArtifact.clear()
})

afterEach(() => {
  closeFull()
  setSide(side.defaultLayout())
  clearAllArtifactFocus()
  for (const id of ['art_doc', 'art_board', 'art_dash']) clearPendingArtifactFeedback(id)
  vi.restoreAllMocks()
})

describe('artifact history stack helpers', () => {
  test('pushes, steps back/forward, truncates forward branch and avoids consecutive duplicates', () => {
    let stack = pushArtifactHistory(undefined, 'a', 'b', 'grp-1')
    expect(stack).toEqual({
      entries: [{ id: 'a' }, { id: 'b', focusNodeId: 'grp-1' }],
      index: 1,
    })

    // Same-artifact jump does not push a duplicate entry.
    stack = pushArtifactHistory(stack, 'b', 'b', 'opt-2')
    expect(stack.entries).toHaveLength(2)
    expect(stack.entries[1]).toEqual({ id: 'b', focusNodeId: 'opt-2' })

    stack = pushArtifactHistory(stack, 'b', 'c')
    expect(stack.entries.map((e) => e.id)).toEqual(['a', 'b', 'c'])
    expect(stack.index).toBe(2)

    const back1 = stepArtifactHistoryStack(stack, 'c', -1)!
    expect(back1.entry.id).toBe('b')
    expect(back1.entry.focusNodeId).toBe('opt-2')
    expect(artifactHistoryNeighbors(back1.stack, 'b')).toEqual({
      back: { id: 'a' },
      forward: { id: 'c' },
    })

    // Branching from 'b' to 'd' truncates 'c'.
    const branched = pushArtifactHistory(back1.stack, 'b', 'd')
    expect(branched.entries.map((e) => e.id)).toEqual(['a', 'b', 'd'])
    expect(branched.index).toBe(2)
    expect(artifactHistoryNeighbors(branched, 'd').forward).toBeNull()

    // Stepping past ends returns null.
    expect(stepArtifactHistoryStack(branched, 'd', 1)).toBeNull()
    const atA = stepArtifactHistoryStack(
      stepArtifactHistoryStack(branched, 'd', -1)!.stack,
      'b',
      -1,
    )!
    expect(stepArtifactHistoryStack(atA.stack, 'a', -1)).toBeNull()
  })

  test('prunes deleted artifacts and collapses resulting adjacent duplicates', () => {
    let stack = createArtifactHistory('a')
    stack = pushArtifactHistory(stack, 'a', 'b')
    stack = pushArtifactHistory(stack, 'b', 'a')
    // Deleting 'b' turns [a, b, a] at index 2 into [a] -> no navigable history left.
    expect(pruneArtifactHistory(stack, (id) => id !== 'b')).toBeUndefined()

    const abc = pushArtifactHistory(pushArtifactHistory(undefined, 'a', 'b'), 'b', 'c')
    const pruned = pruneArtifactHistory(abc, (id) => id !== 'b')
    expect(pruned).toEqual({
      entries: [{ id: 'a' }, { id: 'c' }],
      index: 1,
    })

    // Deleting the active artifact clears the stack.
    expect(pruneArtifactHistory(abc, (id) => id !== 'c')).toBeUndefined()
  })

  test('syncs cursor with adjacent browser Back/Forward jumps', () => {
    const stack = pushArtifactHistory(undefined, 'a', 'b')
    expect(syncArtifactHistoryCursor(stack, 'a')).toEqual({
      entries: [{ id: 'a' }, { id: 'b' }],
      index: 0,
    })
    expect(syncArtifactHistoryCursor(stack, 'unrelated')).toBeUndefined()
  })

  test('validates serialized history stacks safely', () => {
    const valid = parseArtifactHistoryStack(
      {
        entries: [{ id: 'art_doc' }, { id: 'art_board', focusNodeId: 'grp-1' }],
        index: 1,
      },
      'art_board',
    )
    expect(valid).toEqual({
      entries: [{ id: 'art_doc' }, { id: 'art_board', focusNodeId: 'grp-1' }],
      index: 1,
    })
    expect(
      parseArtifactHistoryStack({ entries: [{ id: 'only' }], index: 0 }, 'only'),
    ).toBeUndefined()
    expect(
      parseArtifactHistoryStack({ entries: [{ id: 'a' }, { id: 'b' }], index: 0 }, 'b'),
    ).toBeUndefined()
    expect(
      parseArtifactHistoryStack({ entries: [{ id: 'a' }, { id: 'b' }], index: 5 }),
    ).toBeUndefined()
    expect(parseArtifactHistoryStack(null)).toBeUndefined()
  })

  test('parses inline artifact: links and same-project artifact URLs', () => {
    expect(referencedArtifactId('artifact:art_123', 'prj_1')).toBe('art_123')
    expect(referencedArtifactId('/projects/prj_1?pane=side&tab=artifact:art_123', 'prj_1')).toBe(
      'art_123',
    )
    expect(referencedArtifactId('/projects/prj_1?modal=artifact:art_456&from=thr_1', 'prj_1')).toBe(
      'art_456',
    )
    expect(
      referencedArtifactId(
        `${window.location.origin}/projects/prj_1?tab=artifact:art_789`,
        'prj_1',
      ),
    ).toBe('art_789')
    expect(referencedArtifactId('/projects/prj_other?tab=artifact:art_123', 'prj_1')).toBeNull()
    expect(
      referencedArtifactId('https://example.com/projects/prj_1?tab=artifact:art_1', 'prj_1'),
    ).toBeNull()
    expect(
      referencedArtifactId('//example.com/projects/prj_1?tab=artifact:art_1', 'prj_1'),
    ).toBeNull()
    expect(
      referencedArtifactId('//example.com/projects/prj_1?modal=artifact:art_1', 'prj_1'),
    ).toBeNull()
    expect(referencedArtifactId('thread:thr_1', 'prj_1')).toBeNull()
  })
})

describe('store in-place artifact navigation and Back/Forward history', () => {
  test('full-screen mode replaces the artifact in place without opening a side tab', async () => {
    state.artifacts = [
      makeSummary('art_doc', 'document', '产品方案'),
      makeSummary('art_board', 'whiteboard', '交互画板'),
      makeSummary('art_dash', 'dashboard', '数据看板'),
    ]
    state.artifactsLoaded = true

    await openView({ kind: 'artifact', id: 'art_doc' }, { from: 'thr_1' })
    expect(state.full).toEqual({
      view: { kind: 'artifact', id: 'art_doc' },
      threadId: 'thr_1',
      fromSide: false,
    })
    expect(artifactNavigation('full', 'art_doc')).toEqual({ back: null, forward: null })

    // Jump in-place from document to whiteboard with group focus.
    navigateArtifactInPlace({
      place: 'full',
      fromId: 'art_doc',
      toId: 'art_board',
      focusNodeId: 'grp-nav',
    })
    expect(state.full).toEqual({
      view: { kind: 'artifact', id: 'art_board' },
      threadId: 'thr_1',
      fromSide: false,
    })
    expect(sideTabs()).not.toContain('artifact:art_board')
    expect(artifactFocusTarget.value).toMatchObject({
      artifactId: 'art_board',
      nodeId: 'grp-nav',
    })
    expect(artifactNavigation('full', 'art_board')).toEqual({
      back: { id: 'art_doc' },
      forward: null,
    })

    // Step back to document.
    expect(stepArtifactNavigation('full', 'art_board', -1)).toBe(true)
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_doc' })
    expect(state.full?.threadId).toBe('thr_1')
    expect(artifactFocusTarget.value).toBeNull()
    expect(artifactNavigation('full', 'art_doc')).toEqual({
      back: null,
      forward: { id: 'art_board', focusNodeId: 'grp-nav' },
    })

    // Step forward back to whiteboard re-triggers target node focus.
    expect(stepArtifactNavigation('full', 'art_doc', 1)).toBe(true)
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_board' })
    expect(artifactFocusTarget.value).toMatchObject({
      artifactId: 'art_board',
      nodeId: 'grp-nav',
    })

    // Step back then jump to dashboard truncates whiteboard from forward history.
    stepArtifactNavigation('full', 'art_board', -1)
    navigateArtifactInPlace({ place: 'full', fromId: 'art_doc', toId: 'art_dash' })
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_dash' })
    expect(artifactNavigation('full', 'art_dash')).toEqual({
      back: { id: 'art_doc' },
      forward: null,
    })

    // Closing full-screen clears full history.
    closeFull()
    expect(artifactNavigation('full', 'art_dash')).toEqual({ back: null, forward: null })
  })

  test('side dock replaces the current artifact tab in place and supports Back/Forward', async () => {
    state.artifacts = [
      makeSummary('art_doc', 'document', '产品方案'),
      makeSummary('art_board', 'whiteboard', '交互画板'),
    ]
    state.artifactsLoaded = true

    await openView({ kind: 'artifact', id: 'art_doc' })
    expect(sideTabs()).toEqual(['plan', 'threads', 'artifacts', 'artifact:art_doc'])

    navigateArtifactInPlace({
      place: 'side',
      fromId: 'art_doc',
      toId: 'art_board',
      focusNodeId: 'opt-1',
    })
    expect(sideTabs()).toEqual(['plan', 'threads', 'artifacts', 'artifact:art_board'])
    expect(side.activeOf(side.focusedGroup(state.side))).toBe('artifact:art_board')
    expect(artifactNavigation('side', 'art_board')).toEqual({
      back: { id: 'art_doc' },
      forward: null,
    })

    // Back returns to art_doc in the same slot.
    expect(stepArtifactNavigation('side', 'art_board', -1)).toBe(true)
    expect(sideTabs()).toEqual(['plan', 'threads', 'artifacts', 'artifact:art_doc'])
    expect(artifactNavigation('side', 'art_doc')).toEqual({
      back: null,
      forward: { id: 'art_board', focusNodeId: 'opt-1' },
    })

    // Forward returns to art_board with focus restored.
    expect(stepArtifactNavigation('side', 'art_doc', 1)).toBe(true)
    expect(sideTabs()).toEqual(['plan', 'threads', 'artifacts', 'artifact:art_board'])
    expect(artifactFocusTarget.value).toMatchObject({ artifactId: 'art_board', nodeId: 'opt-1' })

    // Closing the tab discards its history.
    closePane('artifact:art_board')
    expect(artifactNavigation('side', 'art_board')).toEqual({ back: null, forward: null })
  })

  test('carries navigation history across fullScreenPane and fullToSide transitions', async () => {
    state.artifacts = [
      makeSummary('art_doc', 'document', '产品方案'),
      makeSummary('art_board', 'whiteboard', '交互画板'),
    ]
    state.artifactsLoaded = true

    await openView({ kind: 'artifact', id: 'art_doc' })
    navigateArtifactInPlace({ place: 'side', fromId: 'art_doc', toId: 'art_board' })
    fullScreenPane('artifact:art_board')

    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_board' })
    expect(artifactNavigation('full', 'art_board')).toEqual({
      back: { id: 'art_doc' },
      forward: null,
    })

    fullToSide()
    expect(state.full).toBeNull()
    expect(sideTabs()).toContain('artifact:art_board')
    expect(artifactNavigation('side', 'art_board')).toEqual({
      back: { id: 'art_doc' },
      forward: null,
    })
  })

  test('skips deleted artifacts when stepping navigation history', async () => {
    state.artifacts = [
      makeSummary('art_doc', 'document', '产品方案'),
      makeSummary('art_board', 'whiteboard', '交互画板'),
    ]
    state.artifactsLoaded = true

    await openView({ kind: 'artifact', id: 'art_doc' }, { from: 'thr_1' })
    navigateArtifactInPlace({ place: 'full', fromId: 'art_doc', toId: 'art_board' })

    // Simulate art_doc being deleted.
    state.artifacts = [makeSummary('art_board', 'whiteboard', '交互画板')]
    expect(stepArtifactNavigation('full', 'art_board', -1)).toBe(false)
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_board' })
    expect(artifactNavigation('full', 'art_board')).toEqual({ back: null, forward: null })
  })

  test('preserves both tabs histories when side navigation activates an already-open artifact tab', async () => {
    state.artifacts = [
      makeSummary('art_a0', 'document', '起点 A'),
      makeSummary('art_a', 'document', '文档 A'),
      makeSummary('art_b0', 'document', '起点 B'),
      makeSummary('art_b', 'whiteboard', '画板 B'),
    ]
    state.artifactsLoaded = true

    // Group 1 opens art_a0 -> art_a (history: [art_a0, art_a]).
    await openView({ kind: 'artifact', id: 'art_a0' })
    navigateArtifactInPlace({ place: 'side', fromId: 'art_a0', toId: 'art_a' })

    // Split opens art_b0 -> art_b in a second group (history: [art_b0, art_b]).
    setSide(side.open(state.side, side.artifactKey('art_b0'), { beside: 'horizontal' }))
    navigateArtifactInPlace({ place: 'side', fromId: 'art_b0', toId: 'art_b' })

    // Focus back on art_a and click a ref card targeting already-open art_b.
    setSide(side.activate(state.side, side.artifactKey('art_a')))
    navigateArtifactInPlace({
      place: 'side',
      fromId: 'art_a',
      toId: 'art_b',
      focusNodeId: 'grp-1',
    })

    // Both tabs remain open, art_b is activated, focus is requested, and neither history is clobbered.
    expect(sideTabs()).toContain('artifact:art_a')
    expect(sideTabs()).toContain('artifact:art_b')
    expect(side.activeOf(side.focusedGroup(state.side))).toBe('artifact:art_b')
    expect(artifactFocusTarget.value).toMatchObject({ artifactId: 'art_b', nodeId: 'grp-1' })
    expect(artifactNavigation('side', 'art_a')).toEqual({ back: { id: 'art_a0' }, forward: null })
    expect(artifactNavigation('side', 'art_b')).toEqual({ back: { id: 'art_b0' }, forward: null })

    // Now manually open art_a0 in a split while art_a still has Back -> art_a0, then step Back.
    setSide(side.open(state.side, side.artifactKey('art_a0'), { beside: 'horizontal' }))
    // Even if art_a0 is already the active tab in the focused split group, stepping Back from art_a
    // keeps art_a's own history intact and does not overwrite art_a0's history.
    expect(stepArtifactNavigation('side', 'art_a', -1)).toBe(true)
    expect(side.activeOf(side.focusedGroup(state.side))).toBe('artifact:art_a0')
    expect(artifactNavigation('side', 'art_a')).toEqual({ back: { id: 'art_a0' }, forward: null })
    expect(artifactNavigation('side', 'art_a0')).toEqual({ back: null, forward: null })
  })

  test('allows stepping back from an artifact missing from state.artifacts and prunes it afterward', async () => {
    state.artifacts = [
      makeSummary('art_root', 'document', '总览文档'),
      makeSummary('art_doc', 'document', '方案文档'),
    ]
    state.artifactsLoaded = true

    // Full-screen: art_root -> art_doc -> art_unsynced (not yet in state.artifacts or broken link).
    await openView({ kind: 'artifact', id: 'art_root' }, { from: 'thr_1' })
    navigateArtifactInPlace({ place: 'full', fromId: 'art_root', toId: 'art_doc' })
    navigateArtifactInPlace({ place: 'full', fromId: 'art_doc', toId: 'art_unsynced' })

    expect(stepArtifactNavigation('full', 'art_unsynced', -1)).toBe(true)
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_doc' })
    // Earlier valid entry remains reachable; invalid entry is pruned from Forward.
    expect(artifactNavigation('full', 'art_doc')).toEqual({
      back: { id: 'art_root' },
      forward: null,
    })

    // Side dock: art_doc -> art_missing (only 2 entries) -> Back returns to art_doc and clears stale history.
    await openView({ kind: 'artifact', id: 'art_doc' })
    navigateArtifactInPlace({ place: 'side', fromId: 'art_doc', toId: 'art_missing' })
    expect(stepArtifactNavigation('side', 'art_missing', -1)).toBe(true)
    expect(side.activeOf(side.focusedGroup(state.side))).toBe('artifact:art_doc')
    expect(artifactNavigation('side', 'art_doc')).toEqual({ back: null, forward: null })
  })
})

describe('ArtifactEditor ref card navigation and Header Back/Forward UI', () => {
  test('switches in place from RefBlockView, updates header Back/Forward buttons, and preserves pending edits & ask answers', async () => {
    const docSummary = makeSummary('art_doc', 'document', '产品与技术方案')
    const boardSummary = makeSummary('art_board', 'whiteboard', '交互设计稿')
    const dashSummary = makeSummary('art_dash', 'dashboard', '复盘看板')
    state.artifacts = [docSummary, boardSummary, dashSummary]
    state.artifactsLoaded = true

    const docView = makeArtifactView(
      'art_doc',
      'document',
      '产品与技术方案',
      '```ref\n{"artifactId":"art_board","groupId":"grp-nav","label":"交互设计稿"}\n```',
    )
    const boardView = makeArtifactView(
      'art_board',
      'whiteboard',
      '交互设计稿',
      JSON.stringify({
        version: 1,
        nodes: [
          {
            id: 'grp-nav',
            type: 'group',
            position: { x: 0, y: 0 },
            size: { width: 400, height: 240 },
            data: { label: '导航方案对比', round: 1, pick: 'one', confirmed: false },
          },
        ],
        edges: [],
      }),
    )
    const dashView = makeArtifactView(
      'art_dash',
      'dashboard',
      '复盘看板',
      JSON.stringify({ version: 1, data: {}, dashboard: { version: 1, widgets: [] } }),
    )

    const docY = new Y.Doc()
    seedAskDoc(docY, {
      id: 'ask-1',
      question: '入口放哪里？',
      description: '',
      pick: 'one',
      options: [
        { id: 'sidebar', label: '侧栏顶部', recommended: true },
        { id: 'dock', label: '右侧 Dock' },
      ],
      allowNote: true,
      selected: ['dock'],
      note: '更顺手',
      confirmed: false,
    })
    sharedByArtifact.set('art_doc', { doc: docY, ready: ref(true) })

    vi.spyOn(api, 'getArtifact').mockImplementation(async (id) => {
      if (id === 'art_doc') {
        return {
          artifact: docView,
          versions: [],
          comments: [],
          askBaseline: {
            version: 1,
            asks: [{ id: 'ask-1', selected: [], note: '', confirmed: false }],
          },
        }
      }
      if (id === 'art_board') {
        return { artifact: boardView, versions: [], comments: [] }
      }
      return { artifact: dashView, versions: [], comments: [] }
    })

    await openView({ kind: 'artifact', id: 'art_doc' }, { from: 'thr_1' })

    const Host = defineComponent({
      setup() {
        return () =>
          state.full?.view.kind === 'artifact'
            ? h(ArtifactEditor, { id: state.full.view.id, place: 'full' })
            : null
      },
    })

    const wrapper = mount(Host)
    await flushPromises()

    // Initially no history: Back/Forward buttons stay hidden to keep header uncluttered.
    expect(wrapper.find('[data-artifact-nav="back"]').exists()).toBe(false)
    expect(wrapper.find('[data-artifact-nav="forward"]').exists()).toBe(false)

    // Make an unsent text edit on art_doc so pending feedback is tracked.
    docY.transact(() => {
      docY.getMap('document').set('draft-note', 'unsaved-edit')
    }, 'local-user')
    await flushPromises()
    expect(getPendingArtifactFeedback('art_doc')?.pendingChanges).toBe(1)

    // Click the RefBlockView action button ("定位查看") inside the document.
    await wrapper.get('.doc-ref-action').trigger('click')
    await flushPromises()

    // Replaced in-place inside full view; did NOT open a side tab.
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_board' })
    expect(state.full?.threadId).toBe('thr_1')
    expect(sideTabs()).not.toContain('artifact:art_board')
    expect(artifactFocusTarget.value).toMatchObject({
      artifactId: 'art_board',
      nodeId: 'grp-nav',
    })
    expect(wrapper.find('.mock-whiteboard-body').exists()).toBe(true)

    // Leaving art_doc kept its pending edits paused in feedback-pending store.
    expect(getPendingArtifactFeedback('art_doc')).toEqual({
      pendingChanges: 1,
      pendingNotes: 0,
      paused: true,
    })

    // Header now shows Back (enabled) and Forward (disabled) with descriptive aria-labels.
    const backBtn = wrapper.get('[data-artifact-nav="back"]')
    const forwardBtn = wrapper.get('[data-artifact-nav="forward"]')
    expect(backBtn.attributes('disabled')).toBeUndefined()
    expect(backBtn.attributes('aria-label')).toBe('返回「产品与技术方案」')
    expect(forwardBtn.attributes('disabled')).toBeDefined()

    // Click Back in header -> returns to art_doc, restores pending feedback & ask submit bar.
    await backBtn.trigger('click')
    await flushPromises()

    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_doc' })
    expect(wrapper.find('.mock-document-body').exists()).toBe(true)
    expect(wrapper.get('[data-artifact-nav="back"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-artifact-nav="forward"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-artifact-nav="forward"]').attributes('aria-label')).toBe(
      '前进到「交互设计稿」',
    )
    // Inline ask bar is still shown because unsent answers survived the round-trip.
    expect(wrapper.find('.artifact-feedback-bar').text()).toContain('提交回答')
    expect(getPendingArtifactFeedback('art_doc')?.pendingChanges).toBe(1)

    // Click Forward -> returns to art_board and re-triggers target node focus.
    await wrapper.get('[data-artifact-nav="forward"]').trigger('click')
    await flushPromises()
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_board' })
    expect(artifactFocusTarget.value).toMatchObject({
      artifactId: 'art_board',
      nodeId: 'grp-nav',
    })

    // Go back, then trigger inline artifact link -> switches to dashboard and clears forward.
    await wrapper.get('[data-artifact-nav="back"]').trigger('click')
    await flushPromises()
    await wrapper.get('.mock-inline-link').trigger('click')
    await flushPromises()
    expect(state.full?.view).toEqual({ kind: 'artifact', id: 'art_dash' })
    expect(wrapper.find('.mock-dashboard-body').exists()).toBe(true)
    expect(wrapper.get('[data-artifact-nav="back"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-artifact-nav="forward"]').attributes('disabled')).toBeDefined()

    wrapper.unmount()
  })
})
