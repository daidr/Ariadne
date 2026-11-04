<script setup lang="ts">
import type { ThreadStage } from '@ariadne/core'
import { DouButton, DouEmpty, DouMenu, DouTabPanel, DouTabs, DouText } from 'dou-ui'
import { MaximizeIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { count, t } from '../i18n'
import ArtifactList from '../artifacts/ArtifactList.vue'
import PlanBoard from '../plan/PlanBoard.vue'
import PlanPanel from '../plan/PlanPanel.vue'
import { openMain, openThread, state } from '../store'
import MemoryList from './MemoryList.vue'
import ProjectSource from './ProjectSource.vue'
import StageLabel from './StageLabel.vue'

const tab = ref('plan')
const threadItems = computed(() =>
  (state.detail?.threads ?? []).map((thread) => ({ key: thread.id, label: thread.title })),
)
const usage = computed(() => state.detail?.project.usage ?? { input: 0, output: 0 })
const stageOf = (id: string | number): ThreadStage =>
  state.detail?.threads.find((thread) => thread.id === id)?.stage ?? 'queued'
</script>

<template>
  <div class="side">
    <ProjectSource />
    <DouTabs v-model="tab" class="side-tabs">
      <template #extra>
        <DouButton
          v-if="tab === 'plan' && state.plan?.nodes.length"
          size="sm"
          variant="ghost"
          :aria-label="t('plan.expand')"
          :title="t('plan.expand')"
          @click="openMain({ kind: 'plan' })"
        >
          <MaximizeIcon />
        </DouButton>
      </template>
      <DouTabPanel value="plan" :label="t('panel.plan')">
        <PlanPanel />
      </DouTabPanel>
      <DouTabPanel value="board" :label="t('panel.board')">
        <PlanBoard v-if="state.plan?.nodes.length" :plan="state.plan" compact />
        <DouEmpty v-else size="sm" :description="t('plan.empty')" />
      </DouTabPanel>
      <DouTabPanel value="threads" :label="t('panel.threads')">
        <DouText
          v-if="usage.input || usage.output"
          variant="tertiary"
          size="sm"
          class="usage"
          :title="t('usage.detail', { input: usage.input, output: usage.output })"
        >
          {{ t('usage.project', { value: count(usage.input + usage.output) }) }}
        </DouText>
        <DouMenu
          v-if="threadItems.length"
          :model-value="state.threadId ?? undefined"
          :items="threadItems"
          @select="(key) => openThread(String(key))"
        >
          <template #label="{ item }">
            <span class="thread">
              <DouText truncate>{{ item.label }}</DouText>
              <StageLabel :stage="stageOf(item.key)" />
            </span>
          </template>
        </DouMenu>
        <DouEmpty v-else size="sm" />
      </DouTabPanel>
      <DouTabPanel value="artifacts" :label="t('panel.artifacts')">
        <ArtifactList />
      </DouTabPanel>
      <DouTabPanel value="memory" :label="t('memory.title')">
        <MemoryList />
      </DouTabPanel>
    </DouTabs>
  </div>
</template>

<style scoped>
.side {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.side-tabs {
  flex: 1;
  min-height: 0;
  padding: 8px 16px;
  overflow-y: auto;
}

.usage {
  display: block;
  padding: 0 8px 4px;
}

.thread {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
</style>
