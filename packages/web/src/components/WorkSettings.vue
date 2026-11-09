<script setup lang="ts">
import { DouAlert, DouButton, DouEmpty, DouInput, DouSelect, DouTextarea } from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type CommandRule, type WorkSettings } from '../api'
import { t, tm } from '../i18n'

/** One part of how the work is done everywhere; the settings dialog saves them together. */
const props = defineProps<{ part: 'instructions' | 'commands' }>()

const instructions = ref('')
const rules = ref<CommandRule[]>([])
const error = ref<string | null>(null)

const actionOptions = computed(() =>
  (['allow', 'ask', 'deny'] as const).map((action) => ({
    value: action,
    label: t(`work.rule.${action}`),
  })),
)

/** Loaded: before that, saving would send nothing over what is saved. */
const loaded = ref(false)

function load(work: WorkSettings) {
  loaded.value = true
  instructions.value = work.instructions
  rules.value = work.rules.map((rule) => ({ ...rule }))
}

onMounted(async () => {
  try {
    load(await api.getWorkSettings())
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
})

async function save(): Promise<boolean> {
  if (!loaded.value) return true
  error.value = null
  try {
    const input =
      props.part === 'instructions'
        ? { instructions: instructions.value }
        : { rules: rules.value.filter((rule) => rule.pattern.trim()) }
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
</style>
