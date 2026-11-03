<script setup lang="ts">
import type { DashboardContent } from '@ariadne/core'
import {
  DouDashboard,
  DouDashboardPalette,
  migrateDocument,
  type DashboardDocument,
  type DashboardExpose,
  type DashboardSources,
} from '@dou-ui/dashboard'
import { chartWidget } from '@dou-ui/dashboard/chart'
import { pivotWidget } from '@dou-ui/dashboard/pivot'
import { statWidget, tableWidget, textWidget } from '@dou-ui/dashboard/widgets'
import '@dou-ui/dashboard/style.css'
import '@dou-ui/chart/style.css'
import '@dou-ui/pivot/style.css'
import { DouSwitch, DouTag, DouText } from 'dou-ui'
import { computed, ref, shallowRef, watch } from 'vue'
import { locale, t } from '../i18n'

/**
 * A dashboard snapshot. The data was copied in when it was taken, so each source just
 * hands back its stored value and nothing is fetched.
 */
const props = defineProps<{ content: string; readonly: boolean; label?: string }>()
const emit = defineEmits<{ draft: [content: string | null] }>()

const widgets = [statWidget(), textWidget(), tableWidget(), chartWidget(), pivotWidget()]
const snapshot = shallowRef<DashboardContent>({
  version: 1,
  dashboard: { version: 1, widgets: [] },
  data: {},
})
const doc = shallowRef<DashboardDocument>({ version: 1, widgets: [] })
const sources = shallowRef<DashboardSources>({})
const editing = ref(false)
const dashboard = ref<DashboardExpose | null>(null)
const generation = ref(0)

watch(
  () => props.content,
  (content) => {
    const value = JSON.parse(content) as DashboardContent
    snapshot.value = value
    doc.value = migrateDocument(value.dashboard)
    // Defined once per load: a new function per render would count as a new source.
    sources.value = Object.fromEntries(
      Object.entries(value.data).map(([name, data]) => [name, () => data]),
    )
    generation.value++
  },
  { immediate: true },
)
watch(
  () => props.readonly,
  (readonly) => {
    if (readonly) editing.value = false
  },
)

function changed() {
  emit('draft', JSON.stringify({ ...snapshot.value, dashboard: doc.value }))
}

const takenAt = computed(() =>
  snapshot.value.takenAt
    ? new Date(snapshot.value.takenAt).toLocaleString(locale.value, {
        dateStyle: 'medium',
        timeStyle: 'short',
      })
    : null,
)
</script>

<template>
  <div class="dashboard">
    <div class="dashboard-bar">
      <DouTag size="sm" tone="neutral">{{ t('artifact.dashboard.snapshot') }}</DouTag>
      <DouText variant="tertiary" size="sm" class="dashboard-taken">
        {{ takenAt ? t('artifact.dashboard.takenAt', { time: takenAt }) : '' }}
      </DouText>
      <label v-if="!readonly" class="dashboard-edit">
        <DouText size="sm">{{ t('artifact.dashboard.edit') }}</DouText>
        <DouSwitch v-model="editing" size="sm" />
      </label>
    </div>
    <div class="dashboard-main">
      <DouDashboardPalette v-if="editing" :for="dashboard" class="dashboard-palette" />
      <div class="dashboard-grid">
        <DouDashboard
          ref="dashboard"
          :key="generation"
          v-model="doc"
          :widgets="widgets"
          :sources="sources"
          :editable="editing"
          :label="label"
          @change="changed"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.dashboard {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.dashboard-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
}

.dashboard-taken {
  flex: 1;
  min-width: 0;
}

.dashboard-edit {
  display: flex;
  align-items: center;
  gap: 6px;
}

.dashboard-main {
  display: flex;
  flex-direction: column;
  flex: 1;
  gap: 12px;
  min-height: 0;
  padding: 0 16px 16px;
}

.dashboard-palette {
  flex: none;
}

.dashboard-grid {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
</style>
