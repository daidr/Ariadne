<script setup lang="ts">
import type { FlowNode } from '@dou-ui/flow'
import { CheckCircleIcon, ChevronDownIcon } from '@dou-ui/icons'
import { DouText } from 'dou-ui'
import { inject } from 'vue'
import { t } from '../i18n'
import type { PlanGroupData } from './layout'
import { PLAN_GROUP_TOGGLE } from './ui'

const props = defineProps<{
  node: FlowNode
  data: PlanGroupData
}>()

const toggleGroup = inject(PLAN_GROUP_TOGGLE, null)

function toggle() {
  toggleGroup?.(props.node.id)
}
</script>

<template>
  <div
    class="dou-flow-group-node plan-group-node"
    :class="{
      'is-collapsed': data.collapsed,
      'plan-group-archived': data.archived,
      'plan-group-compact': data.compact,
    }"
  >
    <div class="dou-flow-group-node__head plan-group-head">
      <button
        type="button"
        class="dou-flow-group-node__toggle"
        :aria-label="data.collapsed ? t('plan.group.expand') : t('plan.group.collapse')"
        :aria-expanded="!data.collapsed"
        @click.stop="toggle"
      >
        <ChevronDownIcon />
      </button>
      <span class="dou-flow-group-node__title plan-group-title" :title="data.label">
        {{ data.label }}
      </span>
      <span class="plan-group-progress">
        <DouText
          v-if="data.allDone"
          tone="success"
          size="sm"
          class="plan-group-done-icon"
          aria-hidden="true"
        >
          <CheckCircleIcon />
        </DouText>
        <DouText
          :tone="data.allDone ? 'success' : 'neutral'"
          :variant="data.allDone ? 'default' : 'tertiary'"
          size="sm"
          class="plan-group-count"
        >
          {{ data.doneCount }}/{{ data.totalCount }}
        </DouText>
      </span>
    </div>
  </div>
</template>

<style scoped>
.plan-group-node {
  border-radius: inherit;
}

.plan-group-archived {
  opacity: 0.78;
}

.plan-group-head {
  gap: 6px;
}

.plan-group-title {
  flex: 1;
  min-width: 0;
}

.plan-group-progress {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex: none;
  margin-inline-start: auto;
  font-variant-numeric: tabular-nums;
}

.plan-group-done-icon {
  display: inline-flex;
  align-items: center;
  font-size: 13px;
}

.plan-group-count {
  line-height: 1;
}

.plan-group-compact .plan-group-head {
  height: 34px;
  padding: 0 10px 0 6px;
}
</style>
