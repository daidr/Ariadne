<script setup lang="ts">
import { DouAlert, DouButton, DouForm, DouFormItem, DouInput, DouText } from 'dou-ui'
import { onMounted, ref } from 'vue'
import { api, ApiError, type SearchSettings } from '../api'
import { t, tm } from '../i18n'

/**
 * The key of the search service, asked for only where search waits for one: when a model
 * service searches the web itself (or a plugin brought a search service), nothing shows.
 * Saved with the rest of the settings.
 */
const search = ref<SearchSettings | null>(null)
const apiKey = ref('')
/** The stored key goes when the settings are saved. */
const cleared = ref(false)
const error = ref<string | null>(null)

onMounted(async () => {
  try {
    search.value = await api.getSearchSettings()
  } catch {
    // Without it the section stays out of sight.
  }
})

function clear() {
  apiKey.value = ''
  cleared.value = true
}

async function save(): Promise<boolean> {
  const key = apiKey.value.trim()
  if (!search.value || (!key && !cleared.value)) {
    // The models were saved just before: whether a key is needed may have changed.
    search.value = await api.getSearchSettings().catch(() => search.value)
    return true
  }
  error.value = null
  try {
    search.value = await api.saveSearchSettings({ apiKey: key || null })
    apiKey.value = ''
    cleared.value = false
    return true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return false
  }
}

defineExpose({ save })
</script>

<template>
  <section v-if="search && (search.needed || search.keySet)" class="search">
    <DouText weight="medium">{{ t('settings.search') }}</DouText>
    <DouForm layout="vertical" size="sm">
      <DouFormItem
        :label="t('settings.searchKey')"
        :help="search.keySet && !cleared ? t('settings.secretSet') : t('settings.searchKeyHelp')"
      >
        <DouInput v-model="apiKey" type="password" show-password placeholder="tvly-…">
          <template v-if="search.keySet && !cleared" #suffix>
            <DouButton class="clear-secret" size="xs" variant="ghost" @click="clear">
              {{ t('settings.clearSecret') }}
            </DouButton>
          </template>
        </DouInput>
      </DouFormItem>
    </DouForm>
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
  </section>
</template>

<style scoped>
.search {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* Sits inside the input's edge, the same distance from it on three sides. */
.clear-secret {
  margin-inline-end: calc(2px - var(--dou-input-padding-x));
}
</style>
