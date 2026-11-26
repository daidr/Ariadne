<script setup lang="ts">
import { useFlowEditor, type NodeViewProps } from '@dou-ui/flow'
import { CheckCircleIcon, ChevronDownIcon } from '@dou-ui/icons'
import { DouTag, DouText } from 'dou-ui'
import { computed, nextTick, ref, useTemplateRef } from 'vue'
import { t } from '../i18n'
import { groupOptionsMap, isDecisionGroupNode, type DecisionGroupData } from './decision-groups'
import type { OptionNodeData } from './option-node'

/**
 * A group frame on the whiteboard. Plain user groups keep the standard collapse & rename
 * behavior; decision groups additionally show round, multi-select, rationale note, and
 * selection/confirmed status.
 */
const props = defineProps<NodeViewProps<DecisionGroupData>>()
const { doc, messages, announce } = useFlowEditor()

const editing = ref(false)
const draft = ref('')
const input = useTemplateRef<HTMLInputElement>('input')

const groupState = computed(() => {
  const { optionsByGroup } = groupOptionsMap(doc.value)
  const decision = isDecisionGroupNode(props.node, optionsByGroup)
  const options = optionsByGroup.get(props.node.id) ?? []
  let picked = 0
  let recommended = 0
  for (const opt of options) {
    const data = opt.data as OptionNodeData | undefined
    if (data?.chosen) picked++
    if (data?.recommended) recommended++
  }
  return {
    decision,
    picked,
    recommended,
    total: options.length,
  }
})

function edit() {
  if (props.readonly) return
  draft.value = props.data.label ?? ''
  editing.value = true
  void nextTick(() => {
    input.value?.focus()
    input.value?.select()
  })
}

function finish(save: boolean) {
  if (!editing.value) return
  editing.value = false
  if (save && draft.value.trim() !== (props.data.label ?? '')) {
    props.updateData({ label: draft.value.trim() })
  }
}

function toggle() {
  const collapsed = !props.data.collapsed
  props.updateData({ collapsed: collapsed || undefined })
  const msg = messages.value
  const label = props.data.label || msg.group
  announce((collapsed ? msg.collapsed : msg.expanded).replace('{group}', label))
}

function onKeydown(event: KeyboardEvent) {
  if (event.isComposing) return
  if (event.key === 'Enter') finish(true)
  else if (event.key === 'Escape') finish(false)
  else return
  event.preventDefault()
  event.stopPropagation()
}
</script>

<template>
  <div
    class="dou-flow-group-node decision-group"
    :class="{
      'is-collapsed': data.collapsed,
      'is-decision': groupState.decision,
      'is-confirmed': !!data.confirmed,
      'has-note': !!data.note,
    }"
  >
    <div class="dou-flow-group-node__head decision-group-head">
      <button
        type="button"
        class="dou-flow-group-node__toggle"
        :aria-label="data.collapsed ? messages.expand : messages.collapse"
        :aria-expanded="!data.collapsed"
        :disabled="readonly"
        @pointerdown.stop
        @click.stop="toggle"
      >
        <ChevronDownIcon />
      </button>
      <DouTag
        v-if="groupState.decision && data.round"
        size="sm"
        :tone="data.confirmed ? 'neutral' : 'primary'"
        class="decision-group-badge"
      >
        {{ t('artifact.group.round', { round: data.round }) }}
      </DouTag>
      <input
        v-if="editing"
        ref="input"
        v-model="draft"
        class="dou-flow-group-node__input"
        :aria-label="messages.groupName"
        @pointerdown.stop
        @keydown="onKeydown"
        @blur="finish(true)"
      />
      <span
        v-else
        class="dou-flow-group-node__title decision-group-title"
        :title="data.label || messages.group"
        @dblclick.stop="edit"
      >
        {{ data.label || messages.group }}
      </span>
      <DouTag
        v-if="groupState.decision && data.pick === 'many'"
        size="sm"
        tone="neutral"
        class="decision-group-badge"
      >
        {{ t('artifact.group.multi') }}
      </DouTag>
      <div v-if="groupState.decision" class="decision-group-status">
        <DouTag v-if="data.confirmed" size="sm" tone="neutral">
          {{ t('artifact.group.confirmed') }}
        </DouTag>
        <template v-else-if="groupState.picked > 0">
          <DouText tone="primary" size="sm" class="decision-group-picked">
            <CheckCircleIcon class="decision-group-status-icon" />
            {{ t('artifact.group.pickedCount', { count: groupState.picked }) }}
          </DouText>
        </template>
        <DouText v-else-if="groupState.recommended > 0" variant="tertiary" size="sm">
          {{ t('artifact.group.defaultRecommended') }}
        </DouText>
        <DouText v-else-if="groupState.total > 0" variant="tertiary" size="sm">
          {{ t('artifact.group.unpicked') }}
        </DouText>
      </div>
    </div>
    <div v-if="data.note && !data.collapsed" class="decision-group-note">
      <DouText variant="tertiary" size="sm" truncate :title="data.note">{{ data.note }}</DouText>
    </div>
  </div>
</template>

<style scoped>
.decision-group {
  border-radius: inherit;
}

.decision-group.is-confirmed {
  opacity: 0.72;
}

.decision-group-head {
  gap: 6px;
}

.decision-group-title {
  flex: 1;
  min-width: 0;
}

.decision-group-badge {
  flex: none;
}

.decision-group-status {
  display: inline-flex;
  align-items: center;
  flex: none;
  margin-inline-start: auto;
  white-space: nowrap;
}

.decision-group-picked {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-variant-numeric: tabular-nums;
}

.decision-group-status-icon {
  font-size: 13px;
}

.decision-group-note {
  min-width: 0;
  padding: 0 12px 6px 34px;
  overflow: hidden;
  line-height: 1.3;
}
</style>
