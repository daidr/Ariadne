<script setup lang="ts">
import { useFlowEditor, type NodeViewProps } from '@dou-ui/flow'
import { CheckCircleIcon } from '@dou-ui/icons'
import { DouButton, DouText } from 'dou-ui'
import { t } from '../i18n'
import type { OptionNodeData } from './option-node'

/** An option's frame: its title, and the button that picks it. */
const props = defineProps<NodeViewProps<OptionNodeData>>()
const { doc, transact } = useFlowEditor()

function pick() {
  const chosen = !props.data.chosen
  transact((tr) => {
    tr.updateData(props.node.id, { chosen: chosen || undefined })
    // Picking one option unpicks the others, unless they let more than one be picked.
    if (!chosen || props.data.pick === 'many') return
    for (const other of doc.value.nodes) {
      if (other.id !== props.node.id && other.type === 'option' && other.data?.chosen) {
        tr.updateData(other.id, { chosen: undefined })
      }
    }
  })
}
</script>

<template>
  <div class="option" :class="{ 'is-chosen': data.chosen }">
    <div class="option-head">
      <div class="option-title">
        <DouText weight="medium" truncate>{{ data.title }}</DouText>
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
}

.option.is-chosen {
  box-shadow: inset 0 0 0 2px var(--dou-color-primary);
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
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}
</style>
