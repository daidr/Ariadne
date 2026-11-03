<script setup lang="ts">
import { DouAlert, DouButton, DouText } from 'dou-ui'
import { EyeIcon, EyeOffIcon, RotateCcwIcon } from '@dou-ui/icons'
import { computed, watch } from 'vue'
import { t, tm } from '../i18n'
import { answerAttention, dismissProposal, revertPlan, state } from '../store'
import { freshChanges } from './layout'
import { acknowledge, loadSeen, planUi, seenRevision } from './ui'

watch(
  () => state.plan,
  (plan) => plan && loadSeen(plan),
  { immediate: true },
)

const open = computed(() => state.plan?.proposals.filter((p) => p.status === 'open') ?? [])
const stale = computed(() => state.plan?.proposals.filter((p) => p.status === 'stale') ?? [])
// What the coordinator changed since the user last said "got it"; marked on the graph.
const fresh = computed(() =>
  state.plan
    ? freshChanges(state.plan, seenRevision(state.plan))
    : { revisions: [], nodes: new Set<string>(), truncated: false },
)
// The server draws each open proposal on the plan; `null` means it no longer fits.
const unfit = (proposal: { preview: unknown }) => proposal.preview === null

// A preview ends once its proposal is answered.
watch(open, (proposals) => {
  if (planUi.previewId && !proposals.some((p) => p.id === planUi.previewId)) {
    planUi.previewId = null
  }
})

function togglePreview(id: string) {
  planUi.previewId = planUi.previewId === id ? null : id
}
</script>

<template>
  <div v-if="fresh.revisions.length || open.length || stale.length" class="plan-change">
    <DouAlert
      v-for="proposal in open"
      :key="proposal.id"
      tone="warning"
      :title="t('plan.proposalTitle', { reason: proposal.reason })"
    >
      <ul class="plan-ops">
        <li v-for="(line, index) in proposal.changes" :key="index">{{ tm(line) }}</li>
      </ul>
      <DouText v-if="unfit(proposal)" size="sm" variant="secondary">
        {{ t('plan.preview.stale') }}
      </DouText>
      <DouText v-else-if="planUi.previewId === proposal.id" size="sm" variant="secondary">
        {{ t('plan.preview.on') }}
      </DouText>
      <div class="plan-actions">
        <DouButton
          v-if="!unfit(proposal)"
          size="sm"
          variant="ghost"
          :aria-pressed="planUi.previewId === proposal.id"
          @click="togglePreview(proposal.id)"
        >
          <template #leading>
            <EyeOffIcon v-if="planUi.previewId === proposal.id" />
            <EyeIcon v-else />
          </template>
          {{ planUi.previewId === proposal.id ? t('plan.preview.hide') : t('plan.preview.show') }}
        </DouButton>
        <DouButton size="sm" variant="outline" @click="answerAttention(proposal.attentionId, 1)">
          {{ t('plan.confirm.keep') }}
        </DouButton>
        <DouButton size="sm" tone="primary" @click="answerAttention(proposal.attentionId, 0)">
          {{ t('plan.confirm.apply') }}
        </DouButton>
      </div>
    </DouAlert>
    <DouAlert v-for="proposal in stale" :key="proposal.id" tone="danger" role="alert">
      {{ t('plan.stale', { reason: proposal.reason }) }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="dismissProposal(proposal.id)">
          {{ t('plan.dismiss') }}
        </DouButton>
      </template>
    </DouAlert>
    <DouAlert
      v-if="fresh.revisions.length && state.plan"
      tone="primary"
      :title="t('plan.fresh.title', { count: fresh.revisions.length })"
    >
      <DouText v-if="fresh.truncated" size="sm" variant="secondary">
        {{ t('plan.fresh.truncated') }}
      </DouText>
      <ul class="plan-ops">
        <li v-for="revision in fresh.revisions" :key="revision.id" class="plan-fresh">
          <span>{{ revision.reason }}</span>
          <DouButton
            size="sm"
            variant="ghost"
            :aria-label="t('plan.revert')"
            :title="t('plan.revert')"
            @click="revertPlan(revision.id)"
          >
            <RotateCcwIcon />
          </DouButton>
        </li>
      </ul>
      <div class="plan-actions">
        <DouButton size="sm" variant="outline" @click="state.plan && acknowledge(state.plan)">
          {{ t('plan.fresh.ack') }}
        </DouButton>
      </div>
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

.plan-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px;
  margin-top: 8px;
}

.plan-fresh > span {
  margin-inline-end: 4px;
}
</style>
