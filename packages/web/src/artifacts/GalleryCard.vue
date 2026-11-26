<script setup lang="ts">
import type { GalleryArtifactSummary } from '@ariadne/core'
import { AppWindowIcon, EyeIcon, FolderIcon, MessageDotsIcon, SparkleIcon } from '@dou-ui/icons'
import { DouAvatar, DouButton, DouPopover, DouTag, DouText } from 'dou-ui'
import { computed } from 'vue'
import { locale, t } from '../i18n'
import { useServerNow } from '../server-clock'
import ArtifactThumb from './ArtifactThumb.vue'
import { formatGalleryTime, formatGalleryUpdater } from './gallery-groups'
import { kindIcon } from './kinds'

const props = withDefaults(
  defineProps<{
    item: GalleryArtifactSummary
    viewMode?: 'grid' | 'list'
    density?: 'comfortable' | 'compact'
    selected?: boolean
    narrow?: boolean
    hoverDisabled?: boolean
  }>(),
  {
    viewMode: 'grid',
    density: 'comfortable',
    selected: false,
    narrow: false,
    hoverDisabled: false,
  },
)

const emit = defineEmits<{
  open: [item: GalleryArtifactSummary]
  quickView: [item: GalleryArtifactSummary]
  filterProject: [projectId: string]
}>()

const now = useServerNow()

const artifact = computed(() => props.item.artifact)
const preview = computed(() => artifact.value.preview)
const isLiveDashboard = computed(
  () => preview.value.kind === 'dashboard' && Boolean(preview.value.isLive),
)
const kindBadgeIcon = computed(() =>
  props.item.hasPrototype ? AppWindowIcon : kindIcon[artifact.value.kind],
)
const kindClass = computed(() =>
  props.item.hasPrototype ? 'kind-prototype' : `kind-${artifact.value.kind}`,
)
const kindLabel = computed(() =>
  props.item.hasPrototype ? t('gallery.kind.prototype') : t(`artifact.kind.${artifact.value.kind}`),
)
const timeLabel = computed(() =>
  formatGalleryTime(artifact.value.updatedAt, now.value, locale.value),
)
const updaterLabel = computed(() =>
  formatGalleryUpdater(props.item, {
    coordinator: t('gallery.meta.coordinator'),
    user: t('gallery.meta.user'),
  }),
)

const previewSnippet = computed(() => {
  const p = preview.value
  if (p.kind === 'document') return p.excerpt.trim()
  if (p.kind === 'whiteboard') return p.texts.slice(0, 4).join(' · ')
  return p.titles.slice(0, 4).join(' · ')
})

const previewStats = computed(() => {
  const p = preview.value
  if (p.kind === 'whiteboard') {
    const parts = [t('gallery.meta.items', { count: p.items })]
    if (p.connectors > 0) parts.push(t('gallery.meta.connectors', { count: p.connectors }))
    if ((p.prototypeCount ?? 0) > 0) {
      parts.push(t('gallery.meta.prototypes', { count: p.prototypeCount ?? 0 }))
    }
    return parts.join(' · ')
  }
  if (p.kind === 'dashboard') {
    const parts = [t('gallery.meta.widgets', { count: p.widgets })]
    if (p.isLive) parts.push(t('artifact.dashboard.live'))
    return parts.join(' · ')
  }
  return ''
})

function onCardClick() {
  emit('open', props.item)
}

function onCardKeydown(event: KeyboardEvent) {
  if (event.target !== event.currentTarget) return
  if (event.key === 'Enter') {
    event.preventDefault()
    onCardClick()
  } else if (event.key === ' ' || event.key === 'Spacebar') {
    event.preventDefault()
    emit('quickView', props.item)
  }
}

function onQuickViewClick(event: MouseEvent) {
  event.stopPropagation()
  emit('quickView', props.item)
}

function onProjectClick(event: MouseEvent) {
  event.stopPropagation()
  emit('filterProject', artifact.value.projectId)
}
</script>

<template>
  <DouPopover
    trigger="hover"
    placement="right"
    :width="320"
    :show-delay="420"
    :hide-delay="120"
    :open-on-tap="false"
    :disabled="hoverDisabled || narrow"
  >
    <div
      role="button"
      tabindex="0"
      class="gallery-card"
      :class="[
        `gallery-card-${viewMode}`,
        `gallery-card-${density}`,
        { 'gallery-card-selected': selected, 'gallery-card-narrow': narrow },
      ]"
      :data-artifact-id="artifact.id"
      :aria-pressed="selected ? 'true' : undefined"
      @click="onCardClick"
      @keydown="onCardKeydown"
    >
      <template v-if="viewMode === 'grid'">
        <div class="card-media">
          <ArtifactThumb
            :artifact="artifact"
            :size="density === 'compact' ? 'compact' : 'default'"
          />
          <div class="media-badges-start">
            <DouTag
              v-if="item.hasPrototype"
              size="sm"
              variant="solid"
              tone="primary"
              round
              class="proto-badge"
            >
              <template #leading><AppWindowIcon /></template>
              {{ t('gallery.kind.prototype') }}
            </DouTag>
            <DouTag
              v-else-if="isLiveDashboard"
              size="sm"
              variant="soft"
              tone="success"
              round
              class="live-badge"
            >
              <template #leading><SparkleIcon /></template>
              {{ t('artifact.dashboard.live') }}
            </DouTag>
          </div>
          <div class="media-actions-end">
            <DouButton
              size="sm"
              variant="outline"
              class="quick-view-btn"
              :aria-label="t('gallery.quickView')"
              :title="t('gallery.quickView')"
              @click="onQuickViewClick"
            >
              <template v-if="!narrow && density !== 'compact'" #leading><EyeIcon /></template>
              <span v-if="!narrow && density !== 'compact'">{{ t('gallery.quickView') }}</span>
              <EyeIcon v-else />
            </DouButton>
          </div>
        </div>

        <div class="card-body">
          <div class="card-title-row">
            <DouAvatar
              shape="square"
              :size="density === 'compact' ? 28 : 32"
              class="kind-avatar"
              :class="kindClass"
            >
              <component :is="kindBadgeIcon" />
            </DouAvatar>
            <div class="card-heading">
              <DouText
                weight="medium"
                class="card-title"
                :class="{ 'card-title-single': density === 'compact' }"
              >
                {{ artifact.title }}
              </DouText>
              <div class="card-subline">
                <button
                  type="button"
                  class="project-pill"
                  :title="item.projectName"
                  @click="onProjectClick"
                >
                  <FolderIcon class="project-pill-icon" aria-hidden="true" />
                  <span class="project-pill-name">{{ item.projectName }}</span>
                </button>
                <span class="meta-sep" aria-hidden="true">·</span>
                <DouText variant="tertiary" size="sm" class="meta-time">{{ timeLabel }}</DouText>
              </div>
            </div>
          </div>

          <div class="card-footer">
            <div class="card-meta-left">
              <DouText variant="tertiary" size="sm" truncate class="meta-updater">
                {{ updaterLabel }}
              </DouText>
              <span class="meta-sep" aria-hidden="true">·</span>
              <DouText variant="tertiary" size="sm" class="meta-version">
                {{ t('gallery.meta.version', { version: artifact.version }) }}
              </DouText>
            </div>
            <div
              v-if="
                item.pendingActions > 0 || item.pendingFeedback > 0 || artifact.openComments > 0
              "
              class="card-status-tags"
            >
              <DouTag v-if="item.pendingActions > 0" size="sm" tone="warning" variant="soft" round>
                {{ t('gallery.status.needsMe') }}
              </DouTag>
              <DouTag
                v-else-if="item.pendingFeedback > 0"
                size="sm"
                tone="primary"
                variant="soft"
                round
              >
                {{ t('gallery.status.feedbackPending') }}
              </DouTag>
              <DouTag
                v-if="artifact.openComments > 0"
                size="sm"
                tone="neutral"
                variant="soft"
                round
              >
                <template #leading><MessageDotsIcon /></template>
                {{ artifact.openComments }}
              </DouTag>
            </div>
          </div>
        </div>
      </template>

      <template v-else>
        <ArtifactThumb :artifact="artifact" size="mini" />
        <div class="list-main">
          <div class="list-title-row">
            <DouText weight="medium" truncate class="list-title">{{ artifact.title }}</DouText>
            <template v-if="!narrow">
              <DouTag
                v-if="item.hasPrototype"
                size="sm"
                variant="soft"
                tone="primary"
                round
                class="list-tag"
              >
                {{ t('gallery.kind.prototype') }}
              </DouTag>
              <DouTag v-else size="sm" variant="soft" tone="neutral" round class="list-tag">
                {{ kindLabel }}
              </DouTag>
            </template>
            <DouTag
              v-if="item.pendingActions > 0"
              size="sm"
              tone="warning"
              variant="soft"
              round
              class="list-tag"
            >
              {{ t('gallery.status.needsMe') }}
            </DouTag>
            <DouTag
              v-else-if="item.pendingFeedback > 0"
              size="sm"
              tone="primary"
              variant="soft"
              round
              class="list-tag"
            >
              {{ t('gallery.status.feedbackPending') }}
            </DouTag>
          </div>
          <div class="list-meta-row">
            <template v-if="narrow">
              <DouText variant="tertiary" size="sm" class="list-meta-fixed">{{
                kindLabel
              }}</DouText>
              <span class="meta-sep" aria-hidden="true">·</span>
              <DouText variant="secondary" size="sm" truncate class="list-meta-project">
                {{ item.projectName }}
              </DouText>
              <span class="meta-sep" aria-hidden="true">·</span>
              <DouText variant="tertiary" size="sm" class="list-meta-fixed">{{
                timeLabel
              }}</DouText>
            </template>
            <template v-else>
              <button type="button" class="project-pill" @click="onProjectClick">
                <FolderIcon class="project-pill-icon" aria-hidden="true" />
                <span class="project-pill-name">{{ item.projectName }}</span>
              </button>
              <span v-if="item.nodeTitle" class="meta-sep" aria-hidden="true">·</span>
              <DouText
                v-if="item.nodeTitle"
                variant="tertiary"
                size="sm"
                truncate
                class="list-node"
              >
                {{ item.nodeTitle }}
              </DouText>
              <span class="meta-sep" aria-hidden="true">·</span>
              <DouText variant="tertiary" size="sm" truncate>{{ updaterLabel }}</DouText>
              <span class="meta-sep" aria-hidden="true">·</span>
              <DouText variant="tertiary" size="sm" class="list-meta-fixed">{{
                t('gallery.meta.version', { version: artifact.version })
              }}</DouText>
              <span v-if="artifact.openComments > 0" class="meta-sep" aria-hidden="true">·</span>
              <DouText
                v-if="artifact.openComments > 0"
                variant="secondary"
                size="sm"
                class="list-comments"
              >
                <MessageDotsIcon class="inline-icon" aria-hidden="true" />
                {{ artifact.openComments }}
              </DouText>
            </template>
          </div>
        </div>
        <div class="list-end">
          <DouText v-if="!narrow" variant="tertiary" size="sm" class="list-time">{{
            timeLabel
          }}</DouText>
          <DouButton
            size="sm"
            variant="ghost"
            class="list-quick-btn"
            :aria-label="t('gallery.quickView')"
            :title="t('gallery.quickView')"
            @click="onQuickViewClick"
          >
            <EyeIcon />
          </DouButton>
        </div>
      </template>
    </div>

    <template #content>
      <div class="hover-preview">
        <div class="hover-head">
          <DouTag size="sm" variant="soft" :tone="item.hasPrototype ? 'primary' : 'neutral'" round>
            {{ kindLabel }}
          </DouTag>
          <DouText variant="tertiary" size="sm">
            {{ t('gallery.meta.version', { version: artifact.version }) }} · {{ timeLabel }}
          </DouText>
        </div>
        <DouText weight="medium" class="hover-title">{{ artifact.title }}</DouText>
        <div class="hover-project">
          <FolderIcon class="project-pill-icon" aria-hidden="true" />
          <DouText size="sm" variant="secondary" truncate>{{ item.projectName }}</DouText>
          <template v-if="item.nodeTitle">
            <span class="meta-sep" aria-hidden="true">·</span>
            <DouText size="sm" variant="tertiary" truncate>{{ item.nodeTitle }}</DouText>
          </template>
        </div>
        <DouText v-if="previewSnippet" size="sm" variant="secondary" class="hover-snippet">
          {{ previewSnippet }}
        </DouText>
        <DouText v-if="previewStats" size="sm" variant="tertiary">
          {{ previewStats }}
        </DouText>
        <div class="hover-foot">
          <DouText size="sm" variant="tertiary">{{ t('gallery.quickView.hint') }}</DouText>
        </div>
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.gallery-card {
  position: relative;
  display: flex;
  min-width: 0;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-container);
  background: var(--dou-color-bg-surface);
  color: var(--dou-color-text);
  cursor: pointer;
  transition:
    border-color 140ms ease,
    box-shadow 140ms ease,
    background-color 140ms ease;
}

.gallery-card:hover {
  border-color: var(--dou-color-border-strong);
  box-shadow: var(--dou-shadow-sm);
}

.gallery-card:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
}

.gallery-card-selected {
  border-color: var(--dou-color-primary);
  box-shadow: 0 0 0 1px var(--dou-color-primary);
}

.gallery-card-grid {
  flex-direction: column;
  overflow: hidden;
}

.card-media {
  position: relative;
  overflow: hidden;
}

.media-badges-start {
  position: absolute;
  top: 8px;
  left: 8px;
  display: flex;
  gap: 4px;
  pointer-events: none;
}

.media-actions-end {
  position: absolute;
  top: 8px;
  right: 8px;
  display: flex;
  opacity: 0;
  transition: opacity 140ms ease;
}

.gallery-card:hover .media-actions-end,
.gallery-card:focus-within .media-actions-end,
.gallery-card-selected .media-actions-end,
.gallery-card-narrow .media-actions-end {
  opacity: 1;
}

.quick-view-btn {
  background: color-mix(in srgb, var(--dou-color-bg-surface) 90%, transparent);
  backdrop-filter: blur(6px);
  box-shadow: var(--dou-shadow-sm);
}

.gallery-card-grid.gallery-card-narrow :deep(.thumb) {
  height: 132px;
}

.gallery-card-grid.gallery-card-narrow.gallery-card-compact :deep(.thumb) {
  height: 108px;
}

.card-body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  padding: 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.gallery-card-compact .card-body {
  gap: 6px;
  padding: 10px;
}

.card-title-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  min-width: 0;
}

.kind-avatar {
  flex: none;
  margin-top: 1px;
  --dou-avatar-bg: var(--dou-color-primary-soft);
  --dou-avatar-fg: var(--dou-color-primary-text);
  --dou-avatar-font-size: 16px;
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

.card-heading {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.card-title {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
  line-height: 1.35;
  word-break: break-word;
}

.card-title-single {
  -webkit-line-clamp: 1;
}

.card-subline {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}

.project-pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 150px;
  min-width: 0;
  padding: 1px 6px;
  border: 0;
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-muted);
  color: var(--dou-color-text-secondary);
  font-size: 12px;
  line-height: 1.4;
  cursor: pointer;
}

.project-pill:hover {
  background: var(--dou-color-primary-soft);
  color: var(--dou-color-primary-text);
}

.project-pill-icon {
  flex: none;
  width: 12px;
  height: 12px;
}

.project-pill-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.meta-sep {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.meta-time,
.meta-version {
  flex: none;
  white-space: nowrap;
}

.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
  margin-top: auto;
}

.card-meta-left {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}

.meta-updater {
  min-width: 0;
}

.card-status-tags {
  display: flex;
  flex: none;
  align-items: center;
  gap: 4px;
}

/* List mode */
.gallery-card-list {
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
}

.gallery-card-list.gallery-card-compact {
  padding: 8px 10px;
}

.gallery-card-list.gallery-card-narrow {
  gap: 10px;
  padding: 8px 10px;
}

.gallery-card-list.gallery-card-narrow :deep(.thumb-size-mini) {
  width: 56px;
  height: 40px;
}

.list-main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.list-title-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.list-title {
  min-width: 0;
}

.list-tag {
  flex: none;
}

.list-meta-row {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  overflow: hidden;
  flex-wrap: nowrap;
}

.list-meta-fixed {
  flex: none;
  white-space: nowrap;
}

.list-meta-project {
  min-width: 0;
}

.list-node {
  max-width: 180px;
}

.list-comments {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex: none;
}

.inline-icon {
  width: 13px;
  height: 13px;
}

.list-end {
  display: flex;
  flex: none;
  align-items: center;
  gap: 6px;
}

.list-time {
  white-space: nowrap;
}

/* Hover quick preview popover */
.hover-preview {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 2px;
}

.hover-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.hover-title {
  line-height: 1.35;
}

.hover-project {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}

.hover-snippet {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 4;
  overflow: hidden;
  line-height: 1.45;
  word-break: break-word;
}

.hover-foot {
  padding-top: 6px;
  border-top: 1px solid var(--dou-color-border-subtle);
}
</style>
