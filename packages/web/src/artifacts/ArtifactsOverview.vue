<script setup lang="ts">
import type { ArtifactKind, ArtifactSummary } from '@ariadne/core'
import {
  DouButton,
  DouCard,
  DouDropdown,
  DouEmpty,
  DouInput,
  DouSegmentedControl,
  DouTag,
  DouText,
  type ContextMenuItem,
} from 'dou-ui'
import { ChevronLeftIcon, PlusIcon, SearchIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { locale, t } from '../i18n'
import { createArtifact, openMain, state } from '../store'
import { authorName } from './author'
import { ARTIFACT_KINDS, kindIcon } from './kinds'

/** Every artifact of the project, by kind, with a glance at what each holds. */
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
const createItems = computed<ContextMenuItem[]>(() =>
  ARTIFACT_KINDS.map((kind) => ({
    key: kind,
    label: t(`artifact.kind.${kind}`),
    icon: kindIcon[kind],
    run: () => void createArtifact(kind),
  })),
)

const nodeTitle = (id: string | null) =>
  id ? (state.plan?.nodes.find((node) => node.id === id)?.title ?? null) : null

function matches(artifact: ArtifactSummary, words: string): boolean {
  if (!words) return true
  const preview = artifact.preview
  const text = [
    artifact.title,
    nodeTitle(artifact.nodeId) ?? '',
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

function glance(artifact: ArtifactSummary): string {
  const preview = artifact.preview
  if (preview.kind === 'document') return preview.excerpt || t('artifacts.preview.emptyDocument')
  if (preview.kind === 'whiteboard') {
    const sizes = t('artifacts.preview.whiteboard', {
      items: preview.items,
      connectors: preview.connectors,
    })
    return preview.texts.length ? `${sizes} · ${preview.texts.join(' · ')}` : sizes
  }
  const sizes = t('artifacts.preview.dashboard', { widgets: preview.widgets })
  return preview.titles.length ? `${sizes} · ${preview.titles.join(' · ')}` : sizes
}

function takenAt(artifact: ArtifactSummary): string | null {
  const preview = artifact.preview
  if (preview.kind !== 'dashboard' || !preview.takenAt) return null
  return t('artifact.dashboard.takenAt', { time: time(preview.takenAt) })
}

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div class="overview">
    <div class="overview-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'conversation' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('artifact.back') }}
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
    <div v-if="shown.length" class="overview-grid">
      <DouCard
        v-for="artifact in shown"
        :key="artifact.id"
        as="button"
        interactive
        padding="sm"
        class="overview-card"
        @click="openMain({ kind: 'artifact', id: artifact.id })"
      >
        <div class="card-head">
          <component :is="kindIcon[artifact.kind]" class="card-icon" />
          <DouText weight="medium" truncate class="card-title">{{ artifact.title }}</DouText>
          <DouTag size="sm" tone="neutral">{{ t(`artifact.kind.${artifact.kind}`) }}</DouTag>
        </div>
        <DouText variant="secondary" size="sm" class="card-glance">{{ glance(artifact) }}</DouText>
        <div class="card-foot">
          <DouText variant="tertiary" size="sm" truncate>
            {{
              t('artifact.updated', {
                who: authorName(artifact.updatedBy),
                time: time(artifact.updatedAt),
              })
            }}
            · {{ t('artifact.version', { version: artifact.version }) }}
          </DouText>
          <DouText v-if="takenAt(artifact)" variant="tertiary" size="sm" truncate>
            {{ takenAt(artifact) }}
          </DouText>
          <DouText v-if="nodeTitle(artifact.nodeId)" variant="tertiary" size="sm" truncate>
            {{ t('artifacts.node', { node: nodeTitle(artifact.nodeId) ?? '' }) }}
          </DouText>
        </div>
      </DouCard>
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
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 12px;
  padding: 12px 16px 16px;
  overflow-y: auto;
}

.overview-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  text-align: left;
  font: inherit;
}

.card-head {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.card-icon {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.card-title {
  flex: 1;
  min-width: 0;
}

.card-glance {
  display: -webkit-box;
  overflow: hidden;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  min-height: 3.6em;
  overflow-wrap: anywhere;
}

.card-foot {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.overview-empty {
  padding: 48px 16px;
}
</style>
