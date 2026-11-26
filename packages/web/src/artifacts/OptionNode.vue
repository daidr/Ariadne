<script setup lang="ts">
import { useFlowEditor, type NodeViewProps } from '@dou-ui/flow'
import { CheckCircleIcon } from '@dou-ui/icons'
import { DouButton, DouTag, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import {
  optionGroupId,
  optionPickMode,
  resolveOptionNoteLayout,
  type DecisionGroupData,
} from './decision-groups'
import type { OptionNodeData } from './option-node'

/** An option's frame: its title, optional recommendation badge, wrapped note, and pick action. */
const props = defineProps<NodeViewProps<OptionNodeData>>()
const { doc, transact } = useFlowEditor()

const byId = computed(() => new Map(doc.value.nodes.map((node) => [node.id, node])))
const groupId = computed(() => optionGroupId(props.node, byId.value))
const groupConfirmed = computed(() => {
  if (!groupId.value) return false
  const data = byId.value.get(groupId.value)?.data as DecisionGroupData | undefined
  return data?.confirmed === true
})

const minChildOffset = computed<number | null>(() => {
  let minOffset = Number.POSITIVE_INFINITY
  for (const node of doc.value.nodes) {
    if (node.parent !== props.node.id) continue
    const offset = node.y - props.node.y
    if (offset < minOffset) minOffset = offset
  }
  return Number.isFinite(minOffset) ? minOffset : null
})

const noteLayout = computed(() =>
  resolveOptionNoteLayout(props.data.note, props.node.height ?? 280, minChildOffset.value),
)

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
    <div
      class="option-head"
      :class="{
        'has-block-note': noteLayout.mode === 'block',
        'is-compact-note': noteLayout.mode === 'compact',
      }"
    >
      <div v-if="noteLayout.mode === 'compact'" class="option-head-main">
        <div class="option-title">
          <DouText weight="medium" size="sm" truncate class="option-title-text" :title="data.title">
            {{ data.title }}
          </DouText>
          <DouTag v-if="data.recommended" size="sm" tone="neutral" class="option-recommended-badge">
            {{ t('artifact.option.recommended') }}
          </DouTag>
        </div>
        <DouText
          variant="tertiary"
          size="sm"
          truncate
          class="option-note-compact"
          :title="data.note"
        >
          {{ data.note }}
        </DouText>
      </div>

      <div v-else class="option-title">
        <DouText weight="medium" truncate class="option-title-text" :title="data.title">
          {{ data.title }}
        </DouText>
        <DouTag v-if="data.recommended" size="sm" tone="neutral" class="option-recommended-badge">
          {{ t('artifact.option.recommended') }}
        </DouTag>
      </div>

      <DouButton
        size="sm"
        class="option-pick"
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

    <div v-if="noteLayout.mode === 'block'" class="option-note">
      <DouText
        as="div"
        variant="tertiary"
        size="sm"
        :truncate="noteLayout.maxLines"
        class="option-note-text"
        :title="data.note"
      >
        {{ data.note }}
      </DouText>
    </div>
  </div>
</template>

<style scoped>
.option {
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  width: 100%;
  height: 100%;
  overflow: hidden;
  border-radius: calc(var(--dou-flow-editor-node-radius, var(--dou-radius-container)) - 1px);
  transition: opacity 0.15s ease;
}

.option.is-chosen {
  background: color-mix(in oklab, var(--dou-color-primary) 4%, transparent);
  box-shadow: inset 0 0 0 2px var(--dou-color-primary);
}

.option.is-confirmed-dim {
  opacity: 0.62;
}

.option-head {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  box-sizing: border-box;
  min-width: 0;
  height: 40px;
  padding: 0 8px 0 12px;
  overflow: hidden;
}

.option-head.has-block-note {
  height: 36px;
  padding: 4px 8px 0 12px;
}

.option-head.is-compact-note {
  padding: 2px 8px 3px 12px;
}

.option-head-main {
  display: flex;
  flex: 1 1 0;
  flex-direction: column;
  justify-content: center;
  gap: 1px;
  min-width: 0;
  overflow: hidden;
}

.option-title {
  display: flex;
  flex: 1 1 0;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
}

.option-head-main > .option-title {
  flex: none;
  min-height: 19px;
  line-height: 18px;
}

.option-title-text {
  flex: 0 1 auto;
  min-width: 0;
}

.option-recommended-badge {
  flex: none;
}

.option-pick {
  flex: none;
  flex-shrink: 0;
  white-space: nowrap;
}

.option-note-compact {
  display: block;
  min-width: 0;
  font-size: 12px;
  line-height: 15px;
}

.option-note {
  display: flex;
  flex: none;
  flex-direction: column;
  box-sizing: border-box;
  width: 100%;
  padding: 2px 12px 8px;
  overflow: hidden;
}

.option-note-text {
  min-width: 0;
  line-height: 1.45;
  overflow-wrap: anywhere;
  word-break: break-word;
}
</style>
