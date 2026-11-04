<script setup lang="ts">
import { DouButton, DouDropdown, DouEmpty } from 'dou-ui'
import { LayoutGridIcon, PlusIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { createItems } from './create'
import { openMain, state } from '../store'
import ArtifactCard from './ArtifactCard.vue'

/** The project's artifacts in the side panel, as the same cards the overview shows. */
const current = computed(() => (state.main.kind === 'artifact' ? state.main.id : undefined))
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
      :current="artifact.id === current"
      @click="openMain({ kind: 'artifact', id: artifact.id })"
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
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}
</style>
