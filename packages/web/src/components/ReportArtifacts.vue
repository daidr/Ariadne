<script setup lang="ts">
import { DouLink } from 'dou-ui'
import type { ThreadArtifact } from '@ariadne/core'
import { kindIcon } from '../artifacts/kinds'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/** The artifacts a thread's report points to, opened by their titles. */
defineProps<{ artifacts: ThreadArtifact[] }>()
</script>

<template>
  <span v-if="artifacts.length" class="report-artifacts">
    <DouLink
      v-for="artifact in artifacts"
      :key="artifact.id"
      :title="artifact.title"
      class="report-artifact"
      underline="none"
      @click="openView({ kind: 'artifact', id: artifact.id })"
    >
      <template #leading><component :is="kindIcon[artifact.kind]" /></template>
      {{ artifact.title }}
    </DouLink>
  </span>
</template>

<style scoped>
.report-artifacts {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 6px;
  min-width: 0;
}

.report-artifact {
  max-width: 100%;
  padding-inline: 4px;
  border-radius: 4px;
  background: var(--dou-color-bg-muted);
  overflow: hidden;
  font-size: var(--dou-font-size-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
