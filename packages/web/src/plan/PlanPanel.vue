<script setup lang="ts">
import { DouEmpty } from 'dou-ui'
import { computed, defineAsyncComponent, ref } from 'vue'
import { t } from '../i18n'
import { state } from '../store'
import PlanChangeBar from './PlanChangeBar.vue'
import PlanNodeDetail from './PlanNodeDetail.vue'

const PlanGraph = defineAsyncComponent(() => import('./PlanGraph.vue'))

const selected = ref<string | null>(null)
const node = computed(() => state.plan?.nodes.find((n) => n.id === selected.value) ?? null)
</script>

<template>
  <div class="plan-panel">
    <template v-if="state.plan?.nodes.length">
      <PlanChangeBar />
      <div class="plan-panel-graph">
        <PlanGraph v-model:selected="selected" :plan="state.plan" compact />
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

.plan-panel > .plan-panel-detail {
  margin: 0;
  padding: 8px 8px 8px 16px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

/* The graph takes whatever height the panel has left. */
.plan-panel-graph {
  flex: 1;
  min-height: 240px;
}
</style>
