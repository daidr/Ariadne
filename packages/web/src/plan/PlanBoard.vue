<script setup lang="ts">
import { DouBadge, DouButton, DouCard, DouTag, DouText } from 'dou-ui'
import type { PlanNodeStatus, PlanView } from '@ariadne/core'
import { computed } from 'vue'
import PullRequestTag from '../components/PullRequestTag.vue'
import { t } from '../i18n'
import { openThread, state } from '../store'
import StageLabel from '../components/StageLabel.vue'

// The plan as columns by status: the same nodes as the graph, read from left to right.
// Statuses come from the threads, so the board is read-only; nodes are changed in the chat.
const props = defineProps<{ plan: PlanView; compact?: boolean }>()
const selected = defineModel<string | null>('selected', { default: null })

const COLUMNS: PlanNodeStatus[] = ['none', 'process', 'wait', 'error', 'paused', 'finish']
const tones: Record<PlanNodeStatus, 'neutral' | 'primary' | 'warning' | 'danger' | 'success'> = {
  none: 'neutral',
  process: 'primary',
  wait: 'warning',
  error: 'danger',
  paused: 'neutral',
  finish: 'success',
}

// Stacked in the side panel, where empty columns would only take room.
const columns = computed(() =>
  COLUMNS.map((status) => ({
    status,
    nodes: props.plan.nodes.filter((node) => node.status === status),
  })).filter((column) => !props.compact || column.nodes.length),
)
const threadsOf = (nodeId: string) =>
  (state.detail?.threads ?? []).filter((thread) => thread.nodeId === nodeId)

function show(threadId: string) {
  void openThread(threadId)
}
</script>

<template>
  <div class="board" :class="{ 'board-compact': compact }">
    <section
      v-for="column in columns"
      :key="column.status"
      class="board-column"
      :aria-label="t(`plan.board.${column.status}`)"
    >
      <header class="board-column-head">
        <DouText size="sm" weight="medium">{{ t(`plan.board.${column.status}`) }}</DouText>
        <DouBadge :value="column.nodes.length" :tone="tones[column.status]" />
      </header>
      <div class="board-cards">
        <DouCard
          v-for="node in column.nodes"
          :key="node.id"
          padding="sm"
          interactive
          :class="{ 'board-card-selected': selected === node.id }"
          @click="selected = selected === node.id ? null : node.id"
        >
          <div class="board-card">
            <div class="board-card-title">
              <DouText size="sm" weight="medium">{{ node.title }}</DouText>
              <DouTag v-if="node.kind !== 'task'" size="sm">{{
                t(`plan.kind.${node.kind}`)
              }}</DouTag>
            </div>
            <ul v-if="threadsOf(node.id).length" class="board-threads">
              <li v-for="thread in threadsOf(node.id)" :key="thread.id" class="board-thread">
                <DouButton
                  size="sm"
                  variant="ghost"
                  class="board-thread-title"
                  :title="thread.title"
                  @click.stop="show(thread.id)"
                >
                  {{ thread.title }}
                </DouButton>
                <StageLabel :stage="thread.stage" />
                <PullRequestTag :thread="thread" />
              </li>
            </ul>
            <DouText v-else variant="tertiary" size="sm">{{ t('plan.board.noThreads') }}</DouText>
          </div>
        </DouCard>
        <DouText v-if="!column.nodes.length" variant="tertiary" size="sm" class="board-empty">
          —
        </DouText>
      </div>
    </section>
  </div>
</template>

<style scoped>
.board {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: minmax(200px, 1fr);
  gap: 12px;
  height: 100%;
  min-width: 0;
  padding: 12px 16px;
  overflow-x: auto;
}

.board-compact {
  grid-auto-flow: row;
  grid-auto-columns: auto;
  height: auto;
  padding: 0;
  overflow: visible;
}

.board-column {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
}

.board-column-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.board-cards {
  display: flex;
  flex-direction: column;
  gap: 8px;
  overflow-y: auto;
}

.board-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.board-card-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}

.board-card-selected {
  outline: 2px solid var(--dou-color-primary);
  outline-offset: -1px;
}

.board-threads {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.board-thread {
  display: flex;
  align-items: center;
  gap: 4px;
}

.board-thread-title {
  flex: 1 1 0;
  min-width: 0;
  justify-content: flex-start;
  margin-inline-start: -8px;
}

.board-empty {
  padding: 4px 0;
}
</style>
