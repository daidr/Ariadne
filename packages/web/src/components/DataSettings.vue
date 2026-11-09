<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouDescriptions,
  DouDescriptionsItem,
  DouEmpty,
  DouPopconfirm,
  DouSpinner,
  DouText,
} from 'dou-ui'
import { DatabasePlusIcon, TrashIcon } from '@dou-ui/icons'
import { onMounted, ref } from 'vue'
import { api, ApiError, type DataUsage } from '../api'
import { formatBytes } from '../bytes'
import { locale, t, tm } from '../i18n'

// What Ariadne's data directory takes, and copies of the database made on request.
const usage = ref<DataUsage | null>(null)
const error = ref<string | null>(null)
const backingUp = ref(false)
const removing = ref<string | null>(null)

const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

async function load() {
  try {
    usage.value = await api.getDataUsage()
  } catch (cause) {
    error.value = message(cause)
  }
}
onMounted(load)

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, {
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })

async function backup() {
  backingUp.value = true
  error.value = null
  try {
    await api.backupData()
    await load()
  } catch (cause) {
    error.value = message(cause)
  } finally {
    backingUp.value = false
  }
}

async function remove(name: string) {
  removing.value = name
  error.value = null
  try {
    await api.removeBackup(name)
    await load()
  } catch (cause) {
    error.value = message(cause)
  } finally {
    removing.value = null
  }
}
</script>

<template>
  <div class="data">
    <DouAlert v-if="error" tone="danger" closable @close="error = null">{{ error }}</DouAlert>
    <div v-if="!usage" class="data-loading"><DouSpinner /></div>
    <template v-else>
      <DouDescriptions :columns="1" layout="horizontal" label-width="120px">
        <DouDescriptionsItem :label="t('data.home')">
          <DouText size="sm" class="data-path">{{ usage.home }}</DouText>
        </DouDescriptionsItem>
        <DouDescriptionsItem :label="t('data.database')">
          {{ formatBytes(usage.database) }}
        </DouDescriptionsItem>
        <DouDescriptionsItem v-if="usage.plugins !== null" :label="t('data.plugins')">
          {{ formatBytes(usage.plugins) }}
        </DouDescriptionsItem>
      </DouDescriptions>
      <div class="data-head">
        <DouText weight="medium" size="sm">{{ t('data.backups') }}</DouText>
        <DouButton size="sm" variant="ghost" :loading="backingUp" @click="backup">
          <template #leading><DatabasePlusIcon /></template>
          {{ t('data.backup') }}
        </DouButton>
      </div>
      <DouEmpty v-if="!usage.backups.length" size="sm" :description="t('data.noBackups')" />
      <ul v-else class="data-list">
        <li v-for="item in usage.backups" :key="item.name" class="data-row">
          <div class="data-main">
            <DouText size="sm">
              {{ time(item.createdAt) }}
              <DouText v-if="item.auto" variant="tertiary">{{ t('data.auto') }}</DouText>
            </DouText>
            <DouText
              size="sm"
              variant="tertiary"
              truncate
              :title="`${usage.backupsDir}/${item.name}`"
            >
              {{ item.name }}
            </DouText>
          </div>
          <DouText size="sm" variant="secondary" class="data-size">
            {{ formatBytes(item.size) }}
          </DouText>
          <DouPopconfirm
            :title="t('data.removeConfirm')"
            tone="danger"
            :confirm-text="t('settings.remove')"
            @confirm="remove(item.name)"
          >
            <DouButton
              size="sm"
              variant="ghost"
              tone="danger"
              :loading="removing === item.name"
              :aria-label="t('settings.remove')"
              :title="t('settings.remove')"
            >
              <TrashIcon />
            </DouButton>
          </DouPopconfirm>
        </li>
      </ul>
    </template>
  </div>
</template>

<style scoped>
.data {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.data-loading {
  display: grid;
  place-items: center;
  padding: 24px 0;
}

.data-path {
  overflow-wrap: anywhere;
  font-family: var(--dou-font-family-mono);
}

.data-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.data-list {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}

.data-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.data-row:last-child {
  border-bottom: none;
}

.data-main {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
}

.data-size {
  flex: none;
  white-space: nowrap;
}
</style>
