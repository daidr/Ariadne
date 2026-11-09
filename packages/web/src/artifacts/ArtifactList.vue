<script setup lang="ts">
import { DouButton, DouDropdown, DouEmpty } from 'dou-ui'
import { LayoutGridIcon, PlusIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { createItems } from './create'
import { openMain, state } from '../store'
import ArtifactCard from './ArtifactCard.vue'
import { useOpenView } from '../opened-from'
import * as side from '../side/layout'

const openView = useOpenView()

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
  <DouButton
    size="sm"
    variant="ghost"
    class="artifact-all"
    @click="openMain({ kind: 'artifacts' })"
  >
    <template #leading><LayoutGridIcon /></template>
    {{ t('artifacts.overview') }}
  </DouButton>
  <div v-if="state.artifacts.length" class="artifact-cards">
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
</template>

<style scoped>
.artifact-all {
  margin-bottom: 8px;
}

/* One column in a narrow panel, more as it widens. */
.artifact-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(220px, 100%), 1fr));
  gap: 12px;
}
</style>
