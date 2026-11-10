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
import { collaboration } from '@dou-ui/dashboard/collaboration'
import { pivotWidget } from '@dou-ui/dashboard/pivot'
import { statWidget, tableWidget, textWidget } from '@dou-ui/dashboard/widgets'
import '@dou-ui/dashboard/style.css'
import '@dou-ui/chart/style.css'
import '@dou-ui/pivot/style.css'
import { DouButton, DouSwitch, DouText } from 'dou-ui'
import { RotateCwIcon } from '@dou-ui/icons'
import { api } from '../api'
import { report } from '../store'
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { locale, t } from '../i18n'
import { commentAuthor } from './comment-threads'
import { pendingWidget } from './pending-widget'
import type { SharedArtifact } from './shared'

/**
 * A dashboard snapshot. The data was copied in when it was taken, so each source just
 * hands back its stored value and nothing is fetched. The newest version is edited
 * together through `shared`: its widgets in the `widgets` map, the rest of the snapshot
 * (data, source, when taken) in `snapshot`; an older one is shown read-only from `content`.
 */
const props = defineProps<{
  content: string
  readonly: boolean
  label?: string
  artifactId?: string
  shared?: SharedArtifact
}>()

const own = [statWidget(), textWidget(), tableWidget(), chartWidget(), pivotWidget()]
// An agent's placeholders are shown, not offered among the widgets to add.
const widgets = [...own, pendingWidget()]
const offered = own.map((widget) => widget.type)
const together = props.shared
  ? collaboration({
      widgets: props.shared.doc.getMap('widgets'),
      awareness: props.shared.awareness,
      user: { name: commentAuthor('user').name },
    })
  : undefined
const snapshot = shallowRef<Omit<DashboardContent, 'dashboard'>>({ version: 1, data: {} })
const doc = shallowRef<DashboardDocument>({ version: 1, widgets: [] })
const sources = shallowRef<DashboardSources>({})
const editing = ref(false)
const dashboard = ref<DashboardExpose | null>(null)

/** Take in the snapshot; the sources change only with the data, not with each look at it. */
function take(next: Omit<DashboardContent, 'dashboard'>) {
  const data = JSON.stringify(next.data)
  if (data !== JSON.stringify(snapshot.value.data)) {
    sources.value = Object.fromEntries(
      Object.entries(next.data).map(([name, value]) => [name, () => value]),
    )
  }
  snapshot.value = next
}

if (props.shared) {
  // The room holds it: its data comes in as it is written (a refresh, an agent's change).
  const map = props.shared.doc.getMap('snapshot')
  const read = () => take({ version: 1, data: {}, ...map.toJSON() })
  map.observe(read)
  watch(props.shared.ready, (ready) => ready && read(), { immediate: true })
  onScopeDispose(() => map.unobserve(read))
} else {
  watch(
    () => props.content,
    (content) => {
      const { dashboard: layout, ...rest } = JSON.parse(content) as DashboardContent
      take(rest)
      doc.value = migrateDocument(layout)
    },
    { immediate: true },
  )
}
watch(
  () => props.readonly,
  (readonly) => {
    if (readonly) editing.value = false
  },
)
const ready = computed(() => !props.shared || props.shared.ready.value)

// A live dashboard fetches again on request; the new data comes back as a new version.
const live = computed(() => !!snapshot.value.source)
const refreshing = ref(false)
async function refresh() {
  if (!props.artifactId) return
  refreshing.value = true
  try {
    await api.refreshDashboard(props.artifactId)
  } catch (error) {
    report(error)
  } finally {
    refreshing.value = false
  }
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
      <!-- Live or a snapshot, said in passing: no badge for a state. -->
      <DouText variant="tertiary" size="sm" class="dashboard-taken">
        {{ t(live ? 'artifact.dashboard.live' : 'artifact.dashboard.snapshot')
        }}{{ takenAt ? ` · ${t('artifact.dashboard.takenAt', { time: takenAt })}` : '' }}
      </DouText>
      <DouButton
        v-if="live && !readonly"
        size="sm"
        variant="ghost"
        :loading="refreshing"
        :disabled="editing"
        @click="refresh"
      >
        <template #leading><RotateCwIcon /></template>
        {{ t('artifact.dashboard.refresh') }}
      </DouButton>
      <label v-if="!readonly" class="dashboard-edit">
        <DouText size="sm">{{ t('artifact.dashboard.edit') }}</DouText>
        <DouSwitch v-model="editing" size="sm" />
      </label>
    </div>
    <div class="dashboard-main">
      <DouDashboardPalette
        v-if="editing"
        :for="dashboard"
        :types="offered"
        direction="horizontal"
        class="dashboard-palette"
      />
      <div class="dashboard-grid">
        <DouDashboard
          v-if="ready"
          ref="dashboard"
          v-model="doc"
          :widgets="widgets"
          :sources="sources"
          :collaboration="together"
          :editable="editing"
          :label="label"
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
