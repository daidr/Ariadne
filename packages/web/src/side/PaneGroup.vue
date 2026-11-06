<script setup lang="ts">
import {
  DouButton,
  DouDropdown,
  DouDropdownItem,
  DouDropdownRadioGroup,
  DouDropdownSub,
  DouTabPanel,
  DouTabs,
  DouTooltip,
  type TabsValue,
  useElementSize,
} from 'dou-ui'
import {
  BrainIcon,
  LayoutColumnsIcon,
  LayoutRowsIcon,
  MaximizeIcon,
  MessagesIcon,
  MoreHorizontalIcon,
  PackageIcon,
  SortIcon,
  WorkflowIcon,
} from '@dou-ui/icons'
import type { Component } from 'vue'
import { computed, useTemplateRef } from 'vue'
import { t } from '../i18n'
import ArtifactList from '../artifacts/ArtifactList.vue'
import PlanPanel from '../plan/PlanPanel.vue'
import ConversationPanel from '../components/ConversationPanel.vue'
import MemoryList from '../components/MemoryList.vue'
import StageIcon from '../components/StageIcon.vue'
import ThreadList from '../components/ThreadList.vue'
import { threadSort } from '../components/thread-list'
import type { ThreadSort } from '../stage'
import { lastPanel } from '../shortcuts'
import { closePane, openMain, setSide, splitPane, state } from '../store'
import * as side from './layout'
import type { PaneGroup, PaneKey } from './layout'

/** A group of tabs on the right side: the project's lists and threads, as the user put them. */
const props = defineProps<{ group: PaneGroup }>()

const sorts = ['active', 'title', 'created'] as const

const lists: Record<string, { label: () => string; icon: Component }> = {
  plan: { label: () => t('panel.plan'), icon: WorkflowIcon },
  threads: { label: () => t('panel.threads'), icon: MessagesIcon },
  artifacts: { label: () => t('panel.artifacts'), icon: PackageIcon },
  memory: { label: () => t('memory.title'), icon: BrainIcon },
}

const threadById = computed(() => new Map((state.detail?.threads ?? []).map((th) => [th.id, th])))
const threadAt = (key: PaneKey) => {
  const id = side.threadOf(key)
  return id ? (threadById.value.get(id) ?? null) : null
}
const labelOf = (key: PaneKey) => lists[key]?.label() ?? threadAt(key)?.title ?? ''

const active = computed(() => props.group.active)
const groups = computed(() => side.groupsOf(state.side.root))
const others = computed(() => groups.value.filter((item) => item.id !== props.group.id))

// Splitting halves the group: only offered while both halves still have room to be read.
const MIN_WIDTH = 220
const MIN_HEIGHT = 200
const root = useTemplateRef<HTMLElement>('root')
const size = useElementSize(root)
const canSplit = computed(() => props.group.tabs.length > 1)
const canSplitRight = computed(() => canSplit.value && size.width.value >= MIN_WIDTH * 2)
const canSplitDown = computed(() => canSplit.value && size.height.value >= MIN_HEIGHT * 2)

function select(value: TabsValue | undefined) {
  if (value === undefined) return
  const key = value as PaneKey
  setSide(side.activate(state.side, key))
  // The keyboard goes to the thread now in front.
  const threadId = side.threadOf(key)
  if (threadId) lastPanel.value = threadId
}

function use() {
  setSide(side.focus(state.side, props.group.id))
}

/** Bring a list into this group: moved here from wherever it is, or opened. */
function bring(key: PaneKey) {
  const at = side.groupWith(state.side, key)
  if (at && at.id !== props.group.id) setSide(side.move(state.side, key, props.group.id))
  else setSide(side.open(side.focus(state.side, props.group.id), key))
}
</script>

<template>
  <div ref="root" class="pane-group" @pointerdown.capture="use" @focusin="use">
    <DouTabs
      :model-value="active"
      height="100%"
      size="sm"
      class="pane-tabs"
      @update:model-value="select"
      @remove="(key) => closePane(key as PaneKey)"
    >
      <template #extra>
        <DouTooltip
          v-if="active === 'plan' && state.plan?.nodes.length"
          :content="t('plan.expand')"
          placement="bottom"
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
          v-else-if="active === 'threads' && state.detail?.threads.length"
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
        <DouTooltip v-if="canSplitRight" :content="t('side.splitRight')" placement="bottom">
          <DouButton
            size="sm"
            variant="ghost"
            :aria-label="t('side.splitRight')"
            @click="splitPane(active, 'horizontal')"
          >
            <LayoutColumnsIcon />
          </DouButton>
        </DouTooltip>
        <DouDropdown placement="bottom-end">
          <DouButton size="sm" variant="ghost" :aria-label="t('side.more')">
            <MoreHorizontalIcon />
          </DouButton>
          <template #menu>
            <DouDropdownItem
              v-if="canSplitDown"
              value="split-down"
              @click="splitPane(active, 'vertical')"
            >
              <template #prefix><LayoutRowsIcon /></template>
              {{ t('side.splitDown') }}
            </DouDropdownItem>
            <DouDropdownSub v-if="others.length" :title="t('side.moveTo')">
              <DouDropdownItem
                v-for="other in others"
                :key="other.id"
                :value="other.id"
                @click="setSide(side.move(state.side, active, other.id))"
              >
                {{ t('side.groupOf', { label: labelOf(other.active) }) }}
              </DouDropdownItem>
            </DouDropdownSub>
            <DouDropdownSub :title="t('side.open')">
              <DouDropdownItem
                v-for="key in side.LISTS"
                :key="key"
                :value="key"
                :disabled="group.tabs.includes(key)"
                @click="bring(key)"
              >
                <template #prefix><component :is="lists[key]!.icon" /></template>
                {{ lists[key]!.label() }}
              </DouDropdownItem>
            </DouDropdownSub>
            <DouDropdownItem
              v-if="group.tabs.some((key) => key !== active && side.threadOf(key))"
              value="close-others"
              divided
              @click="setSide(side.closeOthers(state.side, active))"
            >
              {{ t('side.closeOthers') }}
            </DouDropdownItem>
          </template>
        </DouDropdown>
      </template>
      <DouTabPanel
        v-for="key in group.tabs"
        :key="key"
        :value="key"
        :label="labelOf(key)"
        :icon="lists[key]?.icon"
        :closable="!!side.threadOf(key)"
        :class="['pane-panel', { 'pane-panel--list': !side.threadOf(key) }]"
      >
        <template v-if="threadAt(key)" #label>
          <span class="pane-thread-label">
            <StageIcon :stage="threadAt(key)!.stage" />
            <span class="pane-thread-title">{{ threadAt(key)!.title }}</span>
          </span>
        </template>
        <PlanPanel v-if="key === 'plan'" />
        <ThreadList v-else-if="key === 'threads'" />
        <ArtifactList v-else-if="key === 'artifacts'" />
        <MemoryList v-else-if="key === 'memory'" />
        <ConversationPanel v-else :thread-id="side.threadOf(key)!" />
      </DouTabPanel>
    </DouTabs>
  </div>
</template>

<style scoped>
.pane-group {
  height: 100%;
  min-width: 0;
  min-height: 0;
}

.pane-tabs {
  box-sizing: border-box;
  padding-top: 4px;
}

/* A conversation brings its own room; the lists get the panel's. */
.pane-panel {
  padding-top: 0;
}

.pane-panel--list {
  padding: 8px 8px 8px 16px;
}

.pane-thread-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.pane-thread-title {
  max-width: 16em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
