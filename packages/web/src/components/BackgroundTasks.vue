<script setup lang="ts">
import type { BackgroundView } from '@ariadne/core'
import { DouButton, DouPopover, DouStatusDot } from 'dou-ui'
import { computed, shallowRef } from 'vue'
import { t } from '../i18n'
import BackgroundCommand from './BackgroundCommand.vue'
import { firstLine } from './command-text'

const props = defineProps<{ conversationId: string; commands: BackgroundView[] }>()
const open = shallowRef(false)
const selected = shallowRef('')
const command = computed(
  () => props.commands.find((item) => item.id === selected.value) ?? props.commands[0],
)
</script>

<template>
  <DouPopover
    v-model:open="open"
    trigger="click"
    placement="top-start"
    :width="520"
    :title="t('command.tasks')"
    style="box-shadow: 0 2px 8px rgb(0 0 0 / 8%)"
  >
    <DouButton variant="outline" size="sm" class="task-pill" :aria-expanded="open">
      <template #leading><DouStatusDot tone="success" /></template>
      {{ t('command.runningCount', { count: commands.length }) }}
    </DouButton>
    <template #content>
      <div v-if="open" class="task-details">
        <div v-if="commands.length > 1" class="task-list" :aria-label="t('command.tasks')">
          <DouButton
            v-for="item in commands"
            :key="item.id"
            size="sm"
            :variant="item.id === command?.id ? 'soft' : 'ghost'"
            :aria-pressed="item.id === command?.id"
            :title="item.command"
            class="task-choice"
            @click="selected = item.id"
          >
            <template #leading><DouStatusDot tone="success" /></template>
            <span class="task-name">{{ firstLine(item.command) }}</span>
          </DouButton>
        </div>
        <BackgroundCommand
          v-if="command"
          :key="command.id"
          :thread-id="conversationId"
          :command="command"
        />
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.task-pill {
  border-radius: 999px;
  flex: none;
  box-shadow: none;
  border-color: var(--dou-color-border);
}
.task-details {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}
.task-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 144px;
  overflow: auto;
}
.task-choice {
  justify-content: flex-start;
  width: 100%;
}
.task-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
