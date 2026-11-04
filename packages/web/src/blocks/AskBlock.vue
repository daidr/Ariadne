<script setup lang="ts">
import type { AskBlock, BlockAnswer, FormBlock, FormValue } from '@ariadne/core'
import {
  DouButton,
  DouChatConfirm,
  DouChatSuggestions,
  DouCodeBlock,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputNumber,
  DouSelect,
  DouSwitch,
  DouText,
  DouTextarea,
  type ChatConfirmDecision,
  type ChatSuggestion,
} from 'dou-ui'
import { CheckIcon } from '@dou-ui/icons'
import { computed, reactive, ref, watch } from 'vue'
import { t } from '../i18n'
import { answerAttention } from '../store'

/**
 * A choice, confirmation or form an agent (or the system) asks the user, the same in the
 * conversation and under "needs me". Once answered it shows the answer in place.
 */
const props = defineProps<{
  attentionId: string
  block: AskBlock
  answer: BlockAnswer | null
}>()

const sending = ref(false)
async function send(answer: BlockAnswer) {
  sending.value = true
  try {
    await answerAttention(props.attentionId, answer)
  } finally {
    sending.value = false
  }
}

// A choice: one card per option, the recommended one marked.
const suggestions = computed<ChatSuggestion[]>(() =>
  props.block.type === 'choice'
    ? props.block.options.map((option, index) => ({
        value: String(index),
        label: option.label,
        description: option.consequence,
        disabled: sending.value,
      }))
    : [],
)
const chosen = computed(() =>
  props.block.type === 'choice' && props.answer?.type === 'choice'
    ? (props.block.options[props.answer.choice]?.label ?? null)
    : null,
)

const confirmStatus = computed(() =>
  props.answer?.type === 'confirm' ? (props.answer.approved ? 'approved' : 'denied') : 'pending',
)
function decide(decision: ChatConfirmDecision) {
  if (!sending.value) void send({ type: 'confirm', approved: decision !== 'deny' })
}

// A form: its values start from the defaults, or show what was submitted.
const form = computed(() => (props.block.type === 'form' ? props.block : null))
const values = reactive<Record<string, FormValue>>({})
watch(
  () => [props.block, props.answer] as const,
  () => {
    for (const key of Object.keys(values)) delete values[key]
    if (!form.value) return
    const given = props.answer?.type === 'form' ? props.answer.values : null
    for (const field of form.value.fields) {
      const value = given ? given[field.key] : field.default
      values[field.key] = Array.isArray(value) ? [...value] : (value ?? null)
    }
  },
  { immediate: true },
)
const options = (field: FormBlock['fields'][number]) =>
  field.options.map((option) => ({ label: option.label, value: option.value }))
const text = (key: string) => (typeof values[key] === 'string' ? (values[key] as string) : '')
const numberOf = (key: string) => (typeof values[key] === 'number' ? (values[key] as number) : null)
function submit() {
  void send({ type: 'form', values: { ...values } })
}
</script>

<template>
  <div class="ask-block">
    <template v-if="block.type === 'choice'">
      <template v-if="answer">
        <DouText weight="medium" class="ask-title">{{ block.question }}</DouText>
        <DouText variant="secondary" size="sm" class="ask-answer">
          <CheckIcon />{{ t('block.chosen', { label: chosen ?? '' }) }}
        </DouText>
      </template>
      <DouChatSuggestions
        v-else
        :items="suggestions"
        :title="block.question"
        variant="card"
        @select="(value) => send({ type: 'choice', choice: Number(value) })"
      >
        <template #item="{ item }">
          <span class="ask-option">
            <DouText weight="medium" size="sm">
              {{ item.label }}
              <DouText
                v-if="Number(item.value) === block.recommended"
                tone="primary"
                size="sm"
                class="ask-recommended"
              >
                {{ t('block.recommended') }}
              </DouText>
            </DouText>
            <DouText variant="secondary" size="sm">{{ item.description }}</DouText>
          </span>
        </template>
      </DouChatSuggestions>
    </template>

    <DouChatConfirm
      v-else-if="block.type === 'confirm'"
      :title="block.title"
      :description="block.description ?? undefined"
      :tone="block.tone"
      :status="confirmStatus"
      :approve-text="block.approveText ?? undefined"
      :deny-text="block.denyText ?? undefined"
      @decide="decide"
    >
      <DouCodeBlock v-if="block.detail" :code="block.detail" wrap :max-lines="12" />
    </DouChatConfirm>

    <template v-else-if="form">
      <DouText weight="medium" class="ask-title">{{ form.title }}</DouText>
      <DouText v-if="form.description" variant="secondary" size="sm" class="ask-title">
        {{ form.description }}
      </DouText>
      <DouForm :model="values" :readonly="!!answer" :disabled="sending" size="sm" @submit="submit">
        <DouFormItem
          v-for="field in form.fields"
          :key="field.key"
          :label="field.label"
          :prop="field.key"
          :required="field.required"
        >
          <DouTextarea
            v-if="field.kind === 'textarea'"
            :model-value="text(field.key)"
            :placeholder="field.placeholder ?? undefined"
            autosize
            @update:model-value="(value) => (values[field.key] = value)"
          />
          <DouInputNumber
            v-else-if="field.kind === 'number'"
            :model-value="numberOf(field.key)"
            :min="field.min ?? undefined"
            :max="field.max ?? undefined"
            :placeholder="field.placeholder ?? undefined"
            @update:model-value="(value) => (values[field.key] = value ?? null)"
          />
          <DouSelect
            v-else-if="field.kind === 'select' || field.kind === 'multiselect'"
            v-model="values[field.key] as never"
            :options="options(field)"
            :multiple="field.kind === 'multiselect'"
            :placeholder="field.placeholder ?? undefined"
            clearable
          />
          <DouSwitch
            v-else-if="field.kind === 'switch'"
            :model-value="values[field.key] === true"
            @update:model-value="(value) => (values[field.key] = value)"
          />
          <DouInput
            v-else
            :model-value="text(field.key)"
            :placeholder="field.placeholder ?? undefined"
            @update:model-value="(value) => (values[field.key] = value)"
          />
        </DouFormItem>
        <div class="ask-actions">
          <DouText v-if="answer" variant="secondary" size="sm" class="ask-answer">
            <CheckIcon />{{ t('block.submitted') }}
          </DouText>
          <DouButton v-else native-type="submit" size="sm" tone="primary" :loading="sending">
            {{ form.submitText ?? t('block.submit') }}
          </DouButton>
        </div>
      </DouForm>
    </template>
  </div>
</template>

<style scoped>
.ask-block {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.ask-title {
  white-space: pre-wrap;
}

.ask-answer {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.ask-actions {
  display: flex;
  justify-content: flex-end;
}

.ask-option {
  display: flex;
  flex-direction: column;
  gap: 2px;
  text-align: start;
}

.ask-recommended {
  margin-inline-start: 6px;
}
</style>
