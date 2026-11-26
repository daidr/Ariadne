<script setup lang="ts">
import type { ArtifactVersionView, ArtifactView, GalleryArtifactSummary } from '@ariadne/core'
import {
  AppWindowIcon,
  ArrowUpRightIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CloseIcon,
  FolderIcon,
  MessageDotsIcon,
} from '@dou-ui/icons'
import {
  DouAlert,
  DouAvatar,
  DouButton,
  DouDivider,
  DouDrawer,
  DouLink,
  DouMarkdown,
  DouSpinner,
  DouTag,
  DouText,
} from 'dou-ui'
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { openFilePreview } from '../files/preview'
import { locale, t } from '../i18n'
import { innerLink, sanitizeUrl } from '../markdown/links'
import { useServerNow } from '../server-clock'
import { artifactKey, threadKey } from '../side/layout'
import { openProjectArtifact, openProjectThread } from '../store'
import ArtifactThumb from './ArtifactThumb.vue'
import { formatGalleryTime, formatGalleryUpdater } from './gallery-groups'
import { kindIcon } from './kinds'

const props = defineProps<{
  item: GalleryArtifactSummary | null
  open: boolean
  drawerMode: 'bottom' | 'right' | 'docked'
  hasPrev: boolean
  hasNext: boolean
}>()

const emit = defineEmits<{
  close: []
  openInProject: [item: GalleryArtifactSummary]
  openProject: [projectId: string]
  prev: []
  next: []
}>()

const now = useServerNow()
const detail = ref<ArtifactView | null>(null)
const versions = ref<ArtifactVersionView[]>([])
const loading = ref(false)
const loadError = ref<string | null>(null)

let controller: AbortController | null = null
let serial = 0

async function loadDetail(item: GalleryArtifactSummary) {
  controller?.abort()
  controller = new AbortController()
  const current = ++serial
  loading.value = true
  loadError.value = null
  detail.value = null
  versions.value = []
  try {
    const res = await api.getArtifact(item.artifact.id, {
      versions: true,
      versionPageSize: 6,
      lightweight: item.artifact.kind === 'whiteboard',
      signal: controller.signal,
    })
    if (current !== serial || controller.signal.aborted) return
    detail.value = res.artifact
    versions.value = res.versions
  } catch (err) {
    if (current !== serial || controller.signal.aborted) return
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (current === serial) loading.value = false
  }
}

watch(
  () => [props.open, props.item?.artifact.id, props.item?.artifact.version] as const,
  ([isOpen, id]) => {
    if (!isOpen || !id || !props.item) {
      controller?.abort()
      loading.value = false
      return
    }
    void loadDetail(props.item)
  },
  { immediate: true },
)

const artifact = computed(() => props.item?.artifact ?? null)
const preview = computed(() => artifact.value?.preview ?? null)
const kindBadgeIcon = computed(() => {
  if (!artifact.value) return kindIcon.document
  return props.item?.hasPrototype ? AppWindowIcon : kindIcon[artifact.value.kind]
})
const kindClass = computed(() => {
  if (!artifact.value) return 'kind-document'
  return props.item?.hasPrototype ? 'kind-prototype' : `kind-${artifact.value.kind}`
})
const kindLabel = computed(() => {
  if (!artifact.value) return ''
  return props.item?.hasPrototype
    ? t('gallery.kind.prototype')
    : t(`artifact.kind.${artifact.value.kind}`)
})

const updatedLabel = computed(() =>
  artifact.value ? formatGalleryTime(artifact.value.updatedAt, now.value, locale.value) : '',
)
const updaterLabel = computed(() =>
  props.item
    ? formatGalleryUpdater(props.item, {
        coordinator: t('gallery.meta.coordinator'),
        user: t('gallery.meta.user'),
      })
    : '',
)

/** Render a bounded read-only Markdown excerpt inside quick view for crisp scrolling. */
const documentMarkdown = computed(() => {
  if (artifact.value?.kind !== 'document') return ''
  const content =
    detail.value?.content ?? (preview.value?.kind === 'document' ? preview.value.lead : '')
  const trimmed = content
    .replace(/^```(?:ask|ref|dou-whiteboard)\r?\n[\s\S]*?^```[ \t]*$/gm, '')
    .trim()
  if (trimmed.length <= 4500) return trimmed
  return `${trimmed.slice(0, 4500)}\n\n…`
})

const whiteboardHighlights = computed(() => {
  if (!preview.value || preview.value.kind !== 'whiteboard') return []
  return preview.value.texts
})

const dashboardTitles = computed(() => {
  if (!preview.value || preview.value.kind !== 'dashboard') return []
  return preview.value.titles
})

function formatVersionTime(at: number) {
  return formatGalleryTime(at, now.value, locale.value)
}

function innerLinkHref(href: string | undefined): string | undefined {
  const target = innerLink(href)
  const projectId = artifact.value?.projectId
  if (!target || !projectId) return undefined
  if (target.kind === 'artifact') {
    return `/projects/${encodeURIComponent(projectId)}?pane=side&tab=${encodeURIComponent(artifactKey(target.id))}`
  }
  if (target.kind === 'thread') {
    return `/projects/${encodeURIComponent(projectId)}?pane=side&tab=${encodeURIComponent(threadKey(target.id))}`
  }
  return undefined
}

function openMarkdownInnerLink(href: string, event: MouseEvent) {
  const target = innerLink(href)
  if (!target) return
  if ((event.metaKey || event.ctrlKey || event.shiftKey) && innerLinkHref(href)) {
    return
  }
  event.preventDefault()
  const projectId = artifact.value?.projectId
  if (target.kind === 'artifact' && projectId) {
    void openProjectArtifact(projectId, target.id)
  } else if (target.kind === 'thread' && projectId) {
    void openProjectThread(projectId, target.id)
  } else if (target.kind === 'file') {
    openFilePreview({ id: target.id })
  }
}
</script>

<template>
  <!-- Narrow (bottom sheet) or medium (right slide-over drawer) -->
  <DouDrawer
    v-if="drawerMode !== 'docked'"
    :open="open && Boolean(item)"
    :placement="drawerMode"
    :swipeable="drawerMode === 'bottom'"
    :snap-points="drawerMode === 'bottom' ? ['72%', '92%'] : undefined"
    :size="drawerMode === 'right' ? 384 : undefined"
    :title="artifact?.title ?? t('gallery.quickView')"
    @update:open="(value) => !value && emit('close')"
  >
    <template v-if="item && artifact">
      <div class="quick-sheet-body">
        <div class="quick-badges">
          <DouTag size="sm" variant="soft" :tone="item.hasPrototype ? 'primary' : 'neutral'" round>
            {{ kindLabel }}
          </DouTag>
          <DouTag size="sm" variant="soft" tone="neutral" round>
            {{ t('gallery.meta.version', { version: artifact.version }) }}
          </DouTag>
          <DouTag v-if="item.pendingActions > 0" size="sm" variant="soft" tone="warning" round>
            {{ t('gallery.status.needsMe') }} ({{ item.pendingActions }})
          </DouTag>
          <DouTag v-if="item.pendingFeedback > 0" size="sm" variant="soft" tone="primary" round>
            {{ t('gallery.status.feedbackPending') }}
          </DouTag>
        </div>

        <div class="quick-visual">
          <ArtifactThumb :artifact="artifact" />
        </div>

        <div class="quick-meta-grid">
          <div class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.project') }}</DouText>
            <DouText size="sm" weight="medium" truncate>{{ item.projectName }}</DouText>
          </div>
          <div v-if="item.nodeTitle" class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.planNode') }}</DouText>
            <DouText size="sm" truncate>{{ item.nodeTitle }}</DouText>
          </div>
          <div class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.updatedBy') }}</DouText>
            <DouText size="sm" truncate>{{ updaterLabel }} · {{ updatedLabel }}</DouText>
          </div>
          <div v-if="artifact.openComments > 0" class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.comments') }}</DouText>
            <DouText size="sm">{{
              t('gallery.meta.comments', { count: artifact.openComments })
            }}</DouText>
          </div>
        </div>

        <DouDivider />

        <div v-if="loading" class="quick-loading" role="status">
          <DouSpinner size="sm" />
        </div>
        <DouAlert v-else-if="loadError" tone="danger" :title="t('gallery.quickView.loadError')" />
        <div v-else-if="artifact.kind === 'document' && documentMarkdown" class="quick-markdown">
          <DouMarkdown :content="documentMarkdown" link-target="_blank" :sanitize-url="sanitizeUrl">
            <template #link="{ href, title, external, children }">
              <DouLink
                v-if="innerLink(href)"
                :href="innerLinkHref(href)"
                variant="soft"
                @click="openMarkdownInnerLink(href!, $event)"
              >
                <component :is="children" />
              </DouLink>
              <a
                v-else-if="href"
                :href="href"
                :title="title"
                :target="external ? '_blank' : undefined"
                :rel="external ? 'noopener noreferrer' : undefined"
              >
                <component :is="children" />
              </a>
              <span v-else class="dou-markdown__link">
                <component :is="children" />
              </span>
            </template>
          </DouMarkdown>
        </div>
        <div v-else-if="preview?.kind === 'whiteboard'" class="quick-list-section">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.quickView.summary')
          }}</DouText>
          <DouText size="sm" variant="tertiary">
            {{ t('gallery.meta.items', { count: preview.items }) }} ·
            {{ t('gallery.meta.connectors', { count: preview.connectors }) }}
            <template v-if="(preview.prototypeCount ?? 0) > 0">
              · {{ t('gallery.meta.prototypes', { count: preview.prototypeCount ?? 0 }) }}
            </template>
            <template v-if="(preview.optionCount ?? 0) > 0">
              · {{ t('gallery.meta.options', { count: preview.optionCount ?? 0 }) }}
            </template>
          </DouText>
          <ul v-if="whiteboardHighlights.length" class="quick-highlights">
            <li v-for="(line, i) in whiteboardHighlights" :key="i">
              <DouText size="sm">{{ line }}</DouText>
            </li>
          </ul>
        </div>
        <div v-else-if="preview?.kind === 'dashboard'" class="quick-list-section">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.quickView.summary')
          }}</DouText>
          <DouText size="sm" variant="tertiary">
            {{ t('gallery.meta.widgets', { count: preview.widgets }) }}
            <template v-if="preview.isLive"> · {{ t('artifact.dashboard.live') }}</template>
          </DouText>
          <ul v-if="dashboardTitles.length" class="quick-highlights">
            <li v-for="(line, i) in dashboardTitles" :key="i">
              <DouText size="sm">{{ line }}</DouText>
            </li>
          </ul>
        </div>
      </div>
    </template>

    <template #footer>
      <div v-if="item" class="quick-sheet-footer">
        <DouButton
          variant="outline"
          class="sheet-action-btn"
          @click="emit('openProject', item.artifact.projectId)"
        >
          <template #leading><FolderIcon /></template>
          {{ t('gallery.quickView.openProject') }}
        </DouButton>
        <DouButton tone="primary" class="sheet-action-btn" @click="emit('openInProject', item)">
          <template #trailing><ArrowUpRightIcon /></template>
          {{ t('gallery.quickView.openInProject') }}
        </DouButton>
      </div>
    </template>
  </DouDrawer>

  <!-- Desktop / medium: non-blocking docked quick-view aside -->
  <aside
    v-else-if="open && item && artifact"
    class="quick-pane"
    :aria-label="t('gallery.quickView')"
  >
    <div class="quick-pane-header">
      <div class="quick-pane-title-wrap">
        <DouAvatar shape="square" :size="28" class="kind-avatar" :class="kindClass">
          <component :is="kindBadgeIcon" />
        </DouAvatar>
        <div class="quick-pane-heading">
          <DouText weight="medium" truncate>{{ artifact.title }}</DouText>
          <DouText variant="tertiary" size="sm" truncate>
            {{ kindLabel }} · {{ t('gallery.meta.version', { version: artifact.version }) }}
          </DouText>
        </div>
      </div>
      <div class="quick-pane-nav">
        <DouButton
          size="sm"
          variant="ghost"
          :disabled="!hasPrev"
          :aria-label="t('gallery.quickView.prev')"
          :title="t('gallery.quickView.prev')"
          @click="emit('prev')"
        >
          <ChevronLeftIcon />
        </DouButton>
        <DouButton
          size="sm"
          variant="ghost"
          :disabled="!hasNext"
          :aria-label="t('gallery.quickView.next')"
          :title="t('gallery.quickView.next')"
          @click="emit('next')"
        >
          <ChevronRightIcon />
        </DouButton>
        <DouButton
          size="sm"
          variant="ghost"
          :aria-label="t('gallery.quickView.close')"
          :title="t('gallery.quickView.close')"
          @click="emit('close')"
        >
          <CloseIcon />
        </DouButton>
      </div>
    </div>

    <div class="quick-pane-actions">
      <DouButton
        size="sm"
        tone="primary"
        class="quick-primary-btn"
        @click="emit('openInProject', item)"
      >
        <template #trailing><ArrowUpRightIcon /></template>
        {{ t('gallery.quickView.openInProject') }}
      </DouButton>
      <DouButton size="sm" variant="outline" @click="emit('openProject', item.artifact.projectId)">
        <template #leading><FolderIcon /></template>
        {{ t('gallery.quickView.openProject') }}
      </DouButton>
    </div>

    <div class="quick-pane-scroll">
      <div class="quick-visual">
        <ArtifactThumb :artifact="artifact" />
      </div>

      <div
        v-if="item.pendingActions > 0 || item.pendingFeedback > 0 || artifact.openComments > 0"
        class="quick-badges"
      >
        <DouTag v-if="item.pendingActions > 0" size="sm" variant="soft" tone="warning" round>
          {{ t('gallery.status.needsMe') }} ({{ item.pendingActions }})
        </DouTag>
        <DouTag v-if="item.pendingFeedback > 0" size="sm" variant="soft" tone="primary" round>
          {{ t('gallery.status.feedbackPending') }}
        </DouTag>
        <DouTag v-if="artifact.openComments > 0" size="sm" variant="soft" tone="neutral" round>
          <template #leading><MessageDotsIcon /></template>
          {{ t('gallery.meta.comments', { count: artifact.openComments }) }}
        </DouTag>
      </div>

      <div class="quick-section">
        <DouText size="sm" variant="secondary" weight="medium">{{
          t('gallery.quickView.metadata')
        }}</DouText>
        <div class="quick-meta-grid">
          <div class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.project') }}</DouText>
            <DouText size="sm" weight="medium" truncate>{{ item.projectName }}</DouText>
          </div>
          <div v-if="item.nodeTitle" class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.planNode') }}</DouText>
            <DouText size="sm" truncate>{{ item.nodeTitle }}</DouText>
          </div>
          <div class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.updatedBy') }}</DouText>
            <DouText size="sm" truncate>{{ updaterLabel }}</DouText>
          </div>
          <div class="meta-row">
            <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.updatedAt') }}</DouText>
            <DouText size="sm">{{ updatedLabel }}</DouText>
          </div>
        </div>
      </div>

      <DouDivider />

      <div class="quick-section">
        <DouText size="sm" variant="secondary" weight="medium">{{
          t('gallery.quickView.summary')
        }}</DouText>
        <div v-if="loading" class="quick-loading" role="status">
          <DouSpinner size="sm" />
        </div>
        <DouAlert v-else-if="loadError" tone="danger" :title="t('gallery.quickView.loadError')" />
        <div v-else-if="artifact.kind === 'document' && documentMarkdown" class="quick-markdown">
          <DouMarkdown :content="documentMarkdown" link-target="_blank" :sanitize-url="sanitizeUrl">
            <template #link="{ href, title, external, children }">
              <DouLink
                v-if="innerLink(href)"
                :href="innerLinkHref(href)"
                variant="soft"
                @click="openMarkdownInnerLink(href!, $event)"
              >
                <component :is="children" />
              </DouLink>
              <a
                v-else-if="href"
                :href="href"
                :title="title"
                :target="external ? '_blank' : undefined"
                :rel="external ? 'noopener noreferrer' : undefined"
              >
                <component :is="children" />
              </a>
              <span v-else class="dou-markdown__link">
                <component :is="children" />
              </span>
            </template>
          </DouMarkdown>
        </div>
        <div v-else-if="preview?.kind === 'whiteboard'" class="quick-list-section">
          <DouText size="sm" variant="tertiary">
            {{ t('gallery.meta.items', { count: preview.items }) }} ·
            {{ t('gallery.meta.connectors', { count: preview.connectors }) }}
            <template v-if="(preview.prototypeCount ?? 0) > 0">
              · {{ t('gallery.meta.prototypes', { count: preview.prototypeCount ?? 0 }) }}
            </template>
            <template v-if="(preview.optionCount ?? 0) > 0">
              · {{ t('gallery.meta.options', { count: preview.optionCount ?? 0 }) }}
            </template>
          </DouText>
          <ul v-if="whiteboardHighlights.length" class="quick-highlights">
            <li v-for="(line, i) in whiteboardHighlights" :key="i">
              <DouText size="sm">{{ line }}</DouText>
            </li>
          </ul>
        </div>
        <div v-else-if="preview?.kind === 'dashboard'" class="quick-list-section">
          <DouText size="sm" variant="tertiary">
            {{ t('gallery.meta.widgets', { count: preview.widgets }) }}
            <template v-if="preview.isLive"> · {{ t('artifact.dashboard.live') }}</template>
          </DouText>
          <ul v-if="dashboardTitles.length" class="quick-highlights">
            <li v-for="(line, i) in dashboardTitles" :key="i">
              <DouText size="sm">{{ line }}</DouText>
            </li>
          </ul>
        </div>
      </div>

      <DouDivider />

      <div class="quick-section">
        <DouText size="sm" variant="secondary" weight="medium">{{
          t('gallery.quickView.versions')
        }}</DouText>
        <div v-if="versions.length" class="quick-versions">
          <div v-for="ver in versions.slice(0, 5)" :key="ver.version" class="version-row">
            <div class="version-top">
              <DouTag size="sm" variant="soft" tone="neutral" round>
                {{ t('gallery.meta.version', { version: ver.version }) }}
              </DouTag>
              <DouText size="sm" variant="tertiary">{{ formatVersionTime(ver.createdAt) }}</DouText>
            </div>
            <DouText v-if="ver.reason" size="sm" variant="secondary" class="version-reason">
              {{ ver.reason }}
            </DouText>
          </div>
        </div>
        <DouText v-else-if="!loading" size="sm" variant="tertiary">
          {{ t('gallery.quickView.noVersions') }}
        </DouText>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.quick-pane {
  display: flex;
  flex: none;
  flex-direction: column;
  width: 384px;
  max-width: 44%;
  height: 100%;
  min-height: 0;
  border-left: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
}

.quick-pane-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.quick-pane-title-wrap {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.quick-pane-heading {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.quick-pane-nav {
  display: flex;
  flex: none;
  align-items: center;
  gap: 2px;
}

.quick-pane-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.quick-primary-btn {
  flex: 1;
}

.quick-pane-scroll {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 14px;
  min-height: 0;
  padding: 14px;
  overflow-y: auto;
}

.quick-sheet-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-bottom: 12px;
}

.quick-sheet-footer {
  display: flex;
  gap: 8px;
  width: 100%;
}

.sheet-action-btn {
  flex: 1;
}

.quick-visual {
  overflow: hidden;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-container);
}

.quick-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.quick-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.quick-meta-grid {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.meta-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
}

.quick-loading {
  display: flex;
  justify-content: center;
  padding: 16px 0;
}

.quick-markdown {
  max-height: 360px;
  padding: 10px 12px;
  overflow-y: auto;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-canvas);
  font-size: 13px;
}

.quick-markdown :deep(img) {
  display: block;
  max-width: 100%;
  height: auto;
  margin-block: 6px;
  border-radius: var(--dou-radius-control);
}

.quick-list-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.quick-highlights {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding-left: 16px;
}

.quick-versions {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.version-row {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 8px 10px;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-canvas);
}

.version-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.version-reason {
  word-break: break-word;
}

.kind-avatar {
  flex: none;
  --dou-avatar-bg: var(--dou-color-primary-soft);
  --dou-avatar-fg: var(--dou-color-primary-text);
  --dou-avatar-font-size: 15px;
}

.kind-whiteboard {
  --dou-avatar-bg: var(--dou-color-warning-soft);
  --dou-avatar-fg: var(--dou-color-warning-text);
}

.kind-prototype {
  --dou-avatar-bg: color-mix(in srgb, var(--dou-color-chart-4) 16%, var(--dou-color-bg-surface));
  --dou-avatar-fg: var(--dou-color-chart-4);
}

.kind-dashboard {
  --dou-avatar-bg: var(--dou-color-success-soft);
  --dou-avatar-fg: var(--dou-color-success-text);
}
</style>
