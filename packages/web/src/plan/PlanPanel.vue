<script setup lang="ts">
import { EyeIcon, EyeOffIcon } from '@dou-ui/icons'
import { DouButton, DouEmpty } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import { defineAsyncView } from '../components/async-view'
import { t } from '../i18n'
import { state } from '../store'
import { partitionPlanNodes } from './helpers'
import PlanChangeBar from './PlanChangeBar.vue'
import PlanNodeDetail from './PlanNodeDetail.vue'
import { activePreview, showingCompleted, toggleShowCompleted } from './ui'

const PlanGraph = defineAsyncView(() => import('./PlanGraph.vue'))

const selected = ref<string | null>(null)
const preview = computed(() => activePreview(state.plan))
const summary = computed(() =>
  state.plan
    ? partitionPlanNodes(state.plan.nodes)
    : { active: [], completedIds: new Set<string>() },
)
const completedCount = computed(() => summary.value.completedIds.size)
const showCompleted = computed(() => (state.plan ? showingCompleted(state.plan.projectId) : false))
const allHiddenAsCompleted = computed(
  () =>
    !!state.plan?.nodes.length &&
    summary.value.active.length === 0 &&
    !showCompleted.value &&
    !preview.value,
)
const visibleIds = computed(() => {
  if (!state.plan) return new Set<string>()
  if (preview.value || showCompleted.value) {
    return new Set(state.plan.nodes.map((n) => n.id))
  }
  return new Set(summary.value.active.map((n) => n.id))
})

watch([() => state.plan?.projectId, visibleIds], ([projectId, visible], [prevProjectId]) => {
  if (projectId !== prevProjectId || (selected.value && !visible.has(selected.value))) {
    selected.value = null
  }
})

const node = computed(() =>
  selected.value && visibleIds.value.has(selected.value)
    ? (state.plan?.nodes.find((n) => n.id === selected.value) ?? null)
    : null,
)
</script>

<template>
  <div class="plan-panel">
    <template v-if="state.plan?.nodes.length">
      <PlanChangeBar />
      <div v-if="completedCount > 0" class="plan-panel-bar">
        <DouButton
          size="xs"
          :variant="showCompleted ? 'soft' : 'ghost'"
          :aria-pressed="showCompleted"
          @click="toggleShowCompleted(state.plan.projectId)"
        >
          <template #leading>
            <EyeOffIcon v-if="showCompleted" />
            <EyeIcon v-else />
          </template>
          {{
            showCompleted
              ? t('plan.hideCompleted', { count: completedCount })
              : t('plan.showCompleted', { count: completedCount })
          }}
        </DouButton>
      </div>
      <div class="plan-panel-graph">
        <DouEmpty v-if="allHiddenAsCompleted" size="sm" :description="t('plan.allCompleted')" />
        <PlanGraph v-else v-model:selected="selected" :plan="state.plan" compact />
      </div>
      <div v-if="node" class="plan-panel-detail">
        <PlanNodeDetail :node="node" />
      </div>
    </template>
    <template v-else>
      <PlanChangeBar />
      <DouEmpty size="sm" :description="t('plan.empty')" />
    </template>
  </div>
</template>

<style scoped>
.plan-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
}

/* The canvas runs to the pane's edges; what is written around it keeps the pane's inset. */
.plan-panel > :not(.plan-panel-graph) {
  margin: 8px 8px 8px 16px;
}

.plan-panel > .plan-panel-bar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  margin: 4px 8px 0 16px;
}

.plan-panel > .plan-panel-detail {
  flex: none;
  max-height: 45%;
  overflow-y: auto;
  margin: 0;
  padding: 8px 8px max(8px, var(--ariadne-safe-bottom)) 16px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

/* The graph takes whatever height the panel has left. */
.plan-panel-graph {
  flex: 1;
  min-height: 240px;
}
</style>
