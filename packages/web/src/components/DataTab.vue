<script setup lang="ts">
import { DouAlert, DouText } from 'dou-ui'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { api, ApiError, type StorageSnapshot } from '../api'
import { t, tm } from '../i18n'
import DataSettings from './DataSettings.vue'
import PluginSettings from './PluginSettings.vue'
import WorktreeSettings from './WorktreeSettings.vue'

/**
 * What Ariadne keeps on this machine and how it runs there: data and backups, threads'
 * worktrees, and plugins loaded from a folder. Backups, removals and plugins act at once.
 */
const emit = defineEmits<{ pluginsChanged: [] }>()
const storage = ref<StorageSnapshot | null>(null)
const measuring = ref(true)
const error = ref<string | null>(null)
let controller: AbortController | undefined
let closed = false

onBeforeUnmount(() => {
  closed = true
  controller?.abort()
})

async function measure(cached = false) {
  if (closed) return
  controller?.abort()
  controller = new AbortController()
  const { signal } = controller
  measuring.value = true
  error.value = null
  try {
    if (cached) {
      const saved = await api.getStorage(true, signal)
      if (!signal.aborted) storage.value = saved.storage
    } else {
      storage.value = null
    }
    if (signal.aborted) return
    const measured = await api.getStorage(false, signal)
    if (!signal.aborted) storage.value = measured.storage
  } catch (cause) {
    if (!signal.aborted)
      error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    if (!signal.aborted) measuring.value = false
  }
}

onMounted(() => measure(true))

function pluginsChanged() {
  emit('pluginsChanged')
  void measure()
}
</script>

<template>
  <div class="data-tab">
    <DouAlert v-if="error" tone="danger" closable @close="error = null">{{ error }}</DouAlert>
    <DataSettings :storage="storage" :measuring="measuring" />
    <WorktreeSettings :storage="storage" :measuring="measuring" @changed="measure()" />
    <section class="data-tab-section">
      <DouText weight="medium">{{ t('settings.tab.plugins') }}</DouText>
      <PluginSettings @changed="pluginsChanged" />
    </section>
  </div>
</template>

<style scoped>
.data-tab {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.data-tab-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
