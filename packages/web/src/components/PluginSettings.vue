<script setup lang="ts">
import { DouAlert, DouButton, DouCard, DouInput, DouPopconfirm, DouTag, DouText } from 'dou-ui'
import { TrashIcon } from '@dou-ui/icons'
import { onMounted, ref } from 'vue'
import { api, ApiError, type PluginRecord } from '../api'
import { lt, t, tm } from '../i18n'

// Changes apply at once: a plugin is loaded (or unloaded) as soon as it is added (or removed).
const emit = defineEmits<{ changed: [] }>()

const plugins = ref<PluginRecord[]>([])
const path = ref('')
const busy = ref(false)
const error = ref<string | null>(null)

const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

async function run(action: () => Promise<{ plugins: PluginRecord[] }>, changed = true) {
  busy.value = true
  error.value = null
  try {
    plugins.value = (await action()).plugins
    if (changed) emit('changed')
    return true
  } catch (cause) {
    error.value = message(cause)
    return false
  } finally {
    busy.value = false
  }
}

onMounted(() => run(() => api.listPlugins(), false))

async function add() {
  if (await run(() => api.addPlugin(path.value.trim()))) path.value = ''
}

const nameOf = (plugin: PluginRecord) =>
  lt(plugin.name ?? undefined) ?? plugin.id ?? plugin.path ?? ''
const provides = (plugin: PluginRecord) => [...plugin.provides.models, ...plugin.provides.contexts]
</script>

<template>
  <div class="plugins">
    <DouText size="sm" variant="secondary">{{ t('plugins.help') }}</DouText>
    <form class="plugins-add" @submit.prevent="add">
      <DouInput
        v-model="path"
        size="sm"
        class="plugins-path"
        :placeholder="t('plugins.placeholder')"
        :aria-label="t('plugins.placeholder')"
      />
      <DouButton size="sm" native-type="submit" :disabled="!path.trim()" :loading="busy">
        {{ t('plugins.add') }}
      </DouButton>
    </form>
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
    <DouCard v-for="plugin in plugins" :key="plugin.id ?? plugin.path ?? ''" padding="sm">
      <div class="plugins-row">
        <div class="plugins-main">
          <div class="plugins-title">
            <DouText weight="medium">{{ nameOf(plugin) }}</DouText>
            <DouTag size="sm">
              {{ plugin.source === 'builtin' ? t('plugins.builtin') : t('plugins.path') }}
            </DouTag>
            <DouTag size="sm" :tone="plugin.status === 'loaded' ? 'success' : 'danger'">
              {{ plugin.status === 'loaded' ? t('plugins.loaded') : t('plugins.failed') }}
            </DouTag>
          </div>
          <DouText v-if="plugin.path" size="sm" variant="tertiary" class="plugins-text">
            {{ plugin.path }}
          </DouText>
          <DouText v-if="plugin.error" size="sm" class="plugins-error plugins-text">
            {{ plugin.error }}
          </DouText>
          <DouText v-else size="sm" variant="secondary">
            {{
              provides(plugin).length
                ? t('plugins.provides', { kinds: provides(plugin).join('、') })
                : t('plugins.nothing')
            }}
          </DouText>
        </div>
        <DouPopconfirm
          v-if="plugin.source === 'path' && plugin.path"
          :title="t('plugins.removeConfirm', { name: nameOf(plugin) })"
          tone="danger"
          :confirm-text="t('plugins.remove')"
          @confirm="run(() => api.removePlugin(plugin.path!))"
        >
          <DouButton size="sm" variant="ghost" tone="danger" :aria-label="t('plugins.remove')">
            <template #leading><TrashIcon /></template>
            {{ t('plugins.remove') }}
          </DouButton>
        </DouPopconfirm>
      </div>
    </DouCard>
  </div>
</template>

<style scoped>
.plugins {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.plugins-add {
  display: flex;
  gap: 8px;
}

.plugins-path {
  flex: 1;
  min-width: 0;
}

.plugins-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.plugins-main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.plugins-title {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.plugins-text {
  overflow-wrap: anywhere;
}

.plugins-error {
  color: var(--dou-color-danger-text);
  white-space: pre-wrap;
}
</style>
