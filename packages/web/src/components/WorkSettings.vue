<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputNumber,
  DouSelect,
  DouTextarea,
} from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type CommandRule, type Limits, type WorkSettings } from '../api'
import { t, tm } from '../i18n'

/** One part of how the work is done everywhere; each tab saves its own. */
const props = defineProps<{ part: 'instructions' | 'commands' | 'limits' }>()

const instructions = ref('')
const rules = ref<CommandRule[]>([])
const limits = ref<Limits | null>(null)
const defaults = ref<Limits | null>(null)
/** The share of the context, shown as a percentage. */
const compactPercent = computed({
  get: () => (limits.value ? Math.round(limits.value.compactShare * 100) : null),
  set: (value) => {
    if (limits.value && value) limits.value.compactShare = value / 100
  },
})
const error = ref<string | null>(null)

const actionOptions = computed(() =>
  (['allow', 'ask', 'deny'] as const).map((action) => ({
    value: action,
    label: t(`work.rule.${action}`),
  })),
)

function load(work: WorkSettings) {
  instructions.value = work.instructions
  rules.value = work.rules.map((rule) => ({ ...rule }))
  limits.value = { ...work.limits }
  defaults.value = work.defaults
}

onMounted(async () => {
  try {
    load(await api.getWorkSettings())
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
})

async function save(): Promise<boolean> {
  error.value = null
  try {
    const input =
      props.part === 'instructions'
        ? { instructions: instructions.value }
        : props.part === 'commands'
          ? { rules: rules.value.filter((rule) => rule.pattern.trim()) }
          : { limits: limits.value ?? undefined }
    load(await api.saveWorkSettings(input))
    return true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return false
  }
}

defineExpose({ save })
</script>

<template>
  <div class="work">
    <DouTextarea
      v-if="part === 'instructions'"
      v-model="instructions"
      :autosize="{ minRows: 10, maxRows: 24 }"
      :maxlength="16000"
      :placeholder="t('work.instructionsPlaceholder')"
      :aria-label="t('settings.tab.instructions')"
    />

    <template v-else-if="part === 'commands'">
      <div class="work-head">
        <DouButton size="sm" variant="ghost" @click="rules.push({ pattern: '', action: 'ask' })">
          <template #leading><PlusIcon /></template>
          {{ t('work.rule.add') }}
        </DouButton>
      </div>
      <DouEmpty v-if="!rules.length" size="sm" :description="t('work.rule.empty')" />
      <div v-for="(rule, at) in rules" :key="at" class="work-row">
        <div class="work-action">
          <DouSelect
            v-model="rule.action"
            size="sm"
            :options="actionOptions"
            :aria-label="t('work.rule.action')"
          />
        </div>
        <div class="work-pattern">
          <DouInput
            v-model="rule.pattern"
            size="sm"
            :maxlength="500"
            placeholder="git push*"
            :aria-label="t('work.rule.pattern')"
          />
        </div>
        <DouButton
          size="sm"
          variant="ghost"
          tone="danger"
          :aria-label="t('settings.remove')"
          :title="t('settings.remove')"
          @click="rules.splice(at, 1)"
        >
          <TrashIcon />
        </DouButton>
      </div>
    </template>

    <DouForm v-else-if="limits && defaults" layout="vertical" size="sm">
      <div class="work-grid">
        <DouFormItem :label="t('work.limit.commandTimeout')">
          <DouInputNumber
            v-model="limits.commandTimeout"
            :min="5"
            :max="3600"
            :placeholder="String(defaults.commandTimeout)"
          />
        </DouFormItem>
        <DouFormItem :label="t('work.limit.commandTimeoutMax')">
          <DouInputNumber
            v-model="limits.commandTimeoutMax"
            :min="5"
            :max="7200"
            :placeholder="String(defaults.commandTimeoutMax)"
          />
        </DouFormItem>
        <DouFormItem :label="t('work.limit.commandOutput')">
          <DouInputNumber
            v-model="limits.commandOutput"
            :min="2000"
            :max="200000"
            :step="1000"
            :placeholder="String(defaults.commandOutput)"
          />
        </DouFormItem>
        <DouFormItem :label="t('work.limit.compactShare')">
          <DouInputNumber
            v-model="compactPercent"
            :min="30"
            :max="90"
            :step="5"
            :placeholder="String(defaults.compactShare * 100)"
          />
        </DouFormItem>
        <DouFormItem :label="t('work.limit.modelRetries')">
          <DouInputNumber
            v-model="limits.modelRetries"
            :min="0"
            :max="10"
            :placeholder="String(defaults.modelRetries)"
          />
        </DouFormItem>
        <DouFormItem :label="t('work.limit.loopRepeats')">
          <DouInputNumber
            v-model="limits.loopRepeats"
            :min="2"
            :max="20"
            :placeholder="String(defaults.loopRepeats)"
          />
        </DouFormItem>
      </div>
      <div class="work-head">
        <DouButton size="sm" variant="ghost" @click="limits = { ...defaults }">
          {{ t('work.limit.reset') }}
        </DouButton>
      </div>
    </DouForm>

    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
  </div>
</template>

<style scoped>
.work {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.work-head {
  display: flex;
  justify-content: flex-end;
}

.work-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.work-action {
  flex: none;
  width: 96px;
}

.work-pattern {
  flex: 1;
  min-width: 0;
}

.work-pattern :deep(input) {
  font-family: var(--dou-font-family-mono);
}

.work-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}
</style>
