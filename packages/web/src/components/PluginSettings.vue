<script setup lang="ts">
import { DouAlert, DouButton, DouCard, DouPopconfirm, DouTag, DouText } from 'dou-ui'
import { TrashIcon } from '@dou-ui/icons'
import { onMounted, ref } from 'vue'
import { api, ApiError, type PluginRecord } from '../api'
import { lt, t, tm } from '../i18n'
import DirectoryInput from './DirectoryInput.vue'

const browseLocal = (typed: string) => api.browseLocal(typed).then((result) => result.listing)

// Changes apply at once: a plugin is loaded (or unloaded) as soon as it is added (or removed).
const emit = defineEmits<{ changed: [] }>()

const plugins = ref<PluginRecord[]>([])
/** Model providers by protocol, to say which stop working when a plugin goes. */
const providers = ref<{ name: string; kind: string }[]>([])
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

onMounted(async () => {
  await run(() => api.listPlugins(), false)
  try {
    providers.value = (await api.getModelSettings()).providers
  } catch {
    // The warning just names no providers.
  }
})

function removeQuestion(plugin: PluginRecord) {
  const question = t('plugins.removeConfirm', { name: nameOf(plugin) })
  const affected = providers.value.filter((p) => plugin.provides.models.includes(p.kind))
  return affected.length
    ? `${question}${t('plugins.removeAffects', { names: affected.map((p) => p.name).join('、') })}`
    : question
}

async function add() {
  if (await run(() => api.addPlugin(path.value.trim()))) path.value = ''
}

const nameOf = (plugin: PluginRecord) =>
  lt(plugin.name ?? undefined) ?? plugin.id ?? plugin.path ?? ''
// A source and its link cards share a name (github, github): each kind is listed once.
const provides = (plugin: PluginRecord) => [
  ...new Set([
    ...plugin.provides.models,
    ...plugin.provides.contexts,
    ...plugin.provides.dashboards,
    ...plugin.provides.searches,
    ...plugin.provides.links,
  ]),
]
</script>

<template>
  <div class="plugins">
    <!-- The warning matters once code from elsewhere is loaded. -->
    <DouText
      v-if="plugins.some((plugin) => plugin.source !== 'builtin')"
      size="sm"
      variant="secondary"
    >
      {{ t('plugins.help') }}
    </DouText>
    <form class="plugins-add" @submit.prevent="add">
      <DirectoryInput
        v-model="path"
        size="sm"
        class="plugins-path"
        :browse="browseLocal"
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
            <DouTag v-if="plugin.source !== 'builtin'" size="sm">{{ t('plugins.path') }}</DouTag>
            <DouTag v-if="plugin.status !== 'loaded'" size="sm" tone="danger">
              {{ t('plugins.failed') }}
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
          :title="removeQuestion(plugin)"
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
