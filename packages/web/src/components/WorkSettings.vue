<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouFieldGroup,
  DouInput,
  DouSelect,
  DouTextarea,
} from 'dou-ui'
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
const savedSnapshot = ref('')

function currentSnapshot() {
  return JSON.stringify(props.part === 'instructions' ? instructions.value : rules.value)
}

const dirty = computed(() => loaded.value && currentSnapshot() !== savedSnapshot.value)

function load(work: WorkSettings) {
  loaded.value = true
  instructions.value = work.instructions
  rules.value = work.rules.map((rule) => ({ ...rule }))
  savedSnapshot.value = currentSnapshot()
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

defineExpose({ save, dirty })
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
      <DouFieldGroup v-for="(rule, at) in rules" :key="at" class="work-rule" size="sm" block>
        <DouSelect
          v-model="rule.action"
          class="work-action"
          :options="actionOptions"
          :aria-label="t('work.rule.action')"
        />
        <DouInput
          v-model="rule.pattern"
          class="work-pattern"
          :maxlength="500"
          placeholder="git push*"
          :aria-label="t('work.rule.pattern')"
        />
        <DouButton
          variant="outline"
          tone="danger"
          :aria-label="t('settings.remove')"
          :title="t('settings.remove')"
          @click="rules.splice(at, 1)"
        >
          <TrashIcon />
        </DouButton>
      </DouFieldGroup>
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

.work-rule :deep(.work-action) {
  flex: 0 0 96px;
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
