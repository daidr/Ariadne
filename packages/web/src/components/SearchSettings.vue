<script setup lang="ts">
import { DouAlert, DouButton, DouForm, DouFormItem, DouInput, DouSelect, DouText } from 'dou-ui'
import { computed, onMounted, ref, watch } from 'vue'
import { api, ApiError, type SearchSettings, type SearchSettingsInput } from '../api'
import { lt, t, tm } from '../i18n'

/** A separate search API, used after the plugins and model services. */
const search = ref<SearchSettings | null>(null)
const provider = ref<SearchSettings['provider']>('tavily')
const drafts = ref<
  Record<SearchSettings['provider'], { baseUrl: string; apiKey: string; cleared: boolean }>
>({
  tavily: { baseUrl: '', apiKey: '', cleared: false },
  deepseek: { baseUrl: '', apiKey: '', cleared: false },
})
const error = ref<string | null>(null)
const checking = ref(false)
const checked = ref<{ ok: boolean; status: number | null } | null>(null)
const active = computed(() => drafts.value[provider.value])
const saved = computed(() => search.value?.services.find((item) => item.kind === provider.value))
const options = computed(
  () => search.value?.services.map((item) => ({ value: item.kind, label: item.title })) ?? [],
)
const sources = computed(() =>
  [...(search.value?.plugins.map((title) => lt(title)) ?? []), ...(search.value?.models ?? [])]
    .filter(Boolean)
    .join('、'),
)

function load(value: SearchSettings) {
  search.value = value
  provider.value = value.provider
  for (const service of value.services) {
    drafts.value[service.kind] = { baseUrl: service.baseUrl, apiKey: '', cleared: false }
  }
}
async function reload() {
  error.value = null
  try {
    load(await api.getSearchSettings())
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
}
onMounted(reload)
watch(
  [provider, drafts],
  () => {
    checked.value = null
  },
  { deep: true },
)

function input(onlyActive = false): SearchSettingsInput {
  const services: SearchSettingsInput['services'] = {}
  for (const kind of ['tavily', 'deepseek'] as const) {
    if (onlyActive && kind !== provider.value) continue
    const draft = drafts.value[kind]
    services[kind] = {
      baseUrl: draft.baseUrl,
      apiKey: draft.apiKey.trim() || (draft.cleared ? null : ''),
    }
  }
  return { provider: provider.value, services }
}
function clear() {
  active.value.apiKey = ''
  active.value.cleared = true
}
async function check() {
  checking.value = true
  error.value = null
  checked.value = null
  try {
    checked.value = await api.checkSearchSettings(input(true))
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    checking.value = false
  }
}
async function save(): Promise<boolean> {
  if (!search.value) return false
  error.value = null
  try {
    // Models were saved just before this section, so the returned sources are current.
    load(await api.saveSearchSettings(input()))
    return true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return false
  }
}
defineExpose({ save, checking })
</script>

<template>
  <section class="search" :aria-label="t('settings.search')">
    <DouText weight="medium">{{ t('settings.search') }}</DouText>
    <template v-if="search">
      <DouText size="sm" variant="secondary">
        {{ sources ? t('settings.searchSources', { sources }) : t('settings.searchNoSources') }}
      </DouText>
      <DouForm layout="vertical" size="sm" :disabled="checking">
        <DouFormItem :label="t('settings.searchProvider')">
          <DouSelect v-model="provider" :options="options" />
        </DouFormItem>
        <DouFormItem :label="t('settings.searchBaseUrl')" :help="t('settings.searchBaseUrlHelp')">
          <DouInput
            v-model="active.baseUrl"
            :placeholder="saved?.defaultBaseUrl"
            autocomplete="off"
          />
        </DouFormItem>
        <DouFormItem
          :label="t('settings.searchKey')"
          :help="saved?.keySet && !active.cleared ? t('settings.secretSet') : undefined"
        >
          <DouInput v-model="active.apiKey" type="password" show-password autocomplete="off">
            <template v-if="saved?.keySet && !active.cleared" #suffix>
              <DouButton size="xs" variant="ghost" @click="clear">{{
                t('settings.clearSecret')
              }}</DouButton>
            </template>
          </DouInput>
        </DouFormItem>
      </DouForm>
      <div class="search-check">
        <DouButton
          size="sm"
          :loading="checking"
          :disabled="checking || (!active.apiKey.trim() && (!saved?.keySet || active.cleared))"
          @click="check"
        >
          {{ t('settings.searchCheck') }}
        </DouButton>
      </div>
      <DouAlert v-if="checked" :tone="checked.ok ? 'success' : 'danger'">
        {{
          checked.ok
            ? t('settings.searchCheckOk')
            : checked.status
              ? t('settings.searchCheckHttp', { status: checked.status })
              : t('settings.searchCheckFailed')
        }}
      </DouAlert>
    </template>
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
    <DouButton v-if="!search" size="sm" @click="reload">{{ t('settings.searchReload') }}</DouButton>
  </section>
</template>

<style scoped>
.search {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.search-check {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
</style>
