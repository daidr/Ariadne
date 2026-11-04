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
import SideTab from './SideTab.vue'
import ThreadList from './ThreadList.vue'
import type { ThreadSort } from '../stage'
import { threadSort } from './thread-list'

const tab = ref('plan')
const sorts = ['active', 'title', 'created'] as const
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
      <DouDropdown
        v-else-if="tab === 'threads' && state.detail?.threads.length"
        placement="bottom-end"
      >
        <DouButton size="sm" variant="ghost" :aria-label="t('threads.sort')">
          <SortIcon />
        </DouButton>
        <template #menu>
          <DouDropdownRadioGroup
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
    <DouTabPanel value="plan" class="side-panel">
      <template #label
        ><SideTab :label="t('panel.plan')"><WorkflowIcon /></SideTab
      ></template>
      <PlanPanel />
    </DouTabPanel>
    <DouTabPanel value="threads" class="side-panel">
      <template #label
        ><SideTab :label="t('panel.threads')"><MessagesIcon /></SideTab
      ></template>
      <ThreadList />
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
</style>
