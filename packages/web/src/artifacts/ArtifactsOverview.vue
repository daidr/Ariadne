<script setup lang="ts">
import type { ArtifactKind, ArtifactSummary } from '@ariadne/core'
import {
  DouButton,
  DouDropdown,
  DouEmpty,
  DouInput,
  DouSegmentedControl,
  DouSpinner,
  DouText,
  DouVirtualList,
  useElementSize,
} from 'dou-ui'
import { ChevronLeftIcon, PlusIcon, SearchIcon } from '@dou-ui/icons'
import { computed, ref, useTemplateRef } from 'vue'
import { t } from '../i18n'
import { backMain, state } from '../store'
import { createItems } from './create'
import ArtifactCard from './ArtifactCard.vue'
import { ARTIFACT_VIRTUALIZE_THRESHOLD, chunkArtifactRows, gridColumnCount } from './card-grid'
import { ARTIFACT_KINDS } from './kinds'
import { useOpenView } from '../opened-from'

const CARD_MIN_WIDTH = 260
const CARD_GAP = 16
const HORIZONTAL_PADDING = 32
const ESTIMATED_ROW_HEIGHT = 228

const openView = useOpenView()
const root = useTemplateRef<HTMLElement>('root')
const { width } = useElementSize(root)

/** Every artifact of the project as cards: a picture of what it holds, its name and when. */
const filter = ref<ArtifactKind | 'all'>('all')
const query = ref('')

const counts = computed(() => {
  const byKind: Record<string, number> = { all: state.artifacts.length }
  for (const artifact of state.artifacts) byKind[artifact.kind] = (byKind[artifact.kind] ?? 0) + 1
  return byKind
})
const filterOptions = computed(() =>
  (['all', ...ARTIFACT_KINDS] as const).map((kind) => ({
    value: kind,
    label: `${t(kind === 'all' ? 'artifacts.all' : `artifact.kind.${kind}`)} ${counts.value[kind] ?? 0}`,
  })),
)

const nodeTitles = computed(() => {
  const titles = new Map<string, string>()
  for (const node of state.plan?.nodes ?? []) titles.set(node.id, node.title)
  return titles
})

function matches(artifact: ArtifactSummary, words: string): boolean {
  if (!words) return true
  const preview = artifact.preview
  const text = [
    artifact.title,
    (artifact.nodeId ? nodeTitles.value.get(artifact.nodeId) : undefined) ?? '',
    preview.kind === 'document' ? preview.excerpt : '',
    ...(preview.kind === 'whiteboard' ? preview.texts : []),
    ...(preview.kind === 'dashboard' ? preview.titles : []),
  ]
    .join(' ')
    .toLowerCase()
  return words.split(/\s+/).every((word) => text.includes(word))
}

const shown = computed(() => {
  const words = query.value.trim().toLowerCase()
  return state.artifacts.filter(
    (a) => (filter.value === 'all' || a.kind === filter.value) && matches(a, words),
  )
})

const virtualized = computed(() => shown.value.length > ARTIFACT_VIRTUALIZE_THRESHOLD)
const columns = computed(() =>
  gridColumnCount(Math.max(0, width.value - HORIZONTAL_PADDING), CARD_MIN_WIDTH, CARD_GAP),
)
const rows = computed(() =>
  virtualized.value ? chunkArtifactRows(shown.value, columns.value) : [],
)
</script>

<template>
  <div ref="root" class="overview">
    <div class="overview-header">
      <DouButton variant="ghost" size="sm" @click="backMain">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('view.back') }}
      </DouButton>
      <DouText weight="medium" class="overview-title">{{ t('artifacts.title') }}</DouText>
      <DouDropdown :items="createItems" placement="bottom-end">
        <DouButton size="sm" tone="primary">
          <template #leading><PlusIcon /></template>
          {{ t('artifacts.create') }}
        </DouButton>
      </DouDropdown>
    </div>
    <div class="overview-tools">
      <DouSegmentedControl v-model="filter" :options="filterOptions" size="sm" />
      <DouInput
        v-model="query"
        size="sm"
        clearable
        :placeholder="t('artifacts.search')"
        :aria-label="t('artifacts.search')"
        class="overview-search"
      >
        <template #prefix><SearchIcon /></template>
      </DouInput>
    </div>
    <div v-if="state.currentId && !state.artifactsLoaded" class="overview-loading" role="status">
      <DouSpinner />
    </div>
    <DouVirtualList
      v-else-if="virtualized"
      class="overview-virtual"
      :items="rows"
      item-key="key"
      :estimate-size="ESTIMATED_ROW_HEIGHT"
      :overscan="2"
      :aria-label="t('artifacts.title')"
    >
      <template #default="{ item }">
        <div
          class="overview-virtual-row"
          :style="{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }"
        >
          <ArtifactCard
            v-for="artifact in item.items"
            :key="artifact.id"
            :artifact="artifact"
            @click="openView({ kind: 'artifact', id: artifact.id })"
          />
        </div>
      </template>
    </DouVirtualList>
    <div v-else-if="shown.length" class="overview-grid">
      <ArtifactCard
        v-for="artifact in shown"
        :key="artifact.id"
        :artifact="artifact"
        @click="openView({ kind: 'artifact', id: artifact.id })"
      />
    </div>
    <DouEmpty
      v-else
      :description="state.artifacts.length ? t('artifacts.noMatch') : t('artifacts.empty')"
      class="overview-empty"
    />
  </div>
</template>

<style scoped>
.overview {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.overview-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.overview-title {
  flex: 1;
}

.overview-tools {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 12px 16px 0;
}

.overview-search {
  flex: 1;
  min-width: 160px;
  max-width: 320px;
  margin-left: auto;
}

.overview-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 16px;
  padding: 12px 16px max(16px, var(--ariadne-safe-bottom));
  overflow-y: auto;
}

.overview-virtual {
  flex: 1;
  min-height: 0;
  padding: 12px 16px max(16px, var(--ariadne-safe-bottom));
}

.overview-virtual-row {
  display: grid;
  gap: 16px;
  padding-bottom: 16px;
}

.overview-empty {
  padding: 48px 16px;
}

.overview-loading {
  display: grid;
  place-items: center;
  flex: 1;
  min-height: 160px;
}
</style>
