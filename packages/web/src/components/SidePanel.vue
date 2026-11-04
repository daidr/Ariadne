<script setup lang="ts">
import type { ThreadStage } from '@ariadne/core'
import { DouButton, DouEmpty, DouMenu, DouTabPanel, DouTabs, DouText, DouTooltip } from 'dou-ui'
import {
  BrainIcon,
  KanbanIcon,
  MaximizeIcon,
  MessagesIcon,
  PackageIcon,
  WorkflowIcon,
} from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { count, t } from '../i18n'
import ArtifactList from '../artifacts/ArtifactList.vue'
import PlanBoard from '../plan/PlanBoard.vue'
import PlanPanel from '../plan/PlanPanel.vue'
import { openMain, openThread, state } from '../store'
import MemoryList from './MemoryList.vue'
import SideTab from './SideTab.vue'
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
  <DouTabs v-model="tab" placement="end" class="side-tabs">
    <template #extra>
      <DouTooltip
        v-if="tab === 'plan' && state.plan?.nodes.length"
        :content="t('plan.expand')"
        placement="left"
      >
        <DouButton
          size="sm"
          variant="ghost"
          :aria-label="t('plan.expand')"
          @click="openMain({ kind: 'plan' })"
        >
          <MaximizeIcon />
        </DouButton>
      </DouTooltip>
    </template>
    <DouTabPanel value="plan" class="side-panel">
      <template #label
        ><SideTab :label="t('panel.plan')"><WorkflowIcon /></SideTab
      ></template>
      <PlanPanel />
    </DouTabPanel>
    <DouTabPanel value="board" class="side-panel">
      <template #label
        ><SideTab :label="t('panel.board')"><KanbanIcon /></SideTab
      ></template>
      <PlanBoard v-if="state.plan?.nodes.length" :plan="state.plan" compact />
      <DouEmpty v-else size="sm" :description="t('plan.empty')" />
    </DouTabPanel>
    <DouTabPanel value="threads" class="side-panel">
      <template #label
        ><SideTab :label="t('panel.threads')"><MessagesIcon /></SideTab
      ></template>
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
    <DouTabPanel value="artifacts" class="side-panel">
      <template #label
        ><SideTab :label="t('panel.artifacts')"><PackageIcon /></SideTab
      ></template>
      <ArtifactList />
    </DouTabPanel>
    <DouTabPanel value="memory" class="side-panel">
      <template #label
        ><SideTab :label="t('memory.title')"><BrainIcon /></SideTab
      ></template>
      <MemoryList />
    </DouTabPanel>
  </DouTabs>
</template>

<style scoped>
.side-tabs {
  --dou-tabs-padding-x: 0;
  box-sizing: border-box;
  height: 100%;
  padding: 8px 8px 0 16px;
}

/* The tabs stay in place on the right while a long panel scrolls. */
.side-panel {
  align-self: stretch;
  overflow-y: auto;
  padding-bottom: 8px;
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
