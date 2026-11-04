<script setup lang="ts">
import type { ArtifactKind, ArtifactSummary } from '@ariadne/core'
import {
  DouAvatar,
  DouButton,
  DouCard,
  DouDropdown,
  DouEmpty,
  DouInput,
  DouSegmentedControl,
  DouText,
} from 'dou-ui'
import { ChevronLeftIcon, PlusIcon, SearchIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { locale, t } from '../i18n'
import { openMain, state } from '../store'
import { createItems } from './create'
import ArtifactThumb from './ArtifactThumb.vue'
import { ARTIFACT_KINDS, kindIcon } from './kinds'

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

/** When it was last changed: the time today, the day this year, the date before. */
function editedAt(at: number): string {
  const date = new Date(at)
  const now = new Date()
  const options: Intl.DateTimeFormatOptions =
    date.toDateString() === now.toDateString()
      ? { hour: '2-digit', minute: '2-digit' }
      : date.getFullYear() === now.getFullYear()
        ? { month: 'long', day: 'numeric' }
        : { dateStyle: 'medium' }
  return t('artifacts.editedAt', { time: date.toLocaleString(locale.value, options) })
}
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
        padding="none"
        class="overview-card"
        @click="openMain({ kind: 'artifact', id: artifact.id })"
      >
        <ArtifactThumb :preview="artifact.preview" />
        <div class="card-info">
          <DouAvatar
            shape="square"
            :size="36"
            class="card-icon"
            :class="`card-icon-${artifact.kind}`"
          >
            <component :is="kindIcon[artifact.kind]" />
          </DouAvatar>
          <span class="card-text">
            <DouText weight="medium" truncate>{{ artifact.title }}</DouText>
            <DouText variant="tertiary" size="sm" truncate>
              {{ editedAt(artifact.updatedAt) }}
            </DouText>
          </span>
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
  grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 16px;
  padding: 12px 16px 16px;
  overflow-y: auto;
}

.overview-card {
  display: flex;
  flex-direction: column;
  min-width: 0;
  overflow: hidden;
  /* DouCard as a button keeps the browser's button padding, font and centring. */
  padding: 0;
  text-align: left;
  font: inherit;
}

.card-info {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  padding: 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.card-icon {
  flex: none;
  --dou-avatar-bg: var(--dou-color-primary-soft);
  --dou-avatar-fg: var(--dou-color-primary-text);
  --dou-avatar-font-size: 18px;
}

.card-icon-whiteboard {
  --dou-avatar-bg: var(--dou-color-warning-soft);
  --dou-avatar-fg: var(--dou-color-warning-text);
}

.card-icon-dashboard {
  --dou-avatar-bg: var(--dou-color-success-soft);
  --dou-avatar-fg: var(--dou-color-success-text);
}

.card-text {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.overview-empty {
  padding: 48px 16px;
}
</style>
