<script setup lang="ts">
import { DouAlert, DouButton } from 'dou-ui'
import { RotateCcwIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t, tm } from '../i18n'
import { answerAttention, revertPlan, state } from '../store'
import { latestCoordinatorChange } from './layout'

// The newest change the coordinator made is highlighted on the graph and can be undone here.
const change = computed(() => latestCoordinatorChange(state.plan))
const open = computed(() => state.plan?.proposals.filter((p) => p.status === 'open') ?? [])
const stale = computed(() => state.plan?.proposals.filter((p) => p.status === 'stale') ?? [])
</script>

<template>
  <div v-if="change || open.length || stale.length" class="plan-change">
    <DouAlert v-for="proposal in open" :key="proposal.id" tone="warning">
      {{ t('plan.proposalTitle', { reason: proposal.reason }) }}
      <ul class="plan-ops">
        <li v-for="(line, index) in proposal.changes" :key="index">{{ tm(line) }}</li>
      </ul>
      <template #action>
        <DouButton size="sm" variant="outline" @click="answerAttention(proposal.attentionId, 1)">
          {{ t('plan.confirm.keep') }}
        </DouButton>
        <DouButton size="sm" tone="primary" @click="answerAttention(proposal.attentionId, 0)">
          {{ t('plan.confirm.apply') }}
        </DouButton>
      </template>
    </DouAlert>
    <DouAlert v-for="proposal in stale" :key="proposal.id" tone="danger" role="alert">
      {{ t('plan.stale', { reason: proposal.reason }) }}
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

.plan-ops {
  margin: 4px 0 0;
  padding-left: 18px;
}
</style>
