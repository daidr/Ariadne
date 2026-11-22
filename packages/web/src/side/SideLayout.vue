<script setup lang="ts">
import {
  DouButton,
  DouDock,
  DouDropdownItem,
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
import PinnedSideTabs from './PinnedSideTabs.vue'
import * as side from './layout'
import type { PaneKey } from './layout'
import { MIN_HEIGHT, MIN_WIDTH } from './sizes'

const props = defineProps<{ compact: boolean; active: boolean }>()

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
const threadAt = (key: PaneKey | undefined) => {
  const id = key && side.threadOf(key)
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

/** The non-artifact views (and any open artifact missing from `state.artifacts`) open in tabs. */
const openViews = computed(() =>
  side
    .groupsOf(state.side.root)
    .flatMap(side.tabsOf)
    .filter((key) => {
      const view = side.viewOf(key)
      return !!view && (view.kind !== 'artifact' || !artifactById.value.has(view.id))
    }),
)

// Every thread and artifact of the project is listed, open or not, so one can be dragged in
// from a list, card or message link. The fixed tabs and their copies have separate dock keys.
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
  ...state.artifacts.map((artifact) => ({
    value: side.artifactKey(artifact.id),
    label: artifact.title,
    icon: kindIcon[artifact.kind],
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
const compactGroup = computed(() => side.compactGroup(state.side))
const pinned = computed(() =>
  side.pinnedTabsOf(groups.value[0]!).map((key) => ({
    key,
    label: lists[key]!.label(),
    icon: lists[key]!.icon,
  })),
)
const othersThan = (group: DockGroup) => groups.value.filter((item) => item.id !== group.id)

// Splitting halves the group: only offered while both halves still have room to be read.
const canSplit = (group: DockGroup, to: 'right' | 'bottom') =>
  !props.compact && group.tabs.length > 1 && !!dock.value?.canSplit(group.id, to)

function changed(root: DockNode) {
  if (props.compact) {
    if (root.type !== 'group') return
    const active = side.activeOf(root)
    if (active !== side.activeOf(compactGroup.value)) setSide(side.activate(state.side, active))
    return
  }
  setSide(side.updateDock(state.side, root))
}

function focusGroup(id: string | undefined) {
  if (!props.compact) setSide(side.focus(state.side, id))
}

function bring(group: DockGroup, key: side.ListKey) {
  setSide(side.move(state.side, key, group.id))
}

function dragPinned(event: PointerEvent, key: side.ListKey) {
  dock.value?.startDrop(event, { value: key })
}
</script>

<template>
  <div class="side-layout">
    <DouDock
      ref="dock"
      :model-value="compact ? compactGroup : state.side.root"
      :active-group="compact ? compactGroup.id : state.side.focused"
      :panels="panels"
      :min="min"
      :draggable="!compact"
      size="sm"
      class="side-dock"
      @update:model-value="changed"
      @update:active-group="focusGroup"
      @remove="(key) => closePane(key as PaneKey, compact)"
    >
      <template #label="{ value }">
        <span v-if="side.isPinned(value)" class="pane-pinned-label" :data-pinned-label="value">
          {{ labelOf(value) }}
        </span>
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
          :active="props.active && (!compact || value === compactGroup.active)"
          tabbed
        />
        <OpenedView
          v-else-if="openedViewOf(value as PaneKey)"
          :view="openedViewOf(value as PaneKey)!"
          place="side"
        />
      </template>
      <template #extra="{ group, active }">
        <PinnedSideTabs
          v-if="group.id === groups[0]?.id"
          :tabs="pinned"
          :active="active"
          :drag-across-groups="!compact"
          @select="(key) => setSide(side.activate(state.side, key))"
          @reorder="(key, index) => setSide(side.reorderPinned(state.side, key, index))"
          @drag="dragPinned"
        />
        <DouTooltip
          v-if="
            !compact &&
            active !== undefined &&
            side.listOf(active) === 'plan' &&
            state.plan?.nodes.length
          "
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
          v-if="!compact || threadAt(active as PaneKey)"
          :thread="threadAt(active as PaneKey)"
          :label="t(threadAt(active as PaneKey) ? 'thread.more' : 'side.more')"
        >
          <template v-if="!compact">
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
          </template>
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
  padding: 8px 8px calc(8px + var(--ariadne-safe-bottom)) 16px;
}

/* The plan's canvas fills its pane. */
.pane-flush {
  padding: 0;
}

.side-dock :deep(.dou-tabs__tab:has(.pane-pinned-label)) {
  display: none;
}

/* Keep the scrolling tab list at full tab height even when every tab inside it is hidden
   (pinned) or empty, so the drop insertion marker is not clipped vertically. */
.side-dock :deep(.dou-tabs__list) {
  min-height: var(--dou-tabs-height);
}

.side-dock :deep(.side-pinned-host) {
  display: flex;
  flex: none;
  align-self: stretch;
}

.side-dock :deep(.dou-tabs__header:has(.side-pinned-tabs)) {
  container-type: inline-size;
}

/* The fixed strip owns insertion feedback for its tabs in the first group. */
.side-dock :deep(.dou-tabs__header:has(.side-pinned-tabs.is-dragging-pin) .dou-tabs__marker) {
  visibility: hidden;
}

@container (width < 240px) {
  .side-dock :deep(.dou-tabs__header:has(.side-pinned-tabs) > .dou-tabs__scroll) {
    display: none;
  }
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
