<script setup lang="ts">
import type { TraceMessage, TraceRequestDetail } from '@ariadne/core'
import {
  DouChatAttachments,
  DouCollapse,
  DouCollapseItem,
  DouText,
  type ChatAttachment,
} from 'dou-ui'
import { computed, ref } from 'vue'
import { shownAttachments } from '../components/attachments'
import { t } from '../i18n'

/** The messages of one request, those new since the step before marked and open. */
const props = defineProps<{ detail: TraceRequestDetail }>()

const input = computed(() => props.detail)
const open = ref<string[]>(
  props.detail.messages
    .map((_, index) => `m${index}`)
    .filter((_, index) => index >= Math.max(props.detail.newFrom, 1)),
)

type Message = TraceMessage

function titleOf(message: Message, index: number) {
  const role = t(`trace.role.${message.role}`)
  const isNew = index === 0 ? input.value.systemChanged : index >= Math.max(input.value.newFrom, 1)
  const calls =
    message.role === 'assistant' && message.toolCalls?.length
      ? ` · ${message.toolCalls.map((call) => call.name).join('、')}`
      : ''
  const error = message.role === 'tool' && message.isError ? ` · ${t('trace.toolError')}` : ''
  const mark = isNew ? ` · ${t(index === 0 ? 'trace.changed' : 'trace.new')}` : ''
  return `${index + 1}. ${role}${calls}${error}${mark}`
}

function bodyOf(message: Message) {
  if (message.role !== 'assistant') return message.content
  const thought = (message.reasoning ?? [])
    .map((block) => (block.redacted ? t('trace.redacted') : block.text))
    .join('\n\n')
  const calls = (message.toolCalls ?? [])
    .map((call) => `${call.name}(${call.arguments})`)
    .join('\n')
  return [thought, message.content, calls].filter(Boolean).join('\n\n')
}

/** The images that went with it, by their files. */
const imagesOf = (message: Message) =>
  message.role === 'user' || message.role === 'tool' ? (message.images ?? []) : []

function openFile(file: ChatAttachment) {
  if (file.url) window.open(file.url, '_blank', 'noopener')
}

const tools = computed(() => JSON.stringify(input.value.tools, null, 2))
</script>

<template>
  <div class="trace-input">
    <template v-if="input">
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
          <DouChatAttachments
            v-if="imagesOf(message).length"
            :model-value="shownAttachments(imagesOf(message))"
            :removable="false"
            @open="openFile"
          />
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
