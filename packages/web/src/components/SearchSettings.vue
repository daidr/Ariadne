<script setup lang="ts">
import { DouAlert, DouButton, DouForm, DouFormItem, DouSelect, DouText, toast } from 'dou-ui'
import { computed, onMounted, ref, watch } from 'vue'
import {
  api,
  ApiError,
  type SearchServiceView,
  type SearchSettings,
  type SearchSettingsInput,
} from '../api'
import { lt, t, tm } from '../i18n'
import { pluginRevision } from '../plugin-config'
import { report } from '../store'
import SettingsFields from './SettingsFields.vue'

/** Unified web search settings for all registered search plugins (built-in and external). */
const search = ref<SearchSettings | null>(null)
const provider = ref('')
const drafts = ref<Record<string, Record<string, unknown>>>({})
const error = ref<string | null>(null)
const checking = ref(false)

const activeService = computed<SearchServiceView | null>(
  () => search.value?.services.find((item) => item.kind === provider.value) ?? null,
)
const activeValues = computed<Record<string, unknown>>(() => drafts.value[provider.value] ?? {})
const activeSecretsSet = computed(
  () => activeService.value?.secretsSet.filter((key) => activeValues.value[key] !== null) ?? [],
)
const hasSettings = computed(
  () => Object.keys(activeService.value?.settings.properties ?? {}).length > 0,
)
const options = computed(
  () =>
    search.value?.services.map((item) => ({
      value: item.kind,
      label: lt(item.title) ?? item.kind,
    })) ?? [],
)
const modelSources = computed(() => (search.value?.models ?? []).filter(Boolean).join('、'))

const canCheck = computed(() => {
  const service = activeService.value
  if (!service?.capabilities?.supportsCheck) return false
  const required = service.settings.required ?? []
  const values = activeValues.value
  const savedSecrets = activeSecretsSet.value
  return required.every((key) => {
    const field = service.settings.properties[key]
    const value = values[key]
    if (field?.type === 'string' && field.secret) {
      return (typeof value === 'string' && value.trim() !== '') || savedSecrets.includes(key)
    }
    if (typeof value === 'string') return value.trim() !== '' || field?.default !== undefined
    return (value !== undefined && value !== null) || field?.default !== undefined
  })
})

function load(value: SearchSettings, preserveDrafts = false) {
  search.value = value
  if (!preserveDrafts || !value.services.some((item) => item.kind === provider.value)) {
    provider.value = value.provider
  }
  const next: Record<string, Record<string, unknown>> = {}
  for (const service of value.services) {
    next[service.kind] =
      preserveDrafts && drafts.value[service.kind]
        ? drafts.value[service.kind]!
        : { ...service.values }
  }
  drafts.value = next
}

async function reload(preserveDrafts = false) {
  error.value = null
  try {
    load(await api.getSearchSettings(), preserveDrafts)
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
}

onMounted(() => reload(false))
watch(pluginRevision, () => {
  if (search.value) void reload(true)
})

function payloadFor(service: SearchServiceView): Record<string, unknown> {
  const draft = drafts.value[service.kind] ?? {}
  const result: Record<string, unknown> = {}
  for (const [key, field] of Object.entries(service.settings.properties ?? {})) {
    const value = draft[key]
    if (field.type === 'string' && field.secret) {
      if (value === null) {
        result[key] = null
      } else if (typeof value === 'string' && value.trim() !== '') {
        result[key] = value.trim()
      }
      continue
    }
    if (value !== undefined) {
      result[key] = value
    }
  }
  return result
}

function input(onlyActive = false): SearchSettingsInput {
  const services: SearchSettingsInput['services'] = {}
  for (const service of search.value?.services ?? []) {
    if (onlyActive && service.kind !== provider.value) continue
    services[service.kind] = payloadFor(service)
  }
  if (!provider.value && search.value?.disabledProvider) {
    return { services }
  }
  return { provider: provider.value, services }
}

function clearSecret(key: string) {
  const kind = provider.value
  if (!kind) return
  const draft = (drafts.value[kind] ??= {})
  draft[key] = null
}

async function check() {
  checking.value = true
  error.value = null
  try {
    const { ok, status, message } = await api.checkSearchSettings(input(true))
    if (ok) {
      toast.success(t('settings.searchCheckOk'))
    } else {
      const detail = lt(message)
      report(
        detail ||
          (status ? t('settings.searchCheckHttp', { status }) : t('settings.searchCheckFailed')),
      )
    }
  } catch (cause) {
    report(cause)
  } finally {
    checking.value = false
  }
}

async function save(): Promise<boolean> {
  if (!search.value) return false
  error.value = null
  try {
    // Models were saved just before this section, so the returned fallback sources are current.
    load(await api.saveSearchSettings(input()))
    return true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return false
  }
}

defineExpose({ save, checking, reload })
</script>

<template>
  <section class="search" :aria-label="t('settings.search')">
    <DouText weight="medium">{{ t('settings.search') }}</DouText>
    <template v-if="search">
      <DouText size="sm" variant="secondary">
        {{
          modelSources
            ? t('settings.searchSources', { sources: modelSources })
            : t('settings.searchNoSources')
        }}
      </DouText>
      <DouText v-if="search.disabledProvider" size="sm" variant="secondary">
        {{ t('settings.searchProviderDisabled', { kind: search.disabledProvider }) }}
      </DouText>
      <DouText v-if="!search.services.length" size="sm" variant="secondary">
        {{ t('settings.searchNoProviders') }}
      </DouText>
      <template v-else>
        <DouForm layout="vertical" size="sm" :disabled="checking">
          <DouFormItem :label="t('settings.searchProvider')">
            <DouSelect v-model="provider" :options="options" />
          </DouFormItem>
          <DouText
            v-if="activeService?.description && lt(activeService.description)"
            size="sm"
            variant="secondary"
          >
            {{ lt(activeService.description) }}
          </DouText>
          <SettingsFields
            v-if="activeService && hasSettings"
            :schema="activeService.settings"
            :values="activeValues"
            :secrets-set="activeSecretsSet"
            @clear-secret="clearSecret"
          />
          <DouText v-else-if="activeService" size="sm" variant="secondary">
            {{ t('settings.searchNoSettings') }}
          </DouText>
        </DouForm>
        <div v-if="activeService?.capabilities?.supportsCheck" class="search-check">
          <DouButton size="sm" :loading="checking" :disabled="checking || !canCheck" @click="check">
            {{ t('settings.searchCheck') }}
          </DouButton>
        </div>
      </template>
    </template>
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
    <DouButton v-if="!search" size="sm" @click="reload(false)">{{
      t('settings.searchReload')
    }}</DouButton>
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
