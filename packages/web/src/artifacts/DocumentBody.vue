<script setup lang="ts">
import type {
  ArtifactCommentAnchor,
  ArtifactCommentThreadView,
  DocumentAskAnswerBaseline,
} from '@ariadne/core'
import { DouEditor, editorKit, type EditorDocument, type PartialBlock } from '@dou-ui/editor'
import { collaboration } from '@dou-ui/editor/collaboration'
import { whiteboardBlock } from '@dou-ui/editor/flow'
import { markdownToBlocks } from '@dou-ui/editor/markdown'
import '@dou-ui/editor/style.css'
import '@dou-ui/flow/style.css'
import { useReducedMotion, type CommentAction, type CommentThread } from 'dou-ui'
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  shallowRef,
  useTemplateRef,
  watch,
} from 'vue'
import { anchoredIds, anchorQuotes, commentTarget } from './comment-anchors'
import { commentAuthor, toCommentThread } from './comment-threads'
import { api } from '../api'
import { t } from '../i18n'
import { askDocumentBlock, normalizeAskProps, type AskBlockProps } from './ask-block'
import {
  applyRecommendedDocumentAsks,
  readSharedDocumentAsks,
  snapshotDocumentAsks,
} from './document-asks'
import { documentCodeLanguages, highlightDocumentCode } from './document-code'
import { documentLocks } from './document-locks'
import {
  computeDocumentTocLayout,
  extractDocumentHeadings,
  isTocCollidingWithTables,
  pickActiveHeadingId,
  sameDocumentHeadings,
  type DocumentTocHeading,
  type DocumentTocLayout,
  type HeadingPosition,
  type TableCollisionBox,
} from './document-toc'
import DocumentToc from './DocumentToc.vue'
import { referencedArtifactId, type ArtifactNavigationTarget } from './artifact-history'
import { pendingDocumentBlock } from './pending-block'
import { createRefDocumentBlock } from './ref-block'
import type { SharedArtifact } from './shared'

/**
 * A Markdown document in the block editor. The newest version is edited together through
 * `shared`, where the comments' marks travel with the text; an older one is shown
 * read-only from `content`, its comments marked again on the text they quote.
 */
const props = defineProps<{
  content: string
  projectId: string
  readonly: boolean
  label?: string
  /** `undefined` turns comments off, as for an older version. */
  comments?: ArtifactCommentThreadView[]
  shared?: SharedArtifact
}>()
const emit = defineEmits<{
  comment: [action: CommentAction, target: string | ArtifactCommentAnchor | null, quote?: string]
  asks: [asks: AskBlockProps[]]
  navigate: [target: ArtifactNavigationTarget]
}>()
const active = defineModel<string | null>('activeComment', { default: null })

const me = commentAuthor('user')
// The same blocks as the server's (`documentExtensions`): a block one side does not know
// is lost when the other writes the document. Images on a whiteboard are uploaded as files.
const upload = async (file: File) => (await api.uploadFile(props.projectId, file)).file.url
const blocks = [
  editorKit({
    codeBlock: {
      highlight: highlightDocumentCode,
      languages: documentCodeLanguages,
    },
  }),
  whiteboardBlock({ upload, commentable: true }),
  pendingDocumentBlock,
  askDocumentBlock,
  createRefDocumentBlock({
    onOpen: (target) => emit('navigate', target),
  }),
]
const extensions = props.shared
  ? [
      ...blocks,
      collaboration({
        map: props.shared.doc.getMap('document'),
        awareness: props.shared.awareness,
        user: { name: me.name },
      }),
      documentLocks(props.shared.awareness, () => t('artifact.aiEditing')),
    ]
  : blocks
const doc = shallowRef<EditorDocument<PartialBlock>>({ version: 1, blocks: [] })
// Bumped to remount the editor when content is replaced from outside.
const generation = ref(0)
const shell = useTemplateRef<HTMLElement>('shell')
const scrollArea = useTemplateRef<HTMLElement>('scrollArea')
const editor = useTemplateRef<InstanceType<typeof DouEditor>>('editor')
const tocRef = useTemplateRef<InstanceType<typeof DocumentToc>>('tocRef')
const reducedMotion = useReducedMotion()

const headings = shallowRef<DocumentTocHeading[]>([])
const activeHeadingId = ref<string | null>(null)
const tocLayout = shallowRef<DocumentTocLayout>({ mode: 'hidden', width: 0, dockedRight: 0 })
const tocCollided = ref(false)

let measureFrame = 0
let pinnedHeadingId: string | null = null
let pinnedSettleTimer: ReturnType<typeof setTimeout> | null = null
let pinnedMaxTimer: ReturnType<typeof setTimeout> | null = null
let layoutObserver: ResizeObserver | null = null
let observedSurface: HTMLElement | null = null

const escapeSelector = (id: string) =>
  typeof CSS !== 'undefined' && typeof CSS.escape === 'function'
    ? CSS.escape(id)
    : id.replace(/["\\]/g, '\\$&')

function clearPinnedHeading() {
  pinnedHeadingId = null
  if (pinnedSettleTimer) {
    clearTimeout(pinnedSettleTimer)
    pinnedSettleTimer = null
  }
  if (pinnedMaxTimer) {
    clearTimeout(pinnedMaxTimer)
    pinnedMaxTimer = null
  }
}

function pinHeading(id: string) {
  clearPinnedHeading()
  pinnedHeadingId = id
  activeHeadingId.value = id
  pinnedSettleTimer = setTimeout(() => {
    clearPinnedHeading()
    scheduleMeasure()
  }, 160)
  pinnedMaxTimer = setTimeout(() => {
    clearPinnedHeading()
    scheduleMeasure()
  }, 900)
}

function syncHeadings(sourceBlocks?: readonly PartialBlock[]) {
  const next = extractDocumentHeadings(sourceBlocks ?? editor.value?.getBlocks() ?? [])
  if (!sameDocumentHeadings(headings.value, next)) {
    headings.value = next
    if (activeHeadingId.value && !next.some((item) => item.id === activeHeadingId.value)) {
      activeHeadingId.value = next[0]?.id ?? null
    }
  }
  scheduleMeasure()
}

function attachSurfaceObserver() {
  if (!layoutObserver) return
  const nextSurface = scrollArea.value?.querySelector<HTMLElement>('.dou-editor__surface') ?? null
  if (nextSurface === observedSurface) return
  if (observedSurface) layoutObserver.unobserve(observedSurface)
  observedSurface = nextSurface
  if (observedSurface) layoutObserver.observe(observedSurface)
}

function measureToc() {
  measureFrame = 0
  const shellEl = shell.value
  const scrollEl = scrollArea.value
  if (!shellEl || !scrollEl) return

  attachSurfaceObserver()

  const shellRect = shellEl.getBoundingClientRect()
  const surface = observedSurface ?? scrollEl.querySelector<HTMLElement>('.dou-editor__surface')
  const surfaceStyle = surface ? getComputedStyle(surface) : null
  const readingLeft = surface
    ? surface.getBoundingClientRect().left +
      (Number.parseFloat(surfaceStyle?.paddingLeft || surfaceStyle?.paddingInlineStart || '') ||
        0) -
      shellRect.left
    : 0

  const nextLayout = computeDocumentTocLayout(readingLeft)
  if (
    nextLayout.mode !== tocLayout.value.mode ||
    nextLayout.width !== tocLayout.value.width ||
    nextLayout.dockedRight !== tocLayout.value.dockedRight
  ) {
    tocLayout.value = nextLayout
  }

  if (!headings.value.length || nextLayout.mode === 'hidden') {
    tocCollided.value = false
    return
  }

  const scrollRect = scrollEl.getBoundingClientRect()

  if (!pinnedHeadingId) {
    const positions: HeadingPosition[] = []
    for (const heading of headings.value) {
      const blockEl = scrollEl.querySelector<HTMLElement>(
        `[data-block-id="${escapeSelector(heading.id)}"]`,
      )
      if (!blockEl) continue
      const targetEl =
        blockEl.querySelector<HTMLElement>(':scope > .dou-editor__content') ?? blockEl
      const rect = targetEl.getBoundingClientRect()
      if (rect.height <= 0) continue
      positions.push({ id: heading.id, relativeTop: rect.top - scrollRect.top })
    }
    activeHeadingId.value = pickActiveHeadingId({
      headings: positions,
      scrollTop: scrollEl.scrollTop,
      clientHeight: scrollEl.clientHeight,
      scrollHeight: scrollEl.scrollHeight,
    })
  }

  if (nextLayout.mode !== 'docked') {
    tocCollided.value = false
    return
  }

  const tables: TableCollisionBox[] = []
  for (const frame of scrollEl.querySelectorAll<HTMLElement>('.dou-editor__content--table')) {
    const tableEl = frame.querySelector<HTMLElement>(':scope > table.dou-editor-table, table')
    if (!tableEl) continue
    const frameRect = frame.getBoundingClientRect()
    const tableRect = tableEl.getBoundingClientRect()
    tables.push({
      visibleLeft: Math.max(frameRect.left, tableRect.left),
      top: frameRect.top,
      bottom: frameRect.bottom,
      height: frameRect.height,
    })
  }

  tocCollided.value = isTocCollidingWithTables({
    dockedTocRight: shellRect.left + nextLayout.dockedRight,
    viewport: { top: scrollRect.top, bottom: scrollRect.bottom },
    tocVertical: tocRef.value?.verticalRange() ?? null,
    tables,
    currentlyCollided: tocCollided.value,
  })
}

function scheduleMeasure() {
  if (measureFrame) return
  measureFrame = requestAnimationFrame(measureToc)
}

function onAnyScroll(event: Event) {
  if (event.target === scrollArea.value && pinnedHeadingId) {
    if (pinnedSettleTimer) clearTimeout(pinnedSettleTimer)
    pinnedSettleTimer = setTimeout(() => {
      clearPinnedHeading()
      scheduleMeasure()
    }, 140)
  }
  scheduleMeasure()
}

function onManualScrollInput() {
  if (pinnedHeadingId) clearPinnedHeading()
}

function onEditorChange(next: EditorDocument) {
  syncHeadings(next.blocks)
  void nextTick(scheduleMeasure)
}

function expandCollapsedAncestors(scrollEl: HTMLElement, blockEl: HTMLElement): boolean {
  const collapsedAncestors: HTMLElement[] = []
  let current = blockEl.parentElement
  while (current && scrollEl.contains(current)) {
    const ancestor = current.closest<HTMLElement>('.dou-editor__block.is-collapsed')
    if (!ancestor || !scrollEl.contains(ancestor)) break
    collapsedAncestors.push(ancestor)
    current = ancestor.parentElement
  }
  if (!collapsedAncestors.length) return false
  let expanded = false
  for (let i = collapsedAncestors.length - 1; i >= 0; i--) {
    const ancestor = collapsedAncestors[i]!
    const button = ancestor.querySelector<HTMLButtonElement>(
      ':scope > .dou-editor__content .dou-editor-toggle__button',
    )
    if (button && button.getAttribute('aria-expanded') !== 'true') {
      button.click()
      expanded = true
    }
  }
  return expanded
}

async function scrollToHeading(id: string) {
  const scrollEl = scrollArea.value
  if (!scrollEl) return
  const selector = `[data-block-id="${escapeSelector(id)}"]`
  let blockEl = scrollEl.querySelector<HTMLElement>(selector)
  if (!blockEl) return

  let targetEl = blockEl.querySelector<HTMLElement>(':scope > .dou-editor__content') ?? blockEl
  let targetRect = targetEl.getBoundingClientRect()

  if (targetRect.height <= 0 || blockEl.parentElement?.closest('.dou-editor__block.is-collapsed')) {
    const expanded = expandCollapsedAncestors(scrollEl, blockEl)
    if (expanded) {
      await nextTick()
      if (scrollArea.value !== scrollEl) return
      blockEl = scrollEl.querySelector<HTMLElement>(selector)
      if (!blockEl) return
      targetEl = blockEl.querySelector<HTMLElement>(':scope > .dou-editor__content') ?? blockEl
      targetRect = targetEl.getBoundingClientRect()
    }
    if (targetRect.height <= 0 && !props.readonly) {
      editor.value?.focusBlock(id, 'start')
      await nextTick()
      if (scrollArea.value !== scrollEl) return
      blockEl = scrollEl.querySelector<HTMLElement>(selector)
      if (!blockEl) return
      targetEl = blockEl.querySelector<HTMLElement>(':scope > .dou-editor__content') ?? blockEl
      targetRect = targetEl.getBoundingClientRect()
    }
  }

  if (targetRect.height <= 0) return

  const scrollRect = scrollEl.getBoundingClientRect()
  const targetTop = Math.max(0, scrollEl.scrollTop + (targetRect.top - scrollRect.top) - 24)
  pinHeading(id)
  scrollEl.scrollTo({
    top: targetTop,
    behavior: reducedMotion.value ? 'auto' : 'smooth',
  })
}

function onTocWheelPassthrough(event: WheelEvent) {
  const scrollEl = scrollArea.value
  if (!scrollEl || event.deltaY === 0) return
  event.preventDefault()
  clearPinnedHeading()
  const deltaPx =
    event.deltaMode === 1
      ? event.deltaY * 16
      : event.deltaMode === 2
        ? event.deltaY * scrollEl.clientHeight
        : event.deltaY
  scrollEl.scrollTop += deltaPx
}

onMounted(() => {
  const scrollEl = scrollArea.value
  const shellEl = shell.value
  if (scrollEl) {
    scrollEl.addEventListener('scroll', onAnyScroll, { capture: true, passive: true })
    scrollEl.addEventListener('wheel', onManualScrollInput, { passive: true })
    scrollEl.addEventListener('touchstart', onManualScrollInput, { passive: true })
  }
  if (typeof ResizeObserver !== 'undefined') {
    layoutObserver = new ResizeObserver(scheduleMeasure)
    if (shellEl) layoutObserver.observe(shellEl)
    attachSurfaceObserver()
  }
  scheduleMeasure()
})

onBeforeUnmount(() => {
  clearPinnedHeading()
  if (measureFrame) {
    cancelAnimationFrame(measureFrame)
    measureFrame = 0
  }
  const scrollEl = scrollArea.value
  if (scrollEl) {
    scrollEl.removeEventListener('scroll', onAnyScroll, true)
    scrollEl.removeEventListener('wheel', onManualScrollInput)
    scrollEl.removeEventListener('touchstart', onManualScrollInput)
  }
  layoutObserver?.disconnect()
  layoutObserver = null
  observedSurface = null
})

/** The threads as the editor shows them; changes here show at once, before the server. */
const threads = ref<CommentThread[]>([])
watch(
  () => props.comments,
  (comments) => (threads.value = (comments ?? []).map(toCommentThread)),
  { immediate: true },
)

function load() {
  // Edited together, the document comes from the room, marks and all.
  if (props.shared) return
  const read = markdownToBlocks(props.content, extensions) as PartialBlock[]
  doc.value = { version: 1, blocks: anchorQuotes(read, props.comments ?? []).blocks }
  generation.value++
}
watch(() => props.content, load, { immediate: true })

const ready = computed(() => !props.shared || props.shared.ready.value)

watch(
  [editor, ready, generation],
  ([instance, isReady]) => {
    if (!instance || !isReady) {
      headings.value = []
      activeHeadingId.value = null
      tocCollided.value = false
      return
    }
    syncHeadings(instance.getBlocks())
  },
  { immediate: true, flush: 'post' },
)

function readCurrentAsks(): AskBlockProps[] {
  if (props.shared) {
    return readSharedDocumentAsks(props.shared.doc.getMap('document')).map((entry) => entry.ask)
  }
  const asks: AskBlockProps[] = []
  const collect = (list: readonly PartialBlock[]) => {
    for (const block of list) {
      if (block.type === 'ask' && block.props) {
        const normalized = normalizeAskProps(block.props as Record<string, unknown>)
        if (normalized) asks.push(normalized)
      }
      if (Array.isArray(block.children) && block.children.length) collect(block.children)
    }
  }
  collect(doc.value.blocks)
  return asks
}

function emitAsks() {
  emit('asks', readCurrentAsks())
}

function currentAskSnapshot(): DocumentAskAnswerBaseline[] {
  return snapshotDocumentAsks(readCurrentAsks())
}

if (props.shared) {
  const root = props.shared.doc.getMap('document')
  const onDocChange = () => {
    if (ready.value) emitAsks()
  }
  root.observeDeep(onDocChange)
  watch(ready, (now) => now && emitAsks(), { immediate: true })
  onBeforeUnmount(() => root.unobserveDeep(onDocChange))
} else {
  watch(() => doc.value.blocks, emitAsks, { immediate: true })
}

function applyRecommendedPicks(): number {
  if (!props.shared) return 0
  return applyRecommendedDocumentAsks(props.shared.doc)
}

// A thread started elsewhere (an agent's) is marked on the text it quotes.
watch(
  () => props.comments,
  (comments) => {
    if (props.shared || !comments) return
    const marked = anchoredIds(doc.value.blocks)
    const missing = comments.some((c) => !c.anchor?.block && !marked.has(c.id) && c.quote)
    if (missing && anchorQuotes(doc.value.blocks, comments).found.size > marked.size) {
      load()
    }
  },
)

function onComment(action: CommentAction) {
  if (action.type !== 'create') return emit('comment', action, '')
  const anchor = editor.value?.getCommentAnchors()[action.thread.id]
  emit('comment', action, commentTarget(anchor), anchor?.text)
}

function onLinkOpen(opening: { link: string; preventDefault: () => void }) {
  const artifactId = referencedArtifactId(opening.link, props.projectId)
  if (!artifactId) return
  opening.preventDefault()
  emit('navigate', { artifactId })
}

/** The commented text as it reads now, by thread; saved with an edit so threads follow it. */
function quotes(): Record<string, string> {
  const anchors = editor.value?.getCommentAnchors() ?? {}
  return Object.fromEntries(
    Object.entries(anchors).flatMap(([id, anchor]) =>
      anchor && !anchor.type ? [[id, anchor.text]] : [],
    ),
  )
}

/** Ids of the threads whose text is in the document. */
function anchored(): Set<string> {
  return new Set(
    Object.entries(editor.value?.getCommentAnchors() ?? {}).flatMap(([id, anchor]) =>
      anchor ? [id] : [],
    ),
  )
}

function revealComment(id: string) {
  editor.value?.scrollToComment(id)
}

defineExpose({ quotes, anchored, revealComment, applyRecommendedPicks, currentAskSnapshot })
</script>

<template>
  <div ref="shell" class="document-shell">
    <div ref="scrollArea" class="document">
      <DouEditor
        v-if="ready"
        ref="editor"
        :key="generation"
        v-model="doc"
        v-model:active-comment="active"
        :readonly="readonly"
        :extensions="extensions"
        :aria-label="label"
        :comments="comments ? threads : undefined"
        :author="comments ? me : undefined"
        class="document-text"
        @change="onEditorChange"
        @update:comments="(next: CommentThread[]) => (threads = next)"
        @comment-action="onComment"
        @link-open="onLinkOpen"
      />
    </div>
    <DocumentToc
      ref="tocRef"
      :headings="headings"
      :active-id="activeHeadingId"
      :mode="tocLayout.mode"
      :width="tocLayout.width"
      :collided="tocCollided"
      @select="scrollToHeading"
      @wheel-passthrough="onTocWheelPassthrough"
    />
  </div>
</template>

<style scoped>
.document-shell {
  position: relative;
  height: 100%;
  overflow: clip;
}

.document {
  height: 100%;
  overflow-y: auto;
  container-type: inline-size;
}

.document-text {
  box-sizing: border-box;
  /* Room on the left for the block handle on wide panes while staying comfortable on narrow screens. */
  padding: clamp(16px, 3cqw, 32px) clamp(16px, 5cqw, 48px) 96px;
  --dou-editor-line-height: 1.8;
  --dou-editor-reading-width: 48em;
  --dou-editor-block-gap: 10px;
  --dou-editor-list-gap: 4px;
  --dou-editor-table-col-max-width: calc(0.75 * var(--dou-editor-reading-width));
}

.document-text :deep([data-ai-editing] > .dou-editor__content) {
  background-color: color-mix(in srgb, var(--dou-color-primary) 9%, transparent);
}
</style>
