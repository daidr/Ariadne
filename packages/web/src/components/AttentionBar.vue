<script setup lang="ts">
import { DouBadge, DouButton, DouEmpty, DouPopover, DouText } from 'dou-ui'
import { needsUser } from '@ariadne/core/stage'
import { BellIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { answerAttention, state } from '../store'

const attentions = computed(() => state.detail?.attentions ?? [])
const waitingThreads = computed(() =>
  (state.detail?.threads ?? []).filter(
    (thread) => needsUser(thread.stage) && !attentions.value.some((a) => a.threadId === thread.id),
  ),
)
// The badge equals the length of the list it opens.
const count = computed(() => attentions.value.length + waitingThreads.value.length)
</script>

<template>
  <DouPopover :title="t('attention.title')" :width="360" placement="bottom-end">
    <DouButton variant="ghost" size="sm">
      <template #leading><BellIcon /></template>
      {{ t('attention.title') }}
      <DouBadge v-if="count" :value="count" tone="warning" />
    </DouButton>
    <template #content>
      <div v-if="count" class="attention-list">
        <div v-for="attention in attentions" :key="attention.id" class="attention">
          <DouText weight="medium">{{ attention.question }}</DouText>
          <div class="attention-options">
            <DouButton
              v-for="(option, index) in attention.options"
              :key="index"
              size="sm"
              :variant="index === attention.recommended ? 'solid' : 'outline'"
              :tone="index === attention.recommended ? 'primary' : 'neutral'"
              :title="option.consequence"
              @click="answerAttention(attention.id, index)"
            >
              {{ option.label }}
            </DouButton>
          </div>
        </div>
        <div v-for="thread in waitingThreads" :key="thread.id" class="attention">
          <DouText weight="medium">{{ thread.title }}</DouText>
          <DouText variant="secondary" size="sm">
            {{ t(`thread.stage.${thread.stage}`) }} · {{ thread.lastSummary }}
          </DouText>
        </div>
      </div>
      <DouEmpty v-else size="sm" :description="t('attention.empty')" />
    </template>
  </DouPopover>
</template>

<style scoped>
.attention-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.attention {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.attention-options {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
</style>
