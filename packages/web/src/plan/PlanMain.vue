<script setup lang="ts">
import { DouButton, DouEmpty, DouTag, DouText, DouTimeline, DouTimelineItem } from 'dou-ui'
import { ChevronLeftIcon, LayoutIcon, PencilIcon, PlusIcon } from '@dou-ui/icons'
import type { PlanOp } from '@ariadne/core'
import { computed, ref } from 'vue'
import { locale, t, tm } from '../i18n'
import { editPlan, openMain, state } from '../store'
import { reasonOf } from './edit'
import PlanChangeBar from './PlanChangeBar.vue'
import PlanGraph from './PlanGraph.vue'
import PlanNodeDetail from './PlanNodeDetail.vue'
import PlanUndoButton from './PlanUndoButton.vue'
import { clearPositions } from './ui'

const selected = ref<string | null>(null)
const editing = ref(false)
const canEdit = computed(() => !!state.plan)

async function addNode() {
  const op: PlanOp = {
    op: 'add',
    id: `nod_${crypto.randomUUID()}`,
    kind: 'task',
    title: t('plan.newNode'),
    description: '',
    acceptance: '',
  }
  if (
    (await editPlan(
      reasonOf([op], () => op.title, t),
      [op],
    )) === 'ok'
  )
    selected.value = op.id
}

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
      <div class="plan-main-tools">
        <template v-if="canEdit && editing">
          <DouButton size="sm" variant="ghost" @click="addNode">
            <template #leading><PlusIcon /></template>
            {{ t('plan.edit.add') }}
          </DouButton>
          <DouButton
            size="sm"
            variant="ghost"
            @click="state.plan && clearPositions(state.plan.projectId)"
          >
            <template #leading><LayoutIcon /></template>
            {{ t('plan.edit.relayout') }}
          </DouButton>
        </template>
        <DouButton
          v-if="canEdit"
          size="sm"
          :variant="editing ? 'solid' : 'outline'"
          :tone="editing ? 'primary' : 'neutral'"
          :aria-pressed="editing"
          @click="editing = !editing"
        >
          <template #leading><PencilIcon /></template>
          {{ editing ? t('plan.edit.done') : t('plan.edit.start') }}
        </DouButton>
      </div>
    </div>
    <div class="plan-main-body">
      <div class="plan-main-graph">
        <PlanGraph
          v-if="state.plan?.nodes.length"
          v-model:selected="selected"
          :plan="state.plan"
          :editable="editing"
        />
        <DouEmpty v-else :description="t('plan.empty')" />
      </div>
      <aside class="plan-main-side">
        <DouText v-if="canEdit && editing" size="sm" variant="secondary">
          {{ t('plan.edit.hint') }}
        </DouText>
        <PlanChangeBar />
        <PlanNodeDetail
          v-if="node"
          :node="node"
          :editable="canEdit && editing"
          @removed="selected = null"
        />
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
              <PlanUndoButton v-if="!revision.revertOf" :revision="revision" />
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
