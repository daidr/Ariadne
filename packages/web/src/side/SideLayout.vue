<script setup lang="ts">
import {
  DouButton,
  DouDock,
  DouDropdown,
  DouDropdownItem,
  DouDropdownRadioGroup,
  DouDropdownSub,
  DouTooltip,
  type DockGroup,
  type DockNode,
  type DockPanel,
} from 'dou-ui'
import {
  FileDiffIcon,
  LayoutColumnsIcon,
  LayoutRowsIcon,
  MaximizeIcon,
  MessagesIcon,
  PackageIcon,
  WorkflowIcon,
} from '@dou-ui/icons'
import type { Component } from 'vue'
import { computed, onBeforeUnmount, useTemplateRef, watch, watchEffect } from 'vue'
import { t } from '../i18n'
import ArtifactList from '../artifacts/ArtifactList.vue'
import PlanPanel from '../plan/PlanPanel.vue'
import ThreadTools from '../components/ThreadTools.vue'
import ConversationPanel from '../components/ConversationPanel.vue'
import OpenedView from '../full/OpenedView.vue'
import { kindIcon } from '../artifacts/kinds'
import StageIcon from '../components/StageIcon.vue'
import ThreadList from '../components/ThreadList.vue'
import { lastPanel } from '../shortcuts'
import {
  activeThreadId,
  closePane,
  openedViewOf,
  openMain,
  setSide,
  splitPane,
  state,
} from '../store'
import { sideDock } from './dock'
import * as side from './layout'
import type { PaneKey } from './layout'
import { MIN_HEIGHT, MIN_WIDTH } from './sizes'

/**
 * The right side, beside the coordinator: the project's lists and threads in groups of
 * tabs, split as the user likes. A tab dragged to the edge of a group gets a group of its
 * own there.
 */

const lists: Record<string, { label: () => string; icon: Component }> = {
  plan: { label: () => t('panel.plan'), icon: WorkflowIcon },
  threads: { label: () => t('panel.threads'), icon: MessagesIcon },
  artifacts: { label: () => t('panel.artifacts'), icon: PackageIcon },
}

const threadById = computed(() => new Map((state.detail?.threads ?? []).map((th) => [th.id, th])))
const threadAt = (key: PaneKey) => {
  const id = side.threadOf(key)
  return id ? (threadById.value.get(id) ?? null) : null
}
const artifactById = computed(() => new Map(state.artifacts.map((a) => [a.id, a])))

/** What an artifact or changes tab is called, with its icon. */
function viewLabel(key: PaneKey): { label: string; icon: Component } | null {
  const view = side.viewOf(key)
  if (!view) return null
  if (view.kind === 'artifact') {
    const artifact = artifactById.value.get(view.id)
    return { label: artifact?.title ?? '', icon: kindIcon[artifact?.kind ?? 'document'] }
  }
  const title = threadById.value.get(view.threadId)?.title ?? ''
  const label =
    view.kind === 'turn' ? `${title} · ${t('changes.turn')}` : t('view.changesOf', { title })
  return { label, icon: FileDiffIcon }
}

const labelOf = (key: PaneKey) =>
  lists[side.listOf(key) ?? '']?.label() ?? threadAt(key)?.title ?? viewLabel(key)?.label ?? ''

/** The artifacts and changes open in tabs. */
const openViews = computed(() =>
  side
    .groupsOf(state.side.root)
    .flatMap(side.tabsOf)
    .filter((key) => side.viewOf(key)),
)

// Every thread of the project is listed, open or not, so one can be dragged in from the
// thread list. The fixed tabs and their copies have separate dock keys.
const panels = computed<DockPanel[]>(() => [
  ...groups.value.flatMap((group) =>
    side.tabsOf(group).flatMap((key) => {
      const list = side.listOf(key)
      return list
        ? [{ value: key, label: lists[list]!.label(), closable: !side.isPinned(key) }]
        : []
    }),
  ),
  ...(state.detail?.threads ?? []).map((thread) => ({
    value: side.threadKey(thread.id),
    label: thread.title,
    closable: true,
  })),
  ...openViews.value.map((key) => ({ value: key, ...viewLabel(key)!, closable: true })),
])

const min = { width: `${MIN_WIDTH}px`, height: `${MIN_HEIGHT}px` } as const

const dock = useTemplateRef('dock')
watchEffect(() => (sideDock.value = dock.value))
onBeforeUnmount(() => (sideDock.value = null))

// The keyboard goes to the thread now in front of the group last used.
watch(activeThreadId, (threadId) => {
  if (threadId) lastPanel.value = threadId
})

const groups = computed(() => side.groupsOf(state.side.root))
const othersThan = (group: DockGroup) => groups.value.filter((item) => item.id !== group.id)

// Splitting halves the group: only offered while both halves still have room to be read.
const canSplit = (group: DockGroup, to: 'right' | 'bottom') =>
  group.tabs.length > 1 && !!dock.value?.canSplit(group.id, to)

function changed(root: DockNode) {
  setSide(side.updateDock(state.side, root))
}

function bring(group: DockGroup, key: side.ListKey) {
  setSide(side.move(state.side, key, group.id))
}
</script>

<template>
  <div class="side-layout">
    <DouDock
      ref="dock"
      :model-value="state.side.root"
      :active-group="state.side.focused"
      :panels="panels"
      :min="min"
      size="sm"
      class="side-dock"
      @update:model-value="changed"
      @update:active-group="(id) => setSide(side.focus(state.side, id))"
      @remove="(key) => closePane(key as PaneKey)"
    >
      <template #label="{ value }">
        <DouTooltip v-if="side.isPinned(value)" :content="labelOf(value)">
          <span class="pane-list-label">
            <component :is="lists[value]!.icon" aria-hidden="true" />
            <span class="pane-list-name">{{ labelOf(value) }}</span>
          </span>
        </DouTooltip>
        <span v-else-if="side.listOf(value)" class="pane-thread-label">
          <component :is="lists[side.listOf(value)!]!.icon" aria-hidden="true" />
          <span class="pane-thread-title">{{ labelOf(value as PaneKey) }}</span>
        </span>
        <span v-else-if="threadAt(value as PaneKey)" class="pane-thread-label">
          <StageIcon
            :stage="threadAt(value as PaneKey)!.stage"
            :pull-request-state="threadAt(value as PaneKey)!.pullRequest?.state"
          />
          <span class="pane-thread-title">{{ threadAt(value as PaneKey)!.title }}</span>
        </span>
        <template v-else>{{ labelOf(value as PaneKey) }}</template>
      </template>
      <template #default="{ value }">
        <div
          v-if="side.listOf(value)"
          class="pane-list"
          :class="{ 'pane-flush': side.listOf(value) === 'plan' }"
        >
          <PlanPanel v-if="side.listOf(value) === 'plan'" />
          <ThreadList v-else-if="side.listOf(value) === 'threads'" />
          <ArtifactList v-else />
        </div>
        <ConversationPanel
          v-else-if="side.threadOf(value as PaneKey)"
          :thread-id="side.threadOf(value as PaneKey)!"
          tabbed
        />
        <OpenedView
          v-else-if="openedViewOf(value as PaneKey)"
          :view="openedViewOf(value as PaneKey)!"
          place="side"
        />
      </template>
      <template #extra="{ group, active }">
        <DouTooltip
          v-if="active !== undefined && side.listOf(active) === 'plan' && state.plan?.nodes.length"
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
        <ThreadTools
          :thread="threadAt(active as PaneKey)"
          :label="t(threadAt(active as PaneKey) ? 'thread.more' : 'side.more')"
        >
          <DouDropdownItem
            v-if="canSplit(group, 'right')"
            value="split-right"
            @click="splitPane(active as PaneKey, 'horizontal')"
          >
            <template #prefix><LayoutColumnsIcon /></template>
            {{ t('side.splitRight') }}
          </DouDropdownItem>
          <DouDropdownItem
            v-if="canSplit(group, 'bottom')"
            value="split-down"
            @click="splitPane(active as PaneKey, 'vertical')"
          >
            <template #prefix><LayoutRowsIcon /></template>
            {{ t('side.splitDown') }}
          </DouDropdownItem>
          <DouDropdownSub v-if="othersThan(group).length" :title="t('side.moveTo')">
            <DouDropdownItem
              v-for="other in othersThan(group)"
              :key="other.id"
              :value="other.id"
              @click="setSide(side.move(state.side, active as PaneKey, other.id))"
            >
              {{ t('side.groupOf', { label: labelOf(side.activeOf(other)) }) }}
            </DouDropdownItem>
          </DouDropdownSub>
          <DouDropdownSub :title="t('side.open')">
            <DouDropdownItem
              v-for="key in side.LISTS"
              :key="key"
              :value="key"
              :disabled="group.tabs.some((tab) => side.listOf(tab) === key)"
              @click="bring(group, key)"
            >
              <template #prefix><component :is="lists[key]!.icon" /></template>
              {{ lists[key]!.label() }}
            </DouDropdownItem>
          </DouDropdownSub>
          <DouDropdownItem
            v-if="group.tabs.some((key) => key !== active && !side.isPinned(key))"
            value="close-others"
            divided
            @click="setSide(side.closeOthers(state.side, active as PaneKey))"
          >
            {{ t('side.closeOthers') }}
          </DouDropdownItem>
        </ThreadTools>
      </template>
    </DouDock>
  </div>
</template>

<style scoped>
.side-layout {
  height: 100%;
  min-height: 0;
  min-width: 0;
}

/* The groups sit flush with the side's edges and a conversation brings its own room, as
   the lists do theirs. */
.side-dock {
  --dou-dock-group-padding-x: 0px;
  --dou-dock-header-padding-x: 8px;
  --dou-dock-group-padding-bottom: 0px;
  --dou-dock-panel-gap: 0px;
}

.pane-list {
  box-sizing: border-box;
  height: 100%;
  padding: 8px 8px 8px 16px;
}

/* The plan's canvas fills its pane. */
.pane-flush {
  padding: 0;
}

.pane-list-label {
  position: relative;
  display: inline-flex;
  align-items: center;
  font-size: 16px;
}

.pane-list-name {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

.pane-thread-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

/* Trimmed to its letters, so the stage icon centres on them; see ThreadRow. Clipped
   sideways only: CJK letters reach below the trimmed baseline. */
.pane-thread-title {
  max-width: 16em;
  overflow-x: clip;
  text-overflow: ellipsis;
  white-space: nowrap;
  text-box: trim-both cap alphabetic;
}
</style>
