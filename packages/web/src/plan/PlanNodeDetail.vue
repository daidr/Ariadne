<script setup lang="ts">
import type { PlanNodeView } from '@ariadne/core'
import { DouButton, DouTag, DouText } from 'dou-ui'
import { t } from '../i18n'
import { stageTone } from '../stage'
import { openThread } from '../store'

defineProps<{ node: PlanNodeView }>()
</script>

<template>
  <section class="node-detail" :aria-label="node.title">
    <div class="node-detail-head">
      <DouText weight="medium">{{ node.title }}</DouText>
      <DouText variant="tertiary" size="sm">{{ t(`plan.kind.${node.kind}`) }}</DouText>
    </div>
    <DouText v-if="node.description" size="sm" class="node-detail-text">
      {{ node.description }}
    </DouText>
    <template v-if="node.acceptance">
      <DouText variant="secondary" size="sm">{{ t('plan.acceptance') }}</DouText>
      <DouText size="sm" class="node-detail-text">{{ node.acceptance }}</DouText>
    </template>
    <DouText variant="secondary" size="sm">{{ t('plan.threads') }}</DouText>
    <div v-if="node.threads.length" class="node-detail-threads">
      <DouButton
        v-for="thread in node.threads"
        :key="thread.id"
        size="sm"
        variant="ghost"
        @click="openThread(thread.id)"
      >
        {{ thread.title }}
        <template #trailing>
          <DouTag size="sm" :tone="stageTone[thread.stage]">
            {{ t(`thread.stage.${thread.stage}`) }}
          </DouTag>
        </template>
      </DouButton>
    </div>
    <DouText v-else variant="tertiary" size="sm">{{ t('plan.noThread') }}</DouText>
  </section>
</template>

<style scoped>
.node-detail {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.node-detail-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.node-detail-text {
  white-space: pre-wrap;
}

.node-detail-threads {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
}
</style>
