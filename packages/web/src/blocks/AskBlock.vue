<script setup lang="ts">
import type { AskBlock, BlockAnswer, FormBlock, FormValue } from '@ariadne/core'
import {
  DouButton,
  DouChatConfirm,
  DouChatSuggestions,
  DouCodeBlock,
  DouDescriptions,
  DouDescriptionsItem,
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
 * conversation and under "needs me". Once answered it shows the answer in place, read-only;
 * once the agent took it back, only what it asked and why it was withdrawn.
 */
const props = defineProps<{
  attentionId: string
  block: AskBlock
  answer: BlockAnswer | null
  withdrawn?: string | null
}>()

const title = computed(() =>
  props.block.type === 'choice' ? props.block.question : props.block.title,
)

const sending = ref(false)
// What the user sent, until the stored answer arrives with the next refresh: without it
// the controls would come back for a moment after the request returns.
const sent = ref<BlockAnswer | null>(null)
const answered = computed(() => props.answer ?? sent.value)
async function send(given: BlockAnswer) {
  sending.value = true
  try {
    if (await answerAttention(props.attentionId, given)) sent.value = given
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
  props.block.type === 'choice' && answered.value?.type === 'choice'
    ? (props.block.options[answered.value.choice]?.label ?? null)
    : null,
)

const confirmStatus = computed(() => {
  if (answered.value?.type === 'confirm') return answered.value.approved ? 'approved' : 'denied'
  return props.withdrawn ? 'withdrawn' : 'waiting'
})
function decide(decision: ChatConfirmDecision) {
  if (!sending.value) void send({ type: 'confirm', approved: decision !== 'deny' })
}

// A form: its values start from the defaults; once submitted it lists what was sent.
const form = computed(() => (props.block.type === 'form' ? props.block : null))
const values = reactive<Record<string, FormValue>>({})
watch(
  () => props.block,
  () => {
    for (const key of Object.keys(values)) delete values[key]
    if (!form.value) return
    for (const field of form.value.fields) {
      const value = field.default
      values[field.key] = Array.isArray(value) ? [...value] : (value ?? null)
    }
  },
  { immediate: true },
)
const submitted = computed(() => (answered.value?.type === 'form' ? answered.value.values : null))
/** A submitted value as the user would say it: option labels, yes or no. */
function shown(field: FormBlock['fields'][number], value: FormValue | undefined): string {
  const label = (v: string) => field.options.find((option) => option.value === v)?.label ?? v
  if (value === null || value === undefined || value === '') return t('block.blank')
  if (Array.isArray(value)) return value.length ? value.map(label).join('、') : t('block.blank')
  if (typeof value === 'boolean') return t(value ? 'block.yes' : 'block.no')
  return field.kind === 'select' ? label(String(value)) : String(value)
}
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
    <!-- A confirm says so itself; a choice or form keeps only what it asked. -->
    <template v-if="withdrawn && block.type !== 'confirm'">
      <DouText weight="medium" class="ask-title">{{ title }}</DouText>
      <DouText variant="tertiary" size="sm">{{
        t('block.withdrawn', { reason: withdrawn })
      }}</DouText>
    </template>
    <template v-else-if="block.type === 'choice'">
      <template v-if="answered">
        <DouText weight="medium" class="ask-title">{{ block.question }}</DouText>
        <div class="ask-chosen">
          <DouText variant="secondary" size="sm" class="ask-answer">
            <CheckIcon />{{ t('block.chosen', { label: chosen ?? '' }) }}
          </DouText>
          <!-- What can still be done about the answer, e.g. taking it back. -->
          <slot name="after" />
        </div>
      </template>
      <DouChatSuggestions
        v-else
        :items="suggestions"
        variant="card"
        @select="(value) => send({ type: 'choice', choice: Number(value) })"
      >
        <!-- Its own lines kept: a question may say where and why under what it asks. -->
        <template #title>
          <span class="ask-title">{{ block.question }}</span>
        </template>
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
      :description="(withdrawn ?? block.description) || undefined"
      :tone="block.tone"
      :status="confirmStatus"
      :approve-text="block.approveText ?? undefined"
      :deny-text="block.denyText ?? undefined"
      @decide="decide"
    >
      <DouCodeBlock v-if="block.detail && !withdrawn" :code="block.detail" wrap :max-lines="12" />
    </DouChatConfirm>

    <template v-else-if="form && submitted">
      <div class="ask-head">
        <DouText weight="medium" class="ask-title">{{ form.title }}</DouText>
        <DouText variant="secondary" size="sm" class="ask-answer">
          <CheckIcon />{{ t('block.submitted') }}
        </DouText>
      </div>
      <DouDescriptions :columns="1" layout="horizontal" label-width="96px" class="ask-values">
        <DouDescriptionsItem v-for="field in form.fields" :key="field.key" :label="field.label">
          <DouText
            :variant="
              shown(field, submitted[field.key]) === t('block.blank') ? 'tertiary' : undefined
            "
            size="sm"
            class="ask-value"
          >
            {{ shown(field, submitted[field.key]) }}
          </DouText>
        </DouDescriptionsItem>
      </DouDescriptions>
    </template>

    <template v-else-if="form">
      <DouText weight="medium" class="ask-title">{{ form.title }}</DouText>
      <DouText v-if="form.description" variant="secondary" size="sm" class="ask-title">
        {{ form.description }}
      </DouText>
      <DouForm :model="values" :disabled="sending" size="sm" @submit="submit">
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
          <DouButton native-type="submit" size="sm" tone="primary" :loading="sending">
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

.ask-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}

.ask-value {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ask-answer {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.ask-chosen {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
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
