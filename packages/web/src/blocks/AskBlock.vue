<script setup lang="ts">
import type { AskBlock, BlockAnswer, FormBlock, FormValue } from '@ariadne/core'
import {
  DouButton,
  DouChatConfirm,
  DouChatQuestion,
  DouCodeBlock,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputNumber,
  DouRadio,
  DouRadioGroup,
  DouSelect,
  DouSwitch,
  DouText,
  DouTextarea,
  type ChatConfirmDecision,
  type ChatQuestionOption,
  type ChatQuestionStatus,
  type RadioValue,
} from 'dou-ui'
import { CheckIcon, ClipboardListIcon } from '@dou-ui/icons'
import { computed, inject, reactive, ref, shallowRef, useId, watch } from 'vue'
import { t } from '../i18n'
import { answerAttention } from '../store'
import { askDraftsKey, type AskDraft } from './ask-drafts'

/**
 * A choice, confirmation or form an agent (or the system) asks the user, the same in the
 * conversation and under "needs me". Once answered it shows the answer in place, read-only;
 * once the agent took it back, only what it asked and why it was withdrawn.
 */
const props = defineProps<{
  attentionId?: string
  block: AskBlock
  answer: BlockAnswer | null
  withdrawn?: string | null
}>()
defineSlots<{ after?: () => unknown }>()

const formTitleId = useId()

const sending = ref(false)
// What the user sent, until the stored answer arrives with the next refresh: without it
// the controls would come back for a moment after the request returns.
const sent = ref<BlockAnswer | null>(null)
const answered = computed(() => props.answer ?? sent.value)
async function send(given: BlockAnswer) {
  if (!props.attentionId || sending.value) return
  sending.value = true
  try {
    if (await answerAttention(props.attentionId, given)) sent.value = given
  } finally {
    sending.value = false
  }
}

const questionOptions = computed<ChatQuestionOption[]>(() =>
  props.block.type === 'choice'
    ? props.block.options.map((option, index) => ({
        value: index,
        label: option.label,
        description: option.consequence,
        disabled: sending.value || !props.attentionId,
      }))
    : [],
)
const questionAnswer = computed(() =>
  answered.value?.type === 'choice' ? answered.value.choice : undefined,
)
const questionStatus = computed<ChatQuestionStatus>(() => {
  if (props.withdrawn) return 'withdrawn'
  return questionAnswer.value === undefined ? 'waiting' : 'answered'
})

const confirmStatus = computed(() => {
  if (answered.value?.type === 'confirm') return answered.value.approved ? 'approved' : 'denied'
  return props.withdrawn ? 'withdrawn' : 'waiting'
})
const confirmDescription = computed(() => {
  if (props.block.type !== 'confirm') return undefined
  if (props.withdrawn) return props.withdrawn
  if (answered.value?.type === 'confirm' && answered.value.scope) {
    return t(answered.value.scope === 'project' ? 'reference.ask.project' : 'reference.ask.thread')
  }
  return props.block.description || undefined
})
function decide(decision: ChatConfirmDecision) {
  const approved = decision !== 'deny'
  if (approved && props.block.type === 'confirm' && props.block.allowProjectReference) {
    const scope = referenceScope.value === 'project' ? 'project' : 'thread'
    void send({ type: 'confirm', approved, scope })
  } else {
    void send({ type: 'confirm', approved })
  }
}

// A form: its values start from the defaults; once submitted it lists what was sent.
const form = computed(() => (props.block.type === 'form' ? props.block : null))
const drafts = inject(askDraftsKey, null)
const draft = shallowRef<AskDraft>(reactive({ scope: 'thread', values: {} }))
const values = computed(() => draft.value.values)
const referenceScope = computed<RadioValue>({
  get: () => draft.value.scope,
  set: (scope) => (draft.value.scope = scope),
})
watch(
  [() => props.attentionId, () => props.block, answered, () => props.withdrawn],
  ([id, block, answer, withdrawn]) => {
    const editable =
      id &&
      drafts &&
      !answer &&
      !withdrawn &&
      (block.type === 'form' || (block.type === 'confirm' && block.allowProjectReference))
    if (id && !editable) drafts?.delete(id)
    const existing = editable ? drafts.get(id) : undefined
    const fields: Record<string, FormValue> = {}
    for (const field of form.value?.fields ?? []) {
      const value =
        existing && Object.hasOwn(existing.values, field.key)
          ? existing.values[field.key]
          : field.default
      fields[field.key] = Array.isArray(value) ? [...value] : (value ?? null)
    }
    draft.value = reactive({ scope: existing?.scope ?? 'thread', values: fields })
    if (editable) drafts.set(id, draft.value)
  },
  { immediate: true },
)
const submitted = computed(() => (answered.value?.type === 'form' ? answered.value.values : null))
function isBlank(value: FormValue | undefined): boolean {
  return (
    value === null ||
    value === undefined ||
    value === '' ||
    (Array.isArray(value) && value.length === 0)
  )
}
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
const text = (key: string) => (typeof values.value[key] === 'string' ? values.value[key] : '')
const numberOf = (key: string) => (typeof values.value[key] === 'number' ? values.value[key] : null)
function submit() {
  void send({ type: 'form', values: { ...values.value } })
}
</script>

<template>
  <div class="ask-block">
    <section v-if="withdrawn && form" class="ask-form is-withdrawn" :aria-labelledby="formTitleId">
      <div class="ask-form-head">
        <span class="ask-form-icon" aria-hidden="true"><ClipboardListIcon /></span>
        <div class="ask-form-heading">
          <div :id="formTitleId" class="ask-form-title">{{ form.title }}</div>
          <div class="ask-form-description">
            {{ t('block.withdrawn', { reason: withdrawn }) }}
          </div>
        </div>
      </div>
    </section>
    <DouChatQuestion
      v-else-if="block.type === 'choice'"
      :options="questionOptions"
      :answer="questionAnswer"
      :status="questionStatus"
      :description="withdrawn || undefined"
      @select="(value) => send({ type: 'choice', choice: Number(value) })"
    >
      <template #title>
        <span class="ask-title">{{ block.question }}</span>
      </template>
      <template v-if="block.recommended != null" #option="{ option }">
        <span class="ask-option">
          <DouText weight="medium" size="sm">
            {{ option.label }}
            <DouText
              v-if="option.value === block.recommended"
              tone="primary"
              size="sm"
              class="ask-recommended"
            >
              {{ t('block.recommended') }}
            </DouText>
          </DouText>
          <DouText variant="secondary" size="sm">{{ option.description }}</DouText>
        </span>
      </template>
    </DouChatQuestion>

    <DouChatConfirm
      v-else-if="block.type === 'confirm'"
      :title="block.title"
      :description="confirmDescription"
      :tone="block.tone"
      :status="confirmStatus"
      :approve-text="block.approveText ?? undefined"
      :deny-text="block.denyText ?? undefined"
      @decide="decide"
    >
      <DouCodeBlock v-if="block.detail && !withdrawn" :code="block.detail" wrap :max-lines="12" />
      <DouRadioGroup
        v-if="block.allowProjectReference && confirmStatus === 'waiting'"
        v-model="referenceScope"
        :aria-label="t('reference.ask.scope')"
        :disabled="sending || !attentionId"
        size="sm"
        class="ask-reference-scope"
      >
        <DouRadio value="thread">{{ t('reference.ask.thread') }}</DouRadio>
        <DouRadio value="project">{{ t('reference.ask.project') }}</DouRadio>
      </DouRadioGroup>
    </DouChatConfirm>

    <section
      v-else-if="form && submitted"
      class="ask-form is-submitted"
      :aria-labelledby="formTitleId"
    >
      <div class="ask-form-head">
        <span class="ask-form-icon" aria-hidden="true"><ClipboardListIcon /></span>
        <div class="ask-form-heading">
          <div :id="formTitleId" class="ask-form-title">{{ form.title }}</div>
        </div>
        <span class="ask-form-status">
          <CheckIcon aria-hidden="true" />
          <span>{{ t('block.submitted') }}</span>
        </span>
      </div>
      <dl v-if="form.fields.length" class="ask-form-values">
        <div v-for="field in form.fields" :key="field.key" class="ask-form-field">
          <dt class="ask-form-label">{{ field.label }}</dt>
          <dd class="ask-form-value" :class="{ 'is-blank': isBlank(submitted[field.key]) }">
            {{ shown(field, submitted[field.key]) }}
          </dd>
        </div>
      </dl>
    </section>

    <section v-else-if="form" class="ask-form is-waiting" :aria-labelledby="formTitleId">
      <div class="ask-form-head">
        <span class="ask-form-icon" aria-hidden="true"><ClipboardListIcon /></span>
        <div class="ask-form-heading">
          <div :id="formTitleId" class="ask-form-title">{{ form.title }}</div>
          <div v-if="form.description" class="ask-form-description">
            {{ form.description }}
          </div>
        </div>
      </div>
      <DouForm :model="values" :disabled="sending" size="sm" class="ask-form-body" @submit="submit">
        <div class="ask-form-inputs">
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
        </div>
        <div class="ask-form-actions">
          <DouButton native-type="submit" size="sm" tone="primary" :loading="sending">
            {{ form.submitText ?? t('block.submit') }}
          </DouButton>
        </div>
      </DouForm>
    </section>
    <slot v-if="answered && !withdrawn" name="after" />
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
  overflow-wrap: anywhere;
}

.ask-form {
  overflow: hidden;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-container);
  background-color: var(--dou-color-bg-surface);
  color: var(--dou-color-text);
  font-size: var(--dou-font-size-sm);
  line-height: 20px;
}

.ask-form.is-waiting {
  border-color: var(--dou-color-border-strong);
  box-shadow: var(--dou-shadow-sm);
}

.ask-form-head {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 12px;
}

.ask-form-icon {
  display: inline-flex;
  flex: none;
  align-items: center;
  height: 20px;
  font-size: 16px;
  color: var(--dou-color-text-secondary);
}

.ask-form.is-withdrawn .ask-form-icon {
  color: var(--dou-color-text-tertiary);
}

.ask-form-heading {
  display: flex;
  flex: auto;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.ask-form-title {
  font-weight: var(--dou-font-weight-medium);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ask-form.is-withdrawn .ask-form-title {
  color: var(--dou-color-text-secondary);
}

.ask-form-description {
  color: var(--dou-color-text-secondary);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ask-form.is-withdrawn .ask-form-description {
  color: var(--dou-color-text-tertiary);
}

.ask-form-status {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 4px;
  height: 20px;
  color: var(--dou-color-text-secondary);
  white-space: nowrap;
}

.ask-form-status > svg {
  flex: none;
}

.ask-form-values {
  margin: 0;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.ask-form-field {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  padding: 10px 12px;
}

.ask-form-field + .ask-form-field {
  border-top: 1px solid var(--dou-color-border-subtle);
}

.ask-form-label {
  color: var(--dou-color-text-tertiary);
  font-size: 12px;
  line-height: 18px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ask-form-value {
  margin: 0;
  color: var(--dou-color-text);
  font-size: var(--dou-font-size-sm);
  line-height: 20px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ask-form-value.is-blank {
  color: var(--dou-color-text-tertiary);
}

.ask-form-body {
  --dou-form-gap: 0;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.ask-form-inputs {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 12px;
}

.ask-form-inputs :deep(.dou-form-item__label) {
  align-items: baseline;
  overflow-wrap: anywhere;
}

.ask-form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
  background-color: color-mix(in oklab, var(--dou-color-text) 2%, var(--dou-color-bg-surface));
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

.ask-reference-scope {
  margin-block-start: 8px;
}
</style>
