<script setup lang="ts">
import type { ThreadStage } from '@ariadne/core'
import { DouEmpty, DouMenu, DouTabPanel, DouTabs, DouTag, DouText } from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { stageTone } from '../stage'
import ArtifactList from '../artifacts/ArtifactList.vue'
import PlanPanel from '../plan/PlanPanel.vue'
import { openThread, state } from '../store'
import ProjectSource from './ProjectSource.vue'
import TodoList from './TodoList.vue'

const tab = ref('plan')
const threadItems = computed(() =>
  (state.detail?.threads ?? []).map((thread) => ({ key: thread.id, label: thread.title })),
)
const stageOf = (id: string | number): ThreadStage =>
  state.detail?.threads.find((thread) => thread.id === id)?.stage ?? 'queued'
</script>

<template>
  <div class="side">
    <ProjectSource />
    <TodoList :todo="state.todo" />
    <DouTabs v-model="tab" class="side-tabs">
      <DouTabPanel value="plan" :label="t('panel.plan')">
        <PlanPanel />
      </DouTabPanel>
      <DouTabPanel value="board" :label="t('panel.board')">
        <DouEmpty size="sm" />
      </DouTabPanel>
      <DouTabPanel value="threads" :label="t('panel.threads')">
        <DouMenu
          v-if="threadItems.length"
          :model-value="state.threadId ?? undefined"
          :items="threadItems"
          @select="(key) => openThread(String(key))"
        >
          <template #label="{ item }">
            <span class="thread">
              <DouText truncate>{{ item.label }}</DouText>
              <DouTag size="sm" :tone="stageTone[stageOf(item.key)]">
                {{ t(`thread.stage.${stageOf(item.key)}`) }}
              </DouTag>
            </span>
          </template>
        </DouMenu>
        <DouEmpty v-else size="sm" />
      </DouTabPanel>
      <DouTabPanel value="artifacts" :label="t('panel.artifacts')">
        <ArtifactList />
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

.thread {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
</style>
