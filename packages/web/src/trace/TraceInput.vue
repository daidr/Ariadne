<script setup lang="ts">
import type { TraceInput } from '@ariadne/core'
import { DouAlert, DouCollapse, DouCollapseItem, DouSpinner, DouText } from 'dou-ui'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import { t, tm } from '../i18n'

/** The whole request of one model call, fetched when the user opens it. */
const props = defineProps<{ conversationId: string; seq: number }>()

const input = ref<TraceInput | null>(null)
const problem = ref<string | null>(null)
// New messages start open: they are what this call added.
const open = ref<string[]>([])

onMounted(async () => {
  try {
    input.value = (await api.getTraceInput(props.conversationId, props.seq)).input
    open.value = input.value.messages
      .map((_, index) => `m${index}`)
      .filter((_, index) => index >= Math.max(input.value?.newFrom ?? 0, 1))
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  }
})

type Message = TraceInput['messages'][number]

function titleOf(message: Message, index: number) {
  const role = t(`trace.role.${message.role}`)
  const isNew =
    !!input.value &&
    (index === 0 ? input.value.systemChanged : index >= Math.max(input.value.newFrom, 1))
  const calls =
    message.role === 'assistant' && message.toolCalls?.length
      ? ` · ${message.toolCalls.map((call) => call.name).join('、')}`
      : ''
  const error = message.role === 'tool' && message.isError ? ` · ${t('trace.toolError')}` : ''
  const mark = isNew ? ` · ${t(index === 0 ? 'trace.changed' : 'trace.new')}` : ''
  return `${index + 1}. ${role}${calls}${error}${mark}`
}

function bodyOf(message: Message) {
  if (message.role !== 'assistant' || !message.toolCalls?.length) return message.content
  const calls = message.toolCalls.map((call) => `${call.name}(${call.arguments})`).join('\n')
  return message.content ? `${message.content}\n\n${calls}` : calls
}

const tools = computed(() => JSON.stringify(input.value?.tools ?? [], null, 2))
</script>

<template>
  <div class="trace-input">
    <DouAlert v-if="problem" tone="danger">{{ problem }}</DouAlert>
    <DouSpinner v-else-if="!input" size="sm" />
    <template v-else>
      <DouText variant="tertiary" size="sm">
        {{
          t('trace.inputSummary', {
            count: input.messages.length,
            fresh: Math.max(0, input.messages.length - Math.max(input.newFrom, 1)),
            tools: input.tools.length,
          })
        }}
      </DouText>
      <DouCollapse v-model="open">
        <DouCollapseItem
          v-for="(message, index) in input.messages"
          :key="index"
          :value="`m${index}`"
          :title="titleOf(message, index)"
        >
          <pre class="trace-input-text">{{ bodyOf(message) }}</pre>
        </DouCollapseItem>
        <DouCollapseItem
          v-if="input.tools.length"
          value="tools"
          :title="t('trace.tools', { count: input.tools.length })"
        >
          <pre class="trace-input-text">{{ tools }}</pre>
        </DouCollapseItem>
      </DouCollapse>
    </template>
  </div>
</template>

<style scoped>
.trace-input {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-left: 12px;
  border-left: 2px solid var(--dou-color-border-subtle);
}

.trace-input-text {
  margin: 0;
  max-height: 480px;
  overflow: auto;
  font-family: var(--dou-font-family-mono);
  font-size: var(--dou-font-size-sm);
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
