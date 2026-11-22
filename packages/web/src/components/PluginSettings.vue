<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouCard,
  DouCollapse,
  DouCollapseItem,
  DouPopconfirm,
  DouSwitch,
  DouTag,
  DouText,
  toast,
} from 'dou-ui'
import { TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type PluginRecord } from '../api'
import { lt, t, tm } from '../i18n'
import { openPluginSettings, pluginsChanged } from '../plugin-config'
import DirectoryInput from './DirectoryInput.vue'
import {
  affectedConfigs,
  groupBuiltinPlugins,
  pluginProvides,
  splitPlugins,
} from './plugin-settings'

const browseLocal = (typed: string) => api.browseLocal(typed).then((result) => result.listing)

// Changes apply at once: a plugin is loaded (or unloaded) as soon as it is added, removed or toggled.
const emit = defineEmits<{ changed: [] }>()

const plugins = ref<PluginRecord[]>([])
/** Model providers by protocol, to say which stop working when a plugin goes. */
const providers = ref<{ name: string; kind: string }[]>([])
const activeSearchProvider = ref('')
const searchTitles = ref<Record<string, string>>({})
const path = ref('')
const busy = ref(false)
const togglingId = ref<string | null>(null)
const error = ref<string | null>(null)
const builtinOpen = ref<string[]>([])

const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

async function refreshUsage() {
  try {
    providers.value = (await api.getModelSettings()).providers
  } catch {
    // Impact hints simply omit models when unavailable.
  }
  try {
    const search = await api.getSearchSettings()
    activeSearchProvider.value = search.provider
    const titles: Record<string, string> = {}
    for (const service of search.services) {
      titles[service.kind] = lt(service.title) ?? service.kind
    }
    searchTitles.value = titles
  } catch {
    // Impact hints simply omit search when unavailable.
  }
}

async function run(action: () => Promise<{ plugins: PluginRecord[] }>, changed = true) {
  busy.value = true
  error.value = null
  try {
    plugins.value = (await action()).plugins
    if (changed) {
      pluginsChanged()
      emit('changed')
    }
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
  await refreshUsage()
})

function removeQuestion(plugin: PluginRecord) {
  const question = t('plugins.removeConfirm', { name: nameOf(plugin) })
  const affected = providers.value.filter((p) => plugin.provides.models.includes(p.kind))
  return affected.length
    ? `${question}${t('plugins.removeAffects', { names: affected.map((p) => p.name).join('、') })}`
    : question
}

async function add() {
  if (await run(() => api.addPlugin(path.value.trim()))) {
    path.value = ''
    void refreshUsage()
  }
}

async function toggleBuiltin(plugin: PluginRecord, enabled: boolean) {
  if (!plugin.id) return
  togglingId.value = plugin.id
  error.value = null
  const affected = !enabled
    ? affectedConfigs(plugin, providers.value, activeSearchProvider.value, searchTitles.value)
    : []
  try {
    plugins.value = (await api.setPluginEnabled(plugin.id, enabled)).plugins
    pluginsChanged()
    emit('changed')
    if (!enabled && affected.length > 0) {
      toast(
        t('plugins.disableAffects', {
          name: nameOf(plugin),
          names: affected.join('、'),
        }),
      )
    }
    void refreshUsage()
  } catch (cause) {
    error.value = message(cause)
  } finally {
    togglingId.value = null
  }
}

const partitioned = computed(() => splitPlugins(plugins.value))
const local = computed(() => partitioned.value.thirdParty)
const builtins = computed(() => partitioned.value.builtins)
const builtinGroups = computed(() => groupBuiltinPlugins(builtins.value))
const enabledBuiltinCount = computed(
  () => builtins.value.filter((plugin) => plugin.status === 'loaded').length,
)

const nameOf = (plugin: PluginRecord) =>
  lt(plugin.name ?? undefined) ?? plugin.id ?? plugin.path ?? ''
</script>

<template>
  <div class="plugins">
    <!-- The warning matters once code from elsewhere is loaded. -->
    <DouText v-if="local.length" size="sm" variant="secondary">
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
    <DouCard v-for="plugin in local" :key="plugin.id ?? plugin.path ?? ''" padding="sm">
      <div class="plugins-row">
        <div class="plugins-main">
          <div class="plugins-title">
            <DouText weight="medium">{{ nameOf(plugin) }}</DouText>
            <DouTag v-if="plugin.status === 'failed'" size="sm" tone="danger">
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
              pluginProvides(plugin).length
                ? t('plugins.provides', { kinds: pluginProvides(plugin).join('、') })
                : t('plugins.nothing')
            }}
          </DouText>
        </div>
        <div class="plugins-actions">
          <DouButton
            v-if="plugin.id && plugin.configurable && plugin.status === 'loaded'"
            size="sm"
            variant="outline"
            @click="openPluginSettings(plugin.id)"
          >
            {{ t('plugins.settings') }}
          </DouButton>
          <DouPopconfirm
            v-if="plugin.source === 'path' && plugin.path"
            :title="removeQuestion(plugin)"
            tone="danger"
            :confirm-text="t('plugins.remove')"
            @confirm="run(() => api.removePlugin(plugin.path!))"
          >
            <DouButton size="sm" variant="ghost" tone="danger" :aria-label="t('plugins.remove')">
              <TrashIcon />
            </DouButton>
          </DouPopconfirm>
        </div>
      </div>
    </DouCard>

    <DouCollapse v-if="builtins.length" v-model="builtinOpen" class="plugins-builtin">
      <DouCollapseItem
        value="builtins"
        :title="
          t('plugins.builtinSummary', { enabled: enabledBuiltinCount, total: builtins.length })
        "
      >
        <div class="plugins-groups">
          <div v-for="group in builtinGroups" :key="group.key" class="plugins-group">
            <DouText size="sm" weight="medium" variant="tertiary" class="plugins-group-title">
              {{ t(`plugins.group.${group.key}`) }}
            </DouText>
            <div class="plugins-builtin-list">
              <div
                v-for="plugin in group.plugins"
                :key="plugin.id ?? ''"
                class="plugins-builtin-item"
                :class="{ 'plugins-builtin-disabled': plugin.status === 'disabled' }"
              >
                <div class="plugins-main">
                  <div class="plugins-title">
                    <DouText size="sm" weight="medium">{{ nameOf(plugin) }}</DouText>
                    <DouTag v-if="plugin.status === 'disabled'" size="sm">
                      {{ t('plugins.disabled') }}
                    </DouTag>
                  </div>
                  <DouText size="sm" variant="secondary">
                    {{
                      pluginProvides(plugin).length
                        ? t('plugins.provides', { kinds: pluginProvides(plugin).join('、') })
                        : t('plugins.nothing')
                    }}
                  </DouText>
                </div>
                <div class="plugins-actions">
                  <DouButton
                    v-if="plugin.id && plugin.configurable && plugin.status === 'loaded'"
                    size="sm"
                    variant="outline"
                    @click="openPluginSettings(plugin.id)"
                  >
                    {{ t('plugins.settings') }}
                  </DouButton>
                  <DouSwitch
                    :model-value="plugin.status === 'loaded'"
                    size="sm"
                    :disabled="busy || togglingId === plugin.id"
                    :aria-label="t('plugins.toggle', { name: nameOf(plugin) })"
                    @update:model-value="(enabled) => toggleBuiltin(plugin, enabled)"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      </DouCollapseItem>
    </DouCollapse>
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

.plugins-actions {
  display: flex;
  align-items: center;
  flex: none;
  gap: 8px;
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

.plugins-groups {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-top: 4px;
}

.plugins-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.plugins-group-title {
  padding-inline: 2px;
}

.plugins-builtin-list {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-md);
  overflow: hidden;
}

.plugins-builtin-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 12px;
  background: var(--dou-color-bg-surface);
}

.plugins-builtin-item + .plugins-builtin-item {
  border-top: 1px solid var(--dou-color-border-subtle);
}

.plugins-builtin-disabled .plugins-main {
  opacity: 0.72;
}
</style>
