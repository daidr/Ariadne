<script setup lang="ts">
import type { ArtifactSummary, BackgroundView, ChangeTotals } from '@ariadne/core'
import { FileDiffIcon, PackageIcon } from '@dou-ui/icons'
import { DouButton, DouPopover } from 'dou-ui'
import { shallowRef } from 'vue'
import { t } from '../i18n'
import { useOpenView } from '../opened-from'
import { kindIcon } from '../artifacts/kinds'
import BackgroundTasks from './BackgroundTasks.vue'

defineProps<{
  conversationId: string
  changes: { threadId: string; totals: ChangeTotals | null } | null
  artifacts: ArtifactSummary[]
  background: BackgroundView[]
}>()
const openView = useOpenView()
const artifactsOpen = shallowRef(false)
function openArtifact(id: string) {
  artifactsOpen.value = false
  openView({ kind: 'artifact', id })
}
</script>

<template>
  <div class="conversation-bar" data-conversation-bar>
    <DouButton
      v-if="changes?.totals && changes.totals.files > 0"
      variant="outline"
      size="sm"
      class="conversation-pill"
      :aria-label="t('changes.open')"
      :title="t('changes.open')"
      @click="openView({ kind: 'changes', threadId: changes.threadId })"
    >
      <template #leading><FileDiffIcon /></template>
      <template v-if="changes.totals">
        <span class="pill-added">+{{ changes.totals.added }}</span>
        <span class="pill-removed">−{{ changes.totals.removed }}</span>
      </template>
    </DouButton>
    <DouPopover
      v-if="artifacts.length"
      v-model:open="artifactsOpen"
      trigger="click"
      placement="top-start"
      :title="t('artifacts.title')"
      :width="320"
      style="box-shadow: 0 2px 8px rgb(0 0 0 / 8%)"
    >
      <DouButton
        variant="outline"
        size="sm"
        class="conversation-pill"
        :aria-expanded="artifactsOpen"
      >
        <template #leading><PackageIcon /></template>
        {{ t('artifacts.count', { count: artifacts.length }) }}
      </DouButton>
      <template #content>
        <div class="capsule-artifacts">
          <DouButton
            v-for="artifact in artifacts"
            :key="artifact.id"
            variant="ghost"
            size="sm"
            class="capsule-artifact"
            :title="artifact.title"
            @click="openArtifact(artifact.id)"
          >
            <template #leading><component :is="kindIcon[artifact.kind]" /></template>
            <span class="capsule-artifact-title">{{ artifact.title }}</span>
          </DouButton>
        </div>
      </template>
    </DouPopover>
    <BackgroundTasks
      v-if="background.length"
      :key="conversationId"
      :conversation-id="conversationId"
      :commands="background"
    />
  </div>
</template>

<style scoped>
.conversation-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: none;
}
.conversation-bar::-webkit-scrollbar {
  display: none;
}
.conversation-pill {
  flex: none;
  border-radius: 999px;
  font-variant-numeric: tabular-nums;
  box-shadow: none;
  border-color: var(--dou-color-border);
}
.pill-added {
  color: var(--dou-color-success);
}
.pill-removed {
  color: var(--dou-color-danger);
}
.capsule-artifacts {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 300px;
  overflow: auto;
}
.capsule-artifact {
  justify-content: flex-start;
  width: 100%;
}
.capsule-artifact-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
