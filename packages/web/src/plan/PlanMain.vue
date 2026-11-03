<script setup lang="ts">
import { DouButton, DouEmpty, DouTag, DouText, DouTimeline, DouTimelineItem } from 'dou-ui'
import { ChevronLeftIcon, RotateCcwIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { locale, t } from '../i18n'
import { openMain, revertPlan, state } from '../store'
import PlanChangeBar from './PlanChangeBar.vue'
import PlanGraph from './PlanGraph.vue'
import PlanNodeDetail from './PlanNodeDetail.vue'

const selected = ref<string | null>(null)
const node = computed(() => state.plan?.nodes.find((n) => n.id === selected.value) ?? null)
const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div class="plan-main">
    <div class="plan-main-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'conversation' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('artifact.back') }}
      </DouButton>
      <DouText weight="medium">{{ t('plan.title') }}</DouText>
    </div>
    <div class="plan-main-body">
      <div class="plan-main-graph">
        <PlanGraph v-if="state.plan?.nodes.length" v-model:selected="selected" :plan="state.plan" />
        <DouEmpty v-else :description="t('plan.empty')" />
      </div>
      <aside class="plan-main-side">
        <PlanChangeBar />
        <PlanNodeDetail v-if="node" :node="node" />
        <DouText variant="secondary" size="sm">{{ t('plan.history') }}</DouText>
        <DouTimeline v-if="state.plan?.revisions.length">
          <DouTimelineItem
            v-for="revision in state.plan.revisions"
            :key="revision.id"
            :title="revision.reason"
            :time="time(revision.createdAt)"
            :tone="revision.author === 'user' ? 'primary' : 'neutral'"
          >
            <div class="plan-main-revision">
              <DouText variant="tertiary" size="sm">
                {{
                  revision.author === 'user' ? t('artifact.by.user') : t('artifact.by.coordinator')
                }}
              </DouText>
              <DouTag v-if="revision.revertedBy" size="sm">{{ t('plan.reverted') }}</DouTag>
              <DouButton
                v-else-if="!revision.revertOf"
                size="sm"
                variant="ghost"
                @click="revertPlan(revision.id)"
              >
                <template #leading><RotateCcwIcon /></template>
                {{ t('plan.revert') }}
              </DouButton>
            </div>
          </DouTimelineItem>
        </DouTimeline>
        <DouText v-else variant="tertiary" size="sm">—</DouText>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.plan-main {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.plan-main-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.plan-main-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.plan-main-graph {
  flex: 1;
  min-width: 0;
  display: grid;
}

.plan-main-side {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: 280px;
  padding: 12px 16px;
  overflow-y: auto;
  border-left: 1px solid var(--dou-color-border-subtle);
}

.plan-main-revision {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
