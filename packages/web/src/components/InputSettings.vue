<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouEmpty,
  DouInput,
  DouKbd,
  DouSegmentedControl,
  DouSwitch,
  DouText,
  DouTextarea,
  useColorMode,
} from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, DEFAULT_KEYS, type InputSettings, type ShortcutAction } from '../api'
import { notifications, setNotifications } from '../notify'
import { t, tm } from '../i18n'
import { state } from '../store'

interface DraftSnippet {
  id: string
  name: string
  text: string
}

const submitOn = ref<InputSettings['submitOn']>(state.input.submitOn)
const snippets = ref<DraftSnippet[]>([])
// Keys are no longer rebound here; ones set before are saved back as they were.
const keys = ref<Record<ShortcutAction, string>>({ ...DEFAULT_KEYS })

const error = ref<string | null>(null)

// The page's look follows the system unless set here; it changes at once, in this browser.
const { mode } = useColorMode()
const modeOptions = computed(() =>
  (['system', 'light', 'dark'] as const).map((value) => ({
    value,
    label: t(`appearance.${value}`),
  })),
)

const submitOptions = computed(() => [
  { value: 'enter', label: t('input.submitEnter') },
  { value: 'mod-enter', label: t('input.submitModEnter') },
])

function load(input: InputSettings) {
  submitOn.value = input.submitOn
  snippets.value = input.snippets.map((snippet) => ({ ...snippet }))
  keys.value = { ...DEFAULT_KEYS, ...input.keys }
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
      keys: keys.value,
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
      <span>{{ t('appearance.title') }}</span>
      <DouSegmentedControl v-model="mode" size="sm" :options="modeOptions" />
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
    <div class="input-row">
      <span>{{ t('settings.notify') }}</span>
      <DouSwitch
        :model-value="notifications.enabled"
        :disabled="
          notifications.permission === 'unsupported' || notifications.permission === 'denied'
        "
        :aria-label="t('settings.notify')"
        @update:model-value="(on: boolean) => setNotifications(on)"
      />
    </div>
    <DouText
      v-if="notifications.permission === 'denied' || notifications.permission === 'unsupported'"
      tone="warning"
      size="sm"
    >
      {{
        t(
          notifications.permission === 'denied'
            ? 'settings.notifyDenied'
            : 'settings.notifyUnsupported',
        )
      }}
    </DouText>
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
