<script setup lang="ts">
import { DouAlert, DouButton, DouText } from 'dou-ui'
import { EyeIcon, EyeOffIcon } from '@dou-ui/icons'
import { computed, watch } from 'vue'
import { t, tm } from '../i18n'
import { answerAttention, dismissProposal, state } from '../store'
import { planUi } from './ui'

const open = computed(() => state.plan?.proposals.filter((p) => p.status === 'open') ?? [])
const stale = computed(() => state.plan?.proposals.filter((p) => p.status === 'stale') ?? [])
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
  <div v-if="open.length || stale.length" class="plan-change">
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
      <template #action>
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
        <DouButton
          size="sm"
          variant="outline"
          @click="answerAttention(proposal.attentionId, { type: 'confirm', approved: false })"
        >
          {{ t('plan.confirm.keep') }}
        </DouButton>
        <DouButton
          size="sm"
          tone="primary"
          @click="answerAttention(proposal.attentionId, { type: 'confirm', approved: true })"
        >
          {{ t('plan.confirm.apply') }}
        </DouButton>
      </template>
    </DouAlert>
    <DouAlert v-for="proposal in stale" :key="proposal.id" tone="danger" role="alert">
      {{ t('plan.stale', { reason: proposal.reason }) }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="dismissProposal(proposal.id)">
          {{ t('plan.dismiss') }}
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
