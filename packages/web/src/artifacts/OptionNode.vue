<script setup lang="ts">
import { useFlowEditor, type NodeViewProps } from '@dou-ui/flow'
import { CheckCircleIcon } from '@dou-ui/icons'
import { DouButton, DouTag, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { optionGroupId, optionPickMode, type DecisionGroupData } from './decision-groups'
import type { OptionNodeData } from './option-node'

/** An option's frame: its title, optional recommendation badge, and pick action. */
const props = defineProps<NodeViewProps<OptionNodeData>>()
const { doc, transact } = useFlowEditor()

const byId = computed(() => new Map(doc.value.nodes.map((node) => [node.id, node])))
const groupId = computed(() => optionGroupId(props.node, byId.value))
const groupConfirmed = computed(() => {
  if (!groupId.value) return false
  const data = byId.value.get(groupId.value)?.data as DecisionGroupData | undefined
  return data?.confirmed === true
})

function pick() {
  const chosen = !props.data.chosen
  const nodesById = byId.value
  const myGroup = optionGroupId(props.node, nodesById)
  const pickMode = optionPickMode(props.node, nodesById)
  transact((tr) => {
    tr.updateData(props.node.id, { chosen: chosen || undefined })
    // Single-select options mutually exclude only within the same decision group (or
    // among ungrouped options when no group is assigned).
    if (!chosen || pickMode === 'many') return
    for (const other of doc.value.nodes) {
      if (other.id === props.node.id || other.type !== 'option' || !other.data?.chosen) continue
      if (optionGroupId(other, nodesById) !== myGroup) continue
      tr.updateData(other.id, { chosen: undefined })
    }
  })
}
</script>

<template>
  <div
    class="option"
    :class="{
      'is-chosen': data.chosen,
      'is-recommended': data.recommended && !data.chosen,
      'is-confirmed-dim': groupConfirmed && !data.chosen,
    }"
  >
    <div class="option-head">
      <div class="option-title">
        <DouText weight="medium" truncate>{{ data.title }}</DouText>
        <DouTag v-if="data.recommended" size="sm" tone="neutral" class="option-recommended-badge">
          {{ t('artifact.option.recommended') }}
        </DouTag>
        <DouText v-if="data.note" variant="tertiary" size="sm" truncate>{{ data.note }}</DouText>
      </div>
      <DouButton
        size="sm"
        :variant="data.chosen ? 'soft' : 'ghost'"
        :tone="data.chosen ? 'primary' : 'neutral'"
        :disabled="readonly"
        :aria-pressed="!!data.chosen"
        @pointerdown.stop
        @click.stop="pick"
      >
        <template v-if="data.chosen" #leading><CheckCircleIcon /></template>
        {{ data.chosen ? t('artifact.option.chosen') : t('artifact.option.choose') }}
      </DouButton>
    </div>
  </div>
</template>

<style scoped>
.option {
  height: 100%;
  border-radius: inherit;
  transition: opacity 0.15s ease;
}

.option.is-chosen {
  box-shadow: inset 0 0 0 2px var(--dou-color-primary);
}

.option.is-confirmed-dim {
  opacity: 0.62;
}

.option-head {
  display: flex;
  align-items: center;
  gap: 8px;
  box-sizing: border-box;
  height: 40px;
  padding: 0 8px 0 12px;
}

.option-title {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.option-recommended-badge {
  flex: none;
}
</style>
