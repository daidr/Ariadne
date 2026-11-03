<script setup lang="ts">
import { DouAlert, DouButton } from 'dou-ui'
import { RotateCcwIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { revertPlan, state } from '../store'
import { latestCoordinatorChange } from './layout'

// The newest change the coordinator made is highlighted on the graph and can be undone here.
const change = computed(() => latestCoordinatorChange(state.plan))
const proposals = computed(() => state.plan?.proposals.length ?? 0)
</script>

<template>
  <div v-if="change || proposals" class="plan-change">
    <DouAlert v-if="proposals" tone="warning">
      {{ t('plan.proposals', { count: proposals }) }}
    </DouAlert>
    <DouAlert v-if="change" tone="neutral">
      {{ t('plan.lastChange', { reason: change.reason }) }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="revertPlan(change.id)">
          <template #leading><RotateCcwIcon /></template>
          {{ t('plan.revert') }}
        </DouButton>
      </template>
    </DouAlert>
  </div>
</template>

<style scoped>
.plan-change {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
