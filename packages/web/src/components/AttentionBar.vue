<script setup lang="ts">
import { DouBadge, DouButton, DouEmpty, DouPopover, DouText } from 'dou-ui'
import { BellIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { answerAttention, state } from '../store'

// The server builds the list; the badge is its length, so the two cannot disagree.
const items = computed(() => state.detail?.needsMe ?? [])
const count = computed(() => items.value.length)
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
        <template v-for="item in items">
          <div v-if="item.kind === 'attention'" :key="item.attention.id" class="attention">
            <DouText v-if="item.thread" variant="secondary" size="sm" truncate>
              {{ item.thread.title }}
            </DouText>
            <DouText weight="medium">{{ item.attention.question }}</DouText>
            <div class="attention-options">
              <DouButton
                v-for="(option, index) in item.attention.options"
                :key="index"
                size="sm"
                :variant="index === item.attention.recommended ? 'solid' : 'outline'"
                :tone="index === item.attention.recommended ? 'primary' : 'neutral'"
                :title="option.consequence"
                @click="answerAttention(item.attention.id, index)"
              >
                {{ option.label }}
              </DouButton>
            </div>
          </div>
          <div v-else :key="item.thread.id" class="attention">
            <DouText weight="medium">{{ item.thread.title }}</DouText>
            <DouText variant="secondary" size="sm">
              {{ t(`thread.stage.${item.thread.stage}`) }} · {{ item.thread.lastSummary }}
            </DouText>
          </div>
        </template>
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
