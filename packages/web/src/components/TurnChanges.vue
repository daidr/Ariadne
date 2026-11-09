<script setup lang="ts">
import type { NoticeView } from '@ariadne/core'
import { FileDiffIcon } from '@dou-ui/icons'
import { DouButton } from 'dou-ui'
import { t } from '../i18n'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/** What a turn changed in the thread's workspace, under its reply; opens those changes. */
const props = defineProps<{ notice: Extract<NoticeView, { kind: 'turn-changes' }> }>()

function open() {
  const { turnId, from, to, files, total } = props.notice
  openView({
    kind: 'changes',
    threadId: props.notice.conversationId,
    turn: { turnId, from, to, files, total },
  })
}
</script>

<template>
  <DouButton variant="ghost" size="sm" :title="t('changes.turnOpen')" @click="open">
    <template #leading><FileDiffIcon /></template>
    {{ t('changes.files', { count: notice.total.files }) }}
    <span class="turn-added">+{{ notice.total.added }}</span>
    <span class="turn-removed">−{{ notice.total.removed }}</span>
  </DouButton>
</template>

<style scoped>
.turn-added {
  margin-left: 2px;
  color: var(--dou-color-success);
}

.turn-removed {
  margin-left: 4px;
  color: var(--dou-color-danger);
}
</style>
