<script setup lang="ts">
import type { ChangeTotals } from '@ariadne/core'
import { FileDiffIcon } from '@dou-ui/icons'
import { DouButton, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/**
 * What a thread has changed so far, in lines added and removed: plain where it sits in a
 * row that opens the thread, a button that opens the changes elsewhere.
 */
const props = defineProps<{
  totals: ChangeTotals
  threadId: string
  /** A button that opens the changes. */
  button?: boolean
  size?: 'xs' | 'sm'
}>()

const title = computed(() => t('changes.files', { count: props.totals.files }))
function open(event: MouseEvent) {
  event.stopPropagation()
  openView({ kind: 'changes', threadId: props.threadId })
}
</script>

<template>
  <DouButton
    v-if="button"
    variant="ghost"
    :size="size ?? 'sm'"
    :title="t('changes.openOf', { files: title })"
    class="change-totals"
    @click="open"
    @pointerdown.stop
  >
    <template #leading><FileDiffIcon /></template>
    <span class="totals-added">+{{ totals.added }}</span>
    <span class="totals-removed">−{{ totals.removed }}</span>
  </DouButton>
  <DouText v-else size="sm" variant="tertiary" class="change-totals" :title="title">
    <span class="totals-added">+{{ totals.added }}</span>
    <span class="totals-removed">−{{ totals.removed }}</span>
  </DouText>
</template>

<style scoped>
.change-totals {
  flex: none;
  font-variant-numeric: tabular-nums;
}

.totals-added {
  color: var(--dou-color-success);
}

.totals-removed {
  margin-left: 4px;
  color: var(--dou-color-danger);
}
</style>
