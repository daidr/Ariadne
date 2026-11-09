<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouButton, DouDropdown, DouDropdownItem, DouInput, DouPopover } from 'dou-ui'
import { ListTreeIcon, MoreHorizontalIcon, PauseIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { t } from '../i18n'
import { openMain, stopThread } from '../store'

/**
 * The "…" menu of a thread: pausing it and its trace. In a side tab group it is the group's
 * own menu, whose items come in through the slot, so a thread has one menu, not two.
 */
const props = defineProps<{ thread?: ThreadView | null; label: string }>()

// The user stops a thread with a reason, the same way the coordinator does.
const stopOpen = ref(false)
const stopReason = ref('')
// In a tab group the thread changes under the menu: a reason written for one thread is
// never left open over another.
watch(
  () => props.thread?.id,
  () => {
    stopOpen.value = false
    stopReason.value = ''
  },
)
// Pausing the whole thread is rarer than stopping a turn: it sits in the menu.
const canPause = computed(() => {
  const current = props.thread
  if (!current) return false
  return current.stopped ? current.stopped.by !== 'user' : current.stage !== 'done'
})
async function stopNow(thread: ThreadView) {
  // Taking over without a reason keeps the coordinator's, which says why it stopped.
  const reason = stopReason.value.trim() || (thread.stopped ? '' : t('thread.stopDefaultReason'))
  await stopThread(thread.id, reason)
  stopOpen.value = false
  stopReason.value = ''
}
</script>

<template>
  <DouPopover
    v-model:open="stopOpen"
    trigger="manual"
    placement="bottom-end"
    :title="t(thread?.stopped ? 'thread.takeOverTitle' : 'thread.stopTitle')"
    :width="320"
  >
    <DouDropdown placement="bottom-end">
      <DouButton variant="ghost" size="sm" :aria-label="label" :title="label">
        <MoreHorizontalIcon />
      </DouButton>
      <template #menu>
        <slot />
        <template v-if="thread">
          <DouDropdownItem
            v-if="canPause"
            value="pause"
            :divided="!!$slots.default"
            @click="stopOpen = true"
          >
            <template #prefix><PauseIcon /></template>
            {{ t(thread.stopped ? 'thread.takeOver' : 'thread.stop') }}
          </DouDropdownItem>
          <DouDropdownItem
            value="trace"
            :divided="!!$slots.default && !canPause"
            @click="openMain({ kind: 'trace', conversationId: thread.id })"
          >
            <template #prefix><ListTreeIcon /></template>
            {{ t('trace.open') }}
          </DouDropdownItem>
        </template>
      </template>
    </DouDropdown>
    <template #content>
      <div v-if="thread" class="stop-form">
        <DouInput v-model="stopReason" :placeholder="t('thread.stopReasonPlaceholder')" />
        <DouButton size="sm" tone="primary" @click="stopNow(thread)">
          {{ t('thread.stopConfirm') }}
        </DouButton>
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.stop-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  align-items: flex-end;
}
</style>
