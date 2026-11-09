<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouButton, DouDropdown, DouDropdownItem } from 'dou-ui'
import { ListTreeIcon, MoreHorizontalIcon } from '@dou-ui/icons'
import { t } from '../i18n'
import { openMain } from '../store'

/**
 * The "…" menu of a thread. In a side tab group it is the group's own menu, whose items
 * come in through the slot, so a thread has one menu, not two.
 */
defineProps<{ thread?: ThreadView | null; label: string }>()
</script>

<template>
  <DouDropdown placement="bottom-end">
    <DouButton variant="ghost" size="sm" :aria-label="label" :title="label">
      <MoreHorizontalIcon />
    </DouButton>
    <template #menu>
      <slot />
      <DouDropdownItem
        v-if="thread"
        value="trace"
        :divided="!!$slots.default"
        @click="openMain({ kind: 'trace', conversationId: thread.id })"
      >
        <template #prefix><ListTreeIcon /></template>
        {{ t('trace.open') }}
      </DouDropdownItem>
    </template>
  </DouDropdown>
</template>
