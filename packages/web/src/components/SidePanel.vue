<script setup lang="ts">
import {
  DouButton,
  DouDropdown,
  DouDropdownItem,
  DouDropdownRadioGroup,
  DouTabPanel,
  DouTabs,
  DouTooltip,
} from 'dou-ui'
import {
  BrainIcon,
  MaximizeIcon,
  MessagesIcon,
  PackageIcon,
  SortIcon,
  WorkflowIcon,
} from '@dou-ui/icons'
import { ref } from 'vue'
import { t } from '../i18n'
import ArtifactList from '../artifacts/ArtifactList.vue'
import PlanPanel from '../plan/PlanPanel.vue'
import { openMain, state } from '../store'
import MemoryList from './MemoryList.vue'
import ThreadList from './ThreadList.vue'
import type { ThreadSort } from '../stage'
import { threadSort } from './thread-list'

const tab = ref('plan')
const sorts = ['active', 'title', 'created'] as const
</script>

<template>
  <DouTabs v-model="tab" placement="end" height="100%" icon-only class="side-tabs">
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
      <DouDropdown
        v-else-if="tab === 'threads' && state.detail?.threads.length"
        placement="bottom-end"
      >
        <DouButton size="sm" variant="ghost" :aria-label="t('threads.sort')">
          <SortIcon />
        </DouButton>
        <template #menu>
          <DouDropdownRadioGroup
            :label="t('threads.sort')"
            :model-value="threadSort"
            @update:model-value="(value) => (threadSort = value as ThreadSort)"
          >
            <DouDropdownItem v-for="sort in sorts" :key="sort" :value="sort">
              {{ t(`threads.sort.${sort}`) }}
            </DouDropdownItem>
          </DouDropdownRadioGroup>
        </template>
      </DouDropdown>
    </template>
    <DouTabPanel value="plan" :label="t('panel.plan')" :icon="WorkflowIcon" class="side-panel">
      <PlanPanel />
    </DouTabPanel>
    <DouTabPanel
      value="threads"
      :label="t('panel.threads')"
      :icon="MessagesIcon"
      class="side-panel"
    >
      <ThreadList />
    </DouTabPanel>
    <DouTabPanel
      value="artifacts"
      :label="t('panel.artifacts')"
      :icon="PackageIcon"
      class="side-panel"
    >
      <ArtifactList />
    </DouTabPanel>
    <DouTabPanel value="memory" :label="t('memory.title')" :icon="BrainIcon" class="side-panel">
      <MemoryList />
    </DouTabPanel>
  </DouTabs>
</template>

<style scoped>
.side-tabs {
  --dou-tabs-padding-x: 0;
  box-sizing: border-box;
  padding: 8px 8px 0 16px;
}

.side-panel {
  padding-bottom: 8px;
}
</style>
