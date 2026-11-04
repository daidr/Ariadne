<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouInput,
  DouKbd,
  DouSegmentedControl,
  DouTextarea,
} from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type InputSettings } from '../api'
import { t, tm } from '../i18n'
import { state } from '../store'

interface DraftSnippet {
  id: string
  name: string
  text: string
}

const submitOn = ref<InputSettings['submitOn']>(state.input.submitOn)
const snippets = ref<DraftSnippet[]>([])
const error = ref<string | null>(null)

const submitOptions = computed(() => [
  { value: 'enter', label: t('input.submitEnter') },
  { value: 'mod-enter', label: t('input.submitModEnter') },
])

function load(input: InputSettings) {
  submitOn.value = input.submitOn
  snippets.value = input.snippets.map((snippet) => ({ ...snippet }))
}

onMounted(async () => {
  load(state.input)
  try {
    load(await api.getInputSettings())
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
})

let nextId = 0
function add() {
  snippets.value.push({ id: `s_${Date.now().toString(36)}${nextId++}`, name: '', text: '' })
}

/** Save what is in view; a snippet with no name or no text is left out. */
async function save(): Promise<boolean> {
  error.value = null
  try {
    const saved = await api.saveInputSettings({
      submitOn: submitOn.value,
      snippets: snippets.value
        .filter((snippet) => snippet.name.trim() && snippet.text.trim())
        .map((snippet) => ({ ...snippet, name: snippet.name.trim() })),
    })
    state.input = saved
    load(saved)
    return true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return false
  }
}

defineExpose({ save })
</script>

<template>
  <div class="input-settings">
    <div class="input-row">
      <span>{{ t('input.submitOn') }}</span>
      <DouSegmentedControl v-model="submitOn" size="sm" :options="submitOptions" />
    </div>
    <div class="input-row">
      <span class="input-heading">
        {{ t('snippets.title') }}
        <DouKbd size="sm">/</DouKbd>
      </span>
      <DouButton size="sm" variant="ghost" @click="add">
        <template #leading><PlusIcon /></template>
        {{ t('snippets.add') }}
      </DouButton>
    </div>
    <DouEmpty v-if="!snippets.length" size="sm" :description="t('snippets.empty')" />
    <div v-for="(snippet, index) in snippets" :key="snippet.id" class="snippet">
      <DouInput
        v-model="snippet.name"
        size="sm"
        :maxlength="40"
        :placeholder="t('snippets.name')"
        :aria-label="t('snippets.name')"
        class="snippet-name"
      />
      <DouTextarea
        v-model="snippet.text"
        size="sm"
        :autosize="{ minRows: 1, maxRows: 8 }"
        :placeholder="t('snippets.text')"
        :aria-label="t('snippets.text')"
        class="snippet-text"
      />
      <DouButton
        size="sm"
        variant="ghost"
        tone="danger"
        :aria-label="t('settings.remove')"
        :title="t('settings.remove')"
        @click="snippets.splice(index, 1)"
      >
        <TrashIcon />
      </DouButton>
    </div>
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
  </div>
</template>

<style scoped>
.input-settings {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.input-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.input-heading {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.snippet {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}

.snippet-name {
  flex: none;
  width: 140px;
}

.snippet-text {
  flex: 1;
  min-width: 0;
}
</style>
