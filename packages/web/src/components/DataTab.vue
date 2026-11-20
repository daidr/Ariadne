<script setup lang="ts">
import { DouAlert } from 'dou-ui'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { api, ApiError, type StorageSnapshot } from '../api'
import { tm } from '../i18n'
import DataSettings from './DataSettings.vue'
import WorktreeSettings from './WorktreeSettings.vue'

/**
 * What Ariadne keeps on this machine and how it runs there: data and backups, threads'
 * worktrees. Backups and removals act at once.
 */
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
</script>

<template>
  <div class="data-tab">
    <DouAlert v-if="error" tone="danger" closable @close="error = null">{{ error }}</DouAlert>
    <DataSettings :storage="storage" :measuring="measuring" />
    <WorktreeSettings :storage="storage" :measuring="measuring" @changed="measure()" />
  </div>
</template>

<style scoped>
.data-tab {
  display: flex;
  flex-direction: column;
  gap: 24px;
}
</style>
