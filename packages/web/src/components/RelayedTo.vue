<script setup lang="ts">
import type { MessageRelay } from '@ariadne/core'
import { ChevronRightIcon } from '@dou-ui/icons'
import { DouLink, DouPopover } from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { openMessage, state } from '../store'
import StageIcon from './StageIcon.vue'

/**
 * Under a message of the user's: which threads the coordinator passed it to. One opens that
 * thread at the message; several are listed to pick from.
 */
const props = defineProps<{ relays: MessageRelay[] }>()

const targets = computed(() =>
  props.relays.flatMap((relay) => {
    const thread = state.detail?.threads.find((item) => item.id === relay.threadId)
    return thread ? [{ relay, thread }] : []
  }),
)
const listing = ref(false)

function open(relay: MessageRelay) {
  listing.value = false
  void openMessage(relay.threadId, relay.messageId)
}
</script>

<template>
  <div v-if="targets.length" class="relayed-to">
    <DouLink v-if="targets.length === 1" underline="hover" @click="open(targets[0]!.relay)">
      {{ t('message.relayedTo') }}
      <template #trailing><ChevronRightIcon /></template>
    </DouLink>
    <DouPopover v-else v-model:open="listing" trigger="click" placement="bottom-end">
      <DouLink underline="hover">
        {{ t('message.relayedToMany', { count: targets.length }) }}
        <template #trailing><ChevronRightIcon /></template>
      </DouLink>
      <template #content>
        <div class="relayed-list">
          <DouLink
            v-for="{ relay, thread } in targets"
            :key="relay.messageId"
            underline="hover"
            @click="open(relay)"
          >
            <template #leading><StageIcon :stage="thread.stage" /></template>
            {{ thread.title }}
          </DouLink>
        </div>
      </template>
    </DouPopover>
  </div>
</template>

<style scoped>
.relayed-to {
  font-size: var(--dou-font-size-sm);
}
.relayed-list {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
}
</style>
