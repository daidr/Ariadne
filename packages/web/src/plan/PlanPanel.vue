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
      <PlanNodeDetail v-if="node" :node="node" />
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
  gap: 8px;
}

.plan-panel-graph {
  height: max(240px, 55dvh);
}
</style>
