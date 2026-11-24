<script setup lang="ts">
import {
  DouButton,
  DouCollapse,
  DouCollapseItem,
  DouEmpty,
  DouTag,
  DouText,
  DouTimeline,
  DouTimelineItem,
} from 'dou-ui'
import { ChevronLeftIcon, EyeIcon, EyeOffIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { locale, t } from '../i18n'
import { backMain, state } from '../store'
import { partitionPlanNodes } from './helpers'
import PlanChangeBar from './PlanChangeBar.vue'
import PlanGraph from './PlanGraph.vue'
import PlanNodeDetail from './PlanNodeDetail.vue'
import { activePreview, showingCompleted, toggleShowCompleted } from './ui'

const selected = ref<string | null>(null)
const historyOpen = ref<string[]>([])

const preview = computed(() => activePreview(state.plan))
const summary = computed(() =>
  state.plan
    ? partitionPlanNodes(state.plan.nodes)
    : { active: [], completedIds: new Set<string>() },
)
const completedCount = computed(() => summary.value.completedIds.size)
const showCompleted = computed(() => (state.plan ? showingCompleted(state.plan.projectId) : false))
const allHiddenAsCompleted = computed(
  () =>
    !!state.plan?.nodes.length &&
    summary.value.active.length === 0 &&
    !showCompleted.value &&
    !preview.value,
)
const visibleIds = computed(() => {
  if (!state.plan) return new Set<string>()
  if (preview.value || showCompleted.value) {
    return new Set(state.plan.nodes.map((n) => n.id))
  }
  return new Set(summary.value.active.map((n) => n.id))
})

watch([() => state.plan?.projectId, visibleIds], ([projectId, visible], [prevProjectId]) => {
  if (projectId !== prevProjectId || (selected.value && !visible.has(selected.value))) {
    selected.value = null
  }
})

const node = computed(() =>
  selected.value && visibleIds.value.has(selected.value)
    ? (state.plan?.nodes.find((n) => n.id === selected.value) ?? null)
    : null,
)
const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div class="plan-main">
    <div class="plan-main-header">
      <DouButton variant="ghost" size="sm" @click="backMain">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('view.back') }}
      </DouButton>
      <DouText weight="medium">{{ t('plan.title') }}</DouText>
      <div class="plan-main-tools">
        <DouButton
          v-if="state.plan && completedCount > 0"
          size="sm"
          :variant="showCompleted ? 'soft' : 'ghost'"
          :aria-pressed="showCompleted"
          @click="toggleShowCompleted(state.plan.projectId)"
        >
          <template #leading>
            <EyeOffIcon v-if="showCompleted" />
            <EyeIcon v-else />
          </template>
          {{
            showCompleted
              ? t('plan.hideCompleted', { count: completedCount })
              : t('plan.showCompleted', { count: completedCount })
          }}
        </DouButton>
      </div>
    </div>
    <div class="plan-main-body">
      <div class="plan-main-graph">
        <DouEmpty v-if="!state.plan?.nodes.length" :description="t('plan.empty')" />
        <DouEmpty v-else-if="allHiddenAsCompleted" :description="t('plan.allCompleted')" />
        <PlanGraph v-else v-model:selected="selected" :plan="state.plan" />
      </div>
      <aside class="plan-main-side">
        <PlanChangeBar />
        <PlanNodeDetail v-if="node" :node="node" />
        <!-- The history is there when looked for; the graph and the node come first. -->
        <DouCollapse v-model="historyOpen" class="plan-main-history">
          <DouCollapseItem value="history" :title="t('plan.history')">
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
                      revision.author === 'user'
                        ? t('artifact.by.user')
                        : t('artifact.by.coordinator')
                    }}
                  </DouText>
                  <DouTag v-if="revision.revertedBy" size="sm">{{ t('plan.reverted') }}</DouTag>
                </div>
              </DouTimelineItem>
            </DouTimeline>
            <DouText v-else variant="tertiary" size="sm">—</DouText>
          </DouCollapseItem>
        </DouCollapse>
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
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.plan-main-tools {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-inline-start: auto;
}

.plan-main-body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.plan-main-graph {
  flex: 1;
  min-width: 0;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  grid-template-rows: minmax(0, 1fr);
  padding: 12px;
}

.plan-main-side {
  display: flex;
  flex-direction: column;
  gap: 16px;
  width: 320px;
  padding: 12px 16px max(12px, var(--ariadne-safe-bottom));
  overflow-y: auto;
  border-left: 1px solid var(--dou-color-border-subtle);
}

@media (max-width: 760px) {
  .plan-main-body {
    flex-direction: column;
  }

  .plan-main-side {
    box-sizing: border-box;
    width: 100%;
    max-height: 45%;
    border-left: 0;
    border-top: 1px solid var(--dou-color-border-subtle);
  }
}

.plan-main-revision {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
