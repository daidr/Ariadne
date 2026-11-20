<script setup lang="ts">
import { DouAlert, DouButton, DouDialog, DouForm, DouSpinner } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import { api, ApiError, type PluginConfig } from '../api'
import { lt, t, tm } from '../i18n'
import { activePlugin, pluginsChanged } from '../plugin-config'
import SettingsFields from './SettingsFields.vue'

const config = ref<PluginConfig | null>(null)
const values = ref<Record<string, unknown>>({})
const error = ref<string | null>(null)
const saving = ref(false)
const browse = (_key: string, path: string) =>
  api.browseLocal(path).then((result) => result.listing)
const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

watch(activePlugin, async (id, _before, onCleanup) => {
  config.value = null
  error.value = null
  values.value = {}
  if (!id) return
  let current = true
  onCleanup(() => {
    current = false
  })
  try {
    const loaded = await api.getPluginConfig(id)
    if (!current) return
    values.value = { ...loaded.values }
    config.value = loaded
  } catch (cause) {
    if (current) error.value = message(cause)
  }
})

const schema = computed(() => {
  if (!config.value) return null
  const { schema: declared, fieldOrder } = config.value
  const keys = new Set([...(fieldOrder ?? []), ...Object.keys(declared.properties)])
  return {
    ...declared,
    properties: Object.fromEntries(
      [...keys].flatMap((key) => {
        const field = declared.properties[key]
        return field ? [[key, field]] : []
      }),
    ),
  }
})
const secretsSet = computed(
  () => config.value?.secretsSet.filter((key) => values.value[key] !== null) ?? [],
)
const title = computed(() => {
  const name = lt(config.value?.name) ?? activePlugin.value
  return name ? t('plugins.settingsTitle', { name }) : t('plugins.settings')
})

async function save() {
  const id = activePlugin.value
  if (!id || !config.value || saving.value) return
  saving.value = true
  error.value = null
  try {
    await api.savePluginConfig(id, values.value)
    pluginsChanged()
    if (activePlugin.value === id) activePlugin.value = null
  } catch (cause) {
    if (activePlugin.value === id) error.value = message(cause)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <DouDialog
    :open="activePlugin !== null"
    :title="title"
    :width="480"
    :before-close="() => !saving"
    centered
    @update:open="
      (open) => {
        if (!open) activePlugin = null
      }
    "
  >
    <DouForm v-if="schema" class="plugin-fields" layout="vertical" size="sm" @submit.prevent="save">
      <SettingsFields
        :schema="schema"
        :values="values"
        :secrets-set="secretsSet"
        :browse="browse"
        @clear-secret="
          (key) => {
            values[key] = null
          }
        "
      />
    </DouForm>
    <DouSpinner v-else-if="!error" size="sm" />
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
      <DouButton tone="primary" :disabled="!config" :loading="saving" @click="save">
        {{ t('settings.save') }}
      </DouButton>
    </template>
  </DouDialog>
</template>

<style scoped>
.plugin-fields {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
