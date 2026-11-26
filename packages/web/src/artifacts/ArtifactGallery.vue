<script setup lang="ts">
import type {
  GalleryArtifactSummary,
  GalleryKindFilter,
  GallerySort,
  GalleryStatusFilter,
} from '@ariadne/core'
import {
  FilterIcon,
  FolderIcon,
  LayoutGridIcon,
  LayoutListIcon,
  RefreshIcon,
  SearchIcon,
  SlidersIcon,
} from '@dou-ui/icons'
import {
  DouAlert,
  DouBadge,
  DouButton,
  DouCheckTag,
  DouDivider,
  DouDrawer,
  DouEmpty,
  DouInput,
  DouSegmentedControl,
  DouSelect,
  DouSkeleton,
  DouSpinner,
  DouText,
  DouTooltip,
  DouVirtualList,
  useElementSize,
} from 'dou-ui'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue'
import { t } from '../i18n'
import { useServerNow } from '../server-clock'
import { onLiveEvent, openProject, openProjectArtifact } from '../store'
import type { WorkspaceLayoutMode } from '../workspace-layout'
import { ARTIFACT_VIRTUALIZE_THRESHOLD, gridColumnCount } from './card-grid'
import GalleryCard from './GalleryCard.vue'
import { groupGalleryItems, type GalleryGroupBy } from './gallery-groups'
import {
  activeGalleryFilterCount,
  galleryState,
  hasActiveGalleryFilters,
  loadGallery,
  loadMoreGallery,
  markGalleryStale,
  resetGalleryFilters,
  saveGalleryPrefs,
  type GalleryDensity,
  type GalleryViewMode,
} from './gallery-state'
import GalleryQuickPreview from './GalleryQuickPreview.vue'

const props = defineProps<{
  layoutMode: WorkspaceLayoutMode
}>()

const isNarrow = computed(() => props.layoutMode === 'narrow')
const now = useServerNow()

const canvasRef = useTemplateRef<HTMLElement>('canvasRef')
const sentinelRef = useTemplateRef<HTMLElement>('sentinelRef')
const searchInputRef = useTemplateRef<InstanceType<typeof DouInput>>('searchInputRef')
const { width: canvasWidth } = useElementSize(canvasRef)

const mobileFilterOpen = ref(false)

const cardMinWidth = computed(() => (galleryState.density === 'compact' ? 216 : 264))
const cardGap = computed(() => (galleryState.density === 'compact' ? 12 : 16))
const horizontalPadding = computed(() => (isNarrow.value ? 24 : 40))

const columns = computed(() =>
  gridColumnCount(
    Math.max(0, canvasWidth.value - horizontalPadding.value),
    cardMinWidth.value,
    cardGap.value,
  ),
)

const sections = computed(() =>
  groupGalleryItems(galleryState.items, galleryState.groupBy, now.value),
)

const virtualized = computed(
  () =>
    galleryState.groupBy === 'none' && galleryState.items.length > ARTIFACT_VIRTUALIZE_THRESHOLD,
)

const virtualRows = computed(() => {
  if (!virtualized.value) return []
  if (galleryState.viewMode === 'list') {
    return galleryState.items.map((item) => ({ key: item.artifact.id, items: [item] }))
  }
  const cols = Math.max(1, columns.value)
  const rows: { key: string; items: GalleryArtifactSummary[] }[] = []
  for (let i = 0; i < galleryState.items.length; i += cols) {
    const chunk = galleryState.items.slice(i, i + cols)
    rows.push({ key: chunk[0]!.artifact.id, items: chunk })
  }
  return rows
})

const estimatedRowHeight = computed(() => {
  if (galleryState.viewMode === 'list') return galleryState.density === 'compact' ? 68 : 78
  return galleryState.density === 'compact' ? 218 : 264
})

const kindOptions = computed(() => {
  const c = galleryState.counts
  const suffix = (n?: number) => (c ? ` ${n ?? 0}` : '')
  return [
    { value: '', label: `${t('gallery.kind.all')}${suffix(c?.total)}` },
    { value: 'document', label: `${t('gallery.kind.document')}${suffix(c?.documents)}` },
    { value: 'whiteboard', label: `${t('gallery.kind.whiteboard')}${suffix(c?.whiteboards)}` },
    { value: 'prototype', label: `${t('gallery.kind.prototype')}${suffix(c?.prototypes)}` },
    { value: 'dashboard', label: `${t('gallery.kind.dashboard')}${suffix(c?.dashboards)}` },
  ]
})

const projectOptions = computed(() => {
  const list = galleryState.counts?.projects ?? []
  return [
    { value: '', label: t('gallery.project.all') },
    ...list.map((entry) => ({
      value: entry.projectId,
      label: `${entry.projectName} (${entry.count})`,
    })),
  ]
})

const sortOptions = computed(() => [
  { value: 'updated_desc', label: t('gallery.sort.updated') },
  { value: 'created_desc', label: t('gallery.sort.created') },
  { value: 'title_asc', label: t('gallery.sort.title') },
])

const groupOptions = computed(() => [
  { value: 'none', label: t('gallery.group.none') },
  { value: 'time', label: t('gallery.group.time') },
  { value: 'project', label: t('gallery.group.project') },
])

const viewModeOptions = computed(() => [
  { value: 'grid', label: t('gallery.view.grid') },
  { value: 'list', label: t('gallery.view.list') },
])

const subtitleText = computed(() => {
  if (!galleryState.counts) return ''
  return t('gallery.subtitle', {
    projects: galleryState.counts.projects.length,
    count: galleryState.counts.total,
  })
})

const quickViewIndex = computed(() => {
  if (!galleryState.quickViewId) return -1
  return galleryState.items.findIndex((item) => item.artifact.id === galleryState.quickViewId)
})

const quickViewItem = computed(() =>
  quickViewIndex.value >= 0 ? (galleryState.items[quickViewIndex.value] ?? null) : null,
)

const hasPrevQuickView = computed(() => quickViewIndex.value > 0)
const hasNextQuickView = computed(
  () => quickViewIndex.value >= 0 && quickViewIndex.value < galleryState.items.length - 1,
)

function sectionTitle(section: (typeof sections.value)[number]): string {
  if (section.timeBucket) return t(`gallery.group.${section.timeBucket}`)
  if (section.projectName) return section.projectName
  return ''
}

function setKindFilter(value: unknown) {
  const next = String(value ?? '') as GalleryKindFilter
  if (galleryState.kindFilter === next) return
  galleryState.kindFilter = next
  galleryState.scrollTop = 0
  void loadGallery()
}

function setProjectFilter(value: unknown) {
  const next = String(value ?? '')
  if (galleryState.projectId === next) return
  galleryState.projectId = next
  galleryState.scrollTop = 0
  void loadGallery()
}

function toggleStatus(target: Exclude<GalleryStatusFilter, ''>) {
  galleryState.status = galleryState.status === target ? '' : target
  galleryState.scrollTop = 0
  void loadGallery()
}

function setSort(value: unknown) {
  const next = String(value ?? 'updated_desc') as GallerySort
  if (galleryState.sort === next) return
  galleryState.sort = next
  galleryState.scrollTop = 0
  void loadGallery()
}

function setGroupBy(value: unknown) {
  const next = String(value ?? 'none') as GalleryGroupBy
  if (galleryState.groupBy === next) return
  galleryState.groupBy = next
  saveGalleryPrefs()
}

function setViewMode(value: unknown) {
  const next = value === 'list' ? 'list' : ('grid' as GalleryViewMode)
  if (galleryState.viewMode === next) return
  galleryState.viewMode = next
  saveGalleryPrefs()
}

function toggleDensity() {
  galleryState.density =
    galleryState.density === 'comfortable' ? 'compact' : ('comfortable' as GalleryDensity)
  saveGalleryPrefs()
}

function setDensity(value: GalleryDensity) {
  if (galleryState.density === value) return
  galleryState.density = value
  saveGalleryPrefs()
}

function openItem(item: GalleryArtifactSummary) {
  galleryState.selectedId = item.artifact.id
  void openProjectArtifact(item.artifact.projectId, item.artifact.id)
}

function openQuickView(item: GalleryArtifactSummary) {
  galleryState.selectedId = item.artifact.id
  if (galleryState.quickViewId === item.artifact.id) {
    galleryState.quickViewId = null
  } else {
    galleryState.quickViewId = item.artifact.id
  }
}

function closeQuickView() {
  galleryState.quickViewId = null
}

function stepQuickView(delta: number) {
  const idx = quickViewIndex.value
  if (idx < 0) return
  const next = galleryState.items[idx + delta]
  if (!next) return
  galleryState.quickViewId = next.artifact.id
  galleryState.selectedId = next.artifact.id
}

function jumpToProject(projectId: string) {
  galleryState.quickViewId = null
  void openProject(projectId)
}

function onCanvasScroll() {
  if (canvasRef.value) {
    galleryState.scrollTop = canvasRef.value.scrollTop
  }
}

// Debounced query reload.
let queryTimer: ReturnType<typeof setTimeout> | undefined
watch(
  () => galleryState.query,
  () => {
    clearTimeout(queryTimer)
    queryTimer = setTimeout(() => {
      galleryState.scrollTop = 0
      void loadGallery()
    }, 220)
  },
)

// Live event refresh so newly created/updated artifacts appear without manual reload.
let liveRefreshTimer: ReturnType<typeof setTimeout> | undefined
const stopLive = onLiveEvent((event) => {
  if (
    event.type.startsWith('artifact.') ||
    event.type === 'project.deleted' ||
    event.type === 'project.renamed'
  ) {
    markGalleryStale()
    clearTimeout(liveRefreshTimer)
    liveRefreshTimer = setTimeout(() => {
      void loadGallery({ silent: true })
    }, 350)
  }
})

// Infinite scroll sentinel observer.
let sentinelObserver: IntersectionObserver | null = null
watch(
  [sentinelRef, () => galleryState.hasMore],
  ([el, hasMore]) => {
    sentinelObserver?.disconnect()
    sentinelObserver = null
    if (!el || !hasMore || typeof IntersectionObserver === 'undefined') return
    sentinelObserver = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          void loadMoreGallery()
        }
      },
      { root: canvasRef.value ?? null, rootMargin: '240px 0px' },
    )
    sentinelObserver.observe(el)
  },
  { flush: 'post' },
)

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable
}

function onKeyDown(event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.altKey) return
  if (event.key === '/' && !isEditableTarget(event.target)) {
    event.preventDefault()
    const root = searchInputRef.value?.$el as HTMLElement | undefined
    root?.querySelector<HTMLInputElement>('input')?.focus()
    return
  }
  if (event.key === 'Escape') {
    if (mobileFilterOpen.value) {
      mobileFilterOpen.value = false
      return
    }
    if (galleryState.quickViewId) {
      closeQuickView()
      return
    }
  }
  if (galleryState.quickViewId && !isEditableTarget(event.target)) {
    if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') {
      if (hasPrevQuickView.value) {
        event.preventDefault()
        stepQuickView(-1)
      }
    } else if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
      if (hasNextQuickView.value) {
        event.preventDefault()
        stepQuickView(1)
      }
    }
  }
}

onMounted(async () => {
  window.addEventListener('keydown', onKeyDown)
  if (!galleryState.loadedOnce || galleryState.stale) {
    await loadGallery({ silent: galleryState.loadedOnce })
  }
  await nextTick()
  if (canvasRef.value && galleryState.scrollTop > 0) {
    canvasRef.value.scrollTop = galleryState.scrollTop
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeyDown)
  clearTimeout(queryTimer)
  clearTimeout(liveRefreshTimer)
  sentinelObserver?.disconnect()
  stopLive()
})
</script>

<template>
  <div class="gallery-page" :class="{ 'gallery-narrow': isNarrow }">
    <header class="gallery-header">
      <div class="gallery-header-top">
        <div v-if="!isNarrow" class="gallery-title-group">
          <div class="gallery-title-row">
            <DouText weight="bold" size="lg">{{ t('gallery.title') }}</DouText>
            <DouBadge
              v-if="galleryState.counts"
              :value="galleryState.counts.total"
              tone="neutral"
            />
          </div>
          <DouText v-if="subtitleText" variant="tertiary" size="sm">
            {{ subtitleText }}
          </DouText>
        </div>

        <div class="gallery-search-controls">
          <DouInput
            ref="searchInputRef"
            v-model="galleryState.query"
            size="sm"
            clearable
            :placeholder="t('gallery.search')"
            :aria-label="t('gallery.search')"
            class="gallery-search"
          >
            <template #prefix><SearchIcon /></template>
          </DouInput>

          <DouButton
            v-if="isNarrow"
            variant="outline"
            class="mobile-filter-trigger"
            :aria-label="t('gallery.filters')"
            @click="mobileFilterOpen = true"
          >
            <template #leading><FilterIcon /></template>
            {{ t('gallery.filters') }}
            <DouBadge
              v-if="activeGalleryFilterCount > 0"
              :value="activeGalleryFilterCount"
              tone="primary"
            />
          </DouButton>

          <template v-else>
            <DouSegmentedControl
              :model-value="galleryState.viewMode"
              :options="viewModeOptions"
              size="sm"
              class="view-mode-switch"
              @update:model-value="setViewMode"
            >
              <template #option="{ option }">
                <span class="view-mode-option" :title="option.label">
                  <LayoutGridIcon v-if="option.value === 'grid'" aria-hidden="true" />
                  <LayoutListIcon v-else aria-hidden="true" />
                  <span class="sr-only">{{ option.label }}</span>
                </span>
              </template>
            </DouSegmentedControl>
            <DouTooltip
              :content="
                galleryState.density === 'comfortable'
                  ? t('gallery.density.compact')
                  : t('gallery.density.comfortable')
              "
              placement="bottom"
            >
              <DouButton
                size="sm"
                :variant="galleryState.density === 'compact' ? 'solid' : 'outline'"
                :tone="galleryState.density === 'compact' ? 'primary' : 'neutral'"
                :aria-label="t('gallery.density.compact')"
                :aria-pressed="galleryState.density === 'compact'"
                @click="toggleDensity"
              >
                <SlidersIcon />
              </DouButton>
            </DouTooltip>
            <div class="gallery-select-wrap">
              <DouSelect
                :model-value="galleryState.sort"
                :options="sortOptions"
                size="sm"
                :aria-label="t('gallery.sort.updated')"
                @update:model-value="setSort"
              />
            </div>
            <div class="gallery-select-wrap">
              <DouSelect
                :model-value="galleryState.groupBy"
                :options="groupOptions"
                size="sm"
                :aria-label="t('gallery.group.none')"
                @update:model-value="setGroupBy"
              />
            </div>
          </template>
        </div>
      </div>

      <!-- Narrow quick kind chips bar -->
      <div
        v-if="isNarrow"
        class="narrow-kind-strip"
        role="tablist"
        :aria-label="t('gallery.title')"
      >
        <DouCheckTag
          v-for="opt in kindOptions"
          :key="opt.value"
          :checked="galleryState.kindFilter === opt.value"
          round
          class="narrow-kind-chip"
          @update:checked="() => setKindFilter(opt.value)"
        >
          {{ opt.label }}
        </DouCheckTag>
      </div>

      <!-- Desktop / medium filter bar -->
      <div v-else class="gallery-filter-bar">
        <DouSegmentedControl
          :model-value="galleryState.kindFilter"
          :options="kindOptions"
          size="sm"
          class="kind-segmented"
          @update:model-value="setKindFilter"
        />

        <div class="gallery-filter-right">
          <div class="project-select-wrap">
            <DouSelect
              :model-value="galleryState.projectId"
              :options="projectOptions"
              size="sm"
              filterable
              :placeholder="t('gallery.project.placeholder')"
              :aria-label="t('gallery.project.placeholder')"
              @update:model-value="setProjectFilter"
            />
          </div>

          <div class="status-chips">
            <DouCheckTag
              :checked="galleryState.status === 'needs_me'"
              tone="warning"
              size="sm"
              round
              @update:checked="() => toggleStatus('needs_me')"
            >
              {{ t('gallery.status.needsMe') }}
              <template v-if="galleryState.counts && galleryState.counts.needsMe > 0">
                ({{ galleryState.counts.needsMe }})
              </template>
            </DouCheckTag>
            <DouCheckTag
              :checked="galleryState.status === 'has_comments'"
              tone="neutral"
              size="sm"
              round
              @update:checked="() => toggleStatus('has_comments')"
            >
              {{ t('gallery.status.hasComments') }}
            </DouCheckTag>
            <DouCheckTag
              :checked="galleryState.status === 'feedback_pending'"
              tone="primary"
              size="sm"
              round
              @update:checked="() => toggleStatus('feedback_pending')"
            >
              {{ t('gallery.status.feedbackPending') }}
              <template v-if="galleryState.counts && galleryState.counts.feedbackPending > 0">
                ({{ galleryState.counts.feedbackPending }})
              </template>
            </DouCheckTag>
          </div>

          <DouButton
            v-if="hasActiveGalleryFilters"
            size="sm"
            variant="ghost"
            @click="resetGalleryFilters"
          >
            {{ t('gallery.filters.reset') }}
          </DouButton>
        </div>
      </div>
    </header>

    <div class="gallery-body">
      <div ref="canvasRef" class="gallery-canvas" @scroll.passive="onCanvasScroll">
        <div v-if="galleryState.error" class="gallery-error">
          <DouAlert tone="danger" :title="t('gallery.error.load')">
            {{ galleryState.error }}
            <template #action>
              <DouButton size="sm" variant="outline" @click="loadGallery()">
                <template #leading><RefreshIcon /></template>
                {{ t('gallery.retry') }}
              </DouButton>
            </template>
          </DouAlert>
        </div>

        <div
          v-else-if="galleryState.loading && !galleryState.items.length"
          class="gallery-grid"
          :class="{ 'gallery-grid-compact': galleryState.density === 'compact' }"
          role="status"
        >
          <div v-for="n in 8" :key="n" class="skeleton-card">
            <DouSkeleton variant="rect" :height="galleryState.density === 'compact' ? 116 : 148" />
            <div class="skeleton-body">
              <DouSkeleton variant="text" :lines="2" />
            </div>
          </div>
        </div>

        <div v-else-if="!galleryState.items.length" class="gallery-empty-wrap">
          <DouEmpty
            :title="
              hasActiveGalleryFilters ? t('gallery.empty.filtered') : t('gallery.empty.title')
            "
            :description="hasActiveGalleryFilters ? undefined : t('gallery.empty.description')"
          >
            <DouButton
              v-if="hasActiveGalleryFilters"
              size="sm"
              variant="outline"
              @click="resetGalleryFilters"
            >
              {{ t('gallery.filters.reset') }}
            </DouButton>
          </DouEmpty>
        </div>

        <DouVirtualList
          v-else-if="virtualized"
          class="gallery-virtual"
          :items="virtualRows"
          item-key="key"
          :estimate-size="estimatedRowHeight"
          :overscan="3"
          :aria-label="t('gallery.title')"
        >
          <template #default="{ item: row }">
            <div
              v-if="galleryState.viewMode === 'grid'"
              class="gallery-virtual-row"
              :style="{
                gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
                gap: `${cardGap}px`,
              }"
            >
              <GalleryCard
                v-for="entry in row.items"
                :key="entry.artifact.id"
                :item="entry"
                view-mode="grid"
                :density="galleryState.density"
                :narrow="isNarrow"
                :selected="galleryState.quickViewId === entry.artifact.id"
                :hover-disabled="Boolean(galleryState.quickViewId)"
                @open="openItem"
                @quick-view="openQuickView"
                @filter-project="setProjectFilter"
              />
            </div>
            <div v-else class="gallery-virtual-list-row">
              <GalleryCard
                :item="row.items[0]!"
                view-mode="list"
                :density="galleryState.density"
                :narrow="isNarrow"
                :selected="galleryState.quickViewId === row.items[0]!.artifact.id"
                :hover-disabled="Boolean(galleryState.quickViewId)"
                @open="openItem"
                @quick-view="openQuickView"
                @filter-project="setProjectFilter"
              />
            </div>
          </template>
        </DouVirtualList>

        <div v-else class="gallery-sections">
          <section v-for="section in sections" :key="section.key" class="gallery-section">
            <div v-if="galleryState.groupBy !== 'none'" class="section-header">
              <FolderIcon v-if="section.projectId" class="section-icon" aria-hidden="true" />
              <DouText weight="medium" size="sm">{{ sectionTitle(section) }}</DouText>
              <DouBadge :value="section.items.length" tone="neutral" />
            </div>

            <div
              v-if="galleryState.viewMode === 'grid'"
              class="gallery-grid"
              :class="{ 'gallery-grid-compact': galleryState.density === 'compact' }"
            >
              <GalleryCard
                v-for="entry in section.items"
                :key="entry.artifact.id"
                :item="entry"
                view-mode="grid"
                :density="galleryState.density"
                :narrow="isNarrow"
                :selected="galleryState.quickViewId === entry.artifact.id"
                :hover-disabled="Boolean(galleryState.quickViewId)"
                @open="openItem"
                @quick-view="openQuickView"
                @filter-project="setProjectFilter"
              />
            </div>

            <div v-else class="gallery-list">
              <GalleryCard
                v-for="entry in section.items"
                :key="entry.artifact.id"
                :item="entry"
                view-mode="list"
                :density="galleryState.density"
                :narrow="isNarrow"
                :selected="galleryState.quickViewId === entry.artifact.id"
                :hover-disabled="Boolean(galleryState.quickViewId)"
                @open="openItem"
                @quick-view="openQuickView"
                @filter-project="setProjectFilter"
              />
            </div>
          </section>
        </div>

        <div v-if="galleryState.hasMore" ref="sentinelRef" class="gallery-load-more">
          <DouSpinner v-if="galleryState.loadingMore" size="sm" />
          <DouButton v-else size="sm" variant="outline" @click="loadMoreGallery()">
            {{ t('gallery.loadMore') }}
          </DouButton>
        </div>
      </div>

      <GalleryQuickPreview
        :item="quickViewItem"
        :open="Boolean(quickViewItem)"
        :narrow="isNarrow"
        :has-prev="hasPrevQuickView"
        :has-next="hasNextQuickView"
        @close="closeQuickView"
        @open-in-project="openItem"
        @open-project="jumpToProject"
        @prev="stepQuickView(-1)"
        @next="stepQuickView(1)"
      />
    </div>

    <!-- Narrow screen filter & sort bottom sheet -->
    <DouDrawer
      v-if="isNarrow"
      v-model:open="mobileFilterOpen"
      placement="bottom"
      swipeable
      :snap-points="['75%', '92%']"
      :title="t('gallery.filters')"
    >
      <div class="mobile-filter-sheet">
        <div class="sheet-field">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.project.placeholder')
          }}</DouText>
          <DouSelect
            :model-value="galleryState.projectId"
            :options="projectOptions"
            filterable
            @update:model-value="setProjectFilter"
          />
        </div>

        <div class="sheet-field">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.status.all')
          }}</DouText>
          <div class="sheet-chips">
            <DouCheckTag
              :checked="galleryState.status === 'needs_me'"
              tone="warning"
              round
              class="sheet-chip"
              @update:checked="() => toggleStatus('needs_me')"
            >
              {{ t('gallery.status.needsMe') }}
            </DouCheckTag>
            <DouCheckTag
              :checked="galleryState.status === 'has_comments'"
              tone="neutral"
              round
              class="sheet-chip"
              @update:checked="() => toggleStatus('has_comments')"
            >
              {{ t('gallery.status.hasComments') }}
            </DouCheckTag>
            <DouCheckTag
              :checked="galleryState.status === 'feedback_pending'"
              tone="primary"
              round
              class="sheet-chip"
              @update:checked="() => toggleStatus('feedback_pending')"
            >
              {{ t('gallery.status.feedbackPending') }}
            </DouCheckTag>
          </div>
        </div>

        <DouDivider />

        <div class="sheet-field">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.sort.updated')
          }}</DouText>
          <DouSelect
            :model-value="galleryState.sort"
            :options="sortOptions"
            @update:model-value="setSort"
          />
        </div>

        <div class="sheet-field">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.group.none')
          }}</DouText>
          <DouSelect
            :model-value="galleryState.groupBy"
            :options="groupOptions"
            @update:model-value="setGroupBy"
          />
        </div>

        <div class="sheet-field">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.view.grid')
          }}</DouText>
          <div class="sheet-chips">
            <DouButton
              :variant="galleryState.viewMode === 'grid' ? 'solid' : 'outline'"
              :tone="galleryState.viewMode === 'grid' ? 'primary' : 'neutral'"
              class="sheet-toggle-btn"
              @click="setViewMode('grid')"
            >
              <template #leading><LayoutGridIcon /></template>
              {{ t('gallery.view.grid') }}
            </DouButton>
            <DouButton
              :variant="galleryState.viewMode === 'list' ? 'solid' : 'outline'"
              :tone="galleryState.viewMode === 'list' ? 'primary' : 'neutral'"
              class="sheet-toggle-btn"
              @click="setViewMode('list')"
            >
              <template #leading><LayoutListIcon /></template>
              {{ t('gallery.view.list') }}
            </DouButton>
          </div>
        </div>

        <div class="sheet-field">
          <DouText size="sm" variant="secondary" weight="medium">{{
            t('gallery.density.comfortable')
          }}</DouText>
          <div class="sheet-chips">
            <DouButton
              :variant="galleryState.density === 'comfortable' ? 'solid' : 'outline'"
              :tone="galleryState.density === 'comfortable' ? 'primary' : 'neutral'"
              class="sheet-toggle-btn"
              @click="setDensity('comfortable')"
            >
              {{ t('gallery.density.comfortable') }}
            </DouButton>
            <DouButton
              :variant="galleryState.density === 'compact' ? 'solid' : 'outline'"
              :tone="galleryState.density === 'compact' ? 'primary' : 'neutral'"
              class="sheet-toggle-btn"
              @click="setDensity('compact')"
            >
              {{ t('gallery.density.compact') }}
            </DouButton>
          </div>
        </div>
      </div>

      <template #footer="{ close }">
        <div class="sheet-footer">
          <DouButton variant="outline" class="sheet-footer-btn" @click="resetGalleryFilters">
            {{ t('gallery.filters.reset') }}
          </DouButton>
          <DouButton tone="primary" class="sheet-footer-btn" @click="close">
            {{ t('gallery.filters.apply') }}
          </DouButton>
        </div>
      </template>
    </DouDrawer>
  </div>
</template>

<style scoped>
.gallery-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--dou-color-bg-canvas);
}

.gallery-header {
  display: flex;
  flex: none;
  flex-direction: column;
  gap: 10px;
  padding: 14px 20px 12px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
  background: var(--dou-color-bg-surface);
}

.gallery-narrow .gallery-header {
  padding: 10px 12px;
  gap: 8px;
}

.gallery-header-top {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.gallery-title-group {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.gallery-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.gallery-search-controls {
  display: flex;
  flex: 1;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  min-width: 220px;
}

.gallery-narrow .gallery-search-controls {
  width: 100%;
  justify-content: stretch;
  flex-wrap: nowrap;
}

.gallery-search {
  flex: 1 1 220px;
  max-width: 340px;
}

.gallery-narrow .gallery-search {
  max-width: none;
}

.gallery-select-wrap {
  flex: none;
  width: 124px;
}

.view-mode-option {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.mobile-filter-trigger {
  flex: none;
  min-height: 44px;
}

.gallery-filter-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.gallery-filter-right {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.project-select-wrap {
  flex: none;
  width: 180px;
}

.status-chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.narrow-kind-strip {
  display: flex;
  gap: 6px;
  overflow-x: auto;
  padding-bottom: 2px;
  scrollbar-width: none;
}

.narrow-kind-strip::-webkit-scrollbar {
  display: none;
}

.narrow-kind-chip {
  flex: none;
  min-height: 36px;
}

.gallery-body {
  display: flex;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.gallery-canvas {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  padding: 16px 20px 28px;
  overflow-y: auto;
}

.gallery-narrow .gallery-canvas {
  padding: 12px 12px calc(24px + var(--ariadne-safe-bottom, 0px));
}

.gallery-sections {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.gallery-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.section-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-bottom: 4px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.section-icon {
  width: 15px;
  height: 15px;
  color: var(--dou-color-text-secondary);
}

.gallery-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(264px, 100%), 1fr));
  gap: 16px;
}

.gallery-grid-compact {
  grid-template-columns: repeat(auto-fill, minmax(min(216px, 100%), 1fr));
  gap: 12px;
}

.gallery-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.gallery-virtual {
  flex: 1;
  min-height: 0;
}

.gallery-virtual-row {
  display: grid;
  padding-bottom: 16px;
}

.gallery-virtual-list-row {
  padding-bottom: 8px;
}

.skeleton-card {
  overflow: hidden;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-container);
  background: var(--dou-color-bg-surface);
}

.skeleton-body {
  padding: 12px;
}

.gallery-error {
  max-width: 540px;
  margin: 24px auto;
}

.gallery-empty-wrap {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  min-height: 280px;
}

.gallery-load-more {
  display: flex;
  justify-content: center;
  padding: 20px 0 8px;
}

/* Mobile filter sheet */
.mobile-filter-sheet {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding-bottom: 12px;
}

.sheet-field {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.sheet-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.sheet-chip {
  min-height: 40px;
}

.sheet-toggle-btn {
  flex: 1;
  min-height: 44px;
}

.sheet-footer {
  display: flex;
  gap: 8px;
  width: 100%;
}

.sheet-footer-btn {
  flex: 1;
  min-height: 44px;
}
</style>
