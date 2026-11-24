<script setup lang="ts">
import {
  DouButton,
  DouDropdown,
  DouEmpty,
  DouSpinner,
  DouVirtualList,
  useElementSize,
} from 'dou-ui'
import { LayoutGridIcon, PlusIcon } from '@dou-ui/icons'
import { computed, useTemplateRef } from 'vue'
import { t } from '../i18n'
import { createItems } from './create'
import { openMain, state } from '../store'
import ArtifactCard from './ArtifactCard.vue'
import { ARTIFACT_VIRTUALIZE_THRESHOLD, chunkArtifactRows, gridColumnCount } from './card-grid'
import { useOpenView } from '../opened-from'
import * as side from '../side/layout'

const CARD_MIN_WIDTH = 220
const CARD_GAP = 12
const ESTIMATED_ROW_HEIGHT = 224

const openView = useOpenView()
const root = useTemplateRef<HTMLElement>('root')
const { width } = useElementSize(root)

const virtualized = computed(() => state.artifacts.length > ARTIFACT_VIRTUALIZE_THRESHOLD)
const columns = computed(() => gridColumnCount(width.value, CARD_MIN_WIDTH, CARD_GAP))
const rows = computed(() =>
  virtualized.value ? chunkArtifactRows(state.artifacts, columns.value) : [],
)

/** The project's artifacts in the side panel, as the same cards the overview shows. */
// Marked while open, in a tab of its own or full screen.
const open = computed(() => {
  const ids = new Set(
    side
      .groupsOf(state.side.root)
      .map(side.activeOf)
      .flatMap((key) => {
        const view = side.viewOf(key)
        return view?.kind === 'artifact' ? [view.id] : []
      }),
  )
  if (state.full?.view.kind === 'artifact') ids.add(state.full.view.id)
  return ids
})
</script>

<template>
  <div ref="root" class="artifact-list" :class="{ 'is-virtualized': virtualized }">
    <div class="artifact-actions">
      <DouButton
        size="sm"
        variant="ghost"
        class="artifact-all"
        @click="openMain({ kind: 'artifacts' })"
      >
        <template #leading><LayoutGridIcon /></template>
        {{ t('artifacts.overview') }}
      </DouButton>
      <DouDropdown v-if="state.artifacts.length" :items="createItems" placement="bottom-end">
        <DouButton size="sm" variant="ghost">
          <template #leading><PlusIcon /></template>
          {{ t('artifacts.create') }}
        </DouButton>
      </DouDropdown>
    </div>
    <div v-if="state.currentId && !state.artifactsLoaded" class="artifact-loading" role="status">
      <DouSpinner size="sm" />
    </div>
    <DouVirtualList
      v-else-if="virtualized"
      class="artifact-virtual"
      :items="rows"
      item-key="key"
      :estimate-size="ESTIMATED_ROW_HEIGHT"
      :overscan="2"
      :aria-label="t('artifacts.title')"
    >
      <template #default="{ item }">
        <div
          class="artifact-virtual-row"
          :style="{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }"
        >
          <ArtifactCard
            v-for="artifact in item.items"
            :key="artifact.id"
            :artifact="artifact"
            :current="open.has(artifact.id)"
            @click="openView({ kind: 'artifact', id: artifact.id })"
          />
        </div>
      </template>
    </DouVirtualList>
    <div v-else-if="state.artifacts.length" class="artifact-cards">
      <ArtifactCard
        v-for="artifact in state.artifacts"
        :key="artifact.id"
        :artifact="artifact"
        :current="open.has(artifact.id)"
        @click="openView({ kind: 'artifact', id: artifact.id })"
      />
    </div>
    <DouEmpty v-else size="sm" :description="t('artifacts.empty')">
      <DouDropdown :items="createItems">
        <DouButton size="sm" variant="outline">
          <template #leading><PlusIcon /></template>
          {{ t('artifacts.create') }}
        </DouButton>
      </DouDropdown>
    </DouEmpty>
  </div>
</template>

<style scoped>
.artifact-list {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.artifact-list.is-virtualized {
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.artifact-actions {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}

.artifact-loading {
  display: grid;
  place-items: center;
  padding: 24px 0;
}

/* One column in a narrow panel, more as it widens. */
.artifact-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(220px, 100%), 1fr));
  gap: 12px;
}

.artifact-virtual {
  flex: 1;
  min-height: 0;
}

.artifact-virtual-row {
  display: grid;
  gap: 12px;
  padding-bottom: 12px;
}
</style>
