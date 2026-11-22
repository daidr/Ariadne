<script setup lang="ts">
import type { ActivityStatus, ProjectActivity } from '@ariadne/core'
import { DouPopover, DouSkeleton, DouStatusDot, DouText } from 'dou-ui'
import { computed, onScopeDispose, ref, shallowRef, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { coalescedRefresh } from '../coalesced-refresh'
import { t } from '../i18n'
import { onLiveEvent, partsOf } from '../store'

const props = defineProps<{ projectId: string; name: string; disabled?: boolean }>()
const open = defineModel<boolean>('open', { required: true })
const popover = useTemplateRef<{ close: () => void }>('popover')
const touchPress = ref(false)
const effectiveDisabled = computed(() => !!props.disabled || touchPress.value)
let suppressOpen = false
let releaseListening = false
const activity = shallowRef<ProjectActivity | null>()
const labels = {
  running: 'project.activity.running',
  queued: 'thread.stage.queued',
  waiting: 'project.activity.waiting',
  idle: 'thread.stage.idle',
} as const satisfies Record<ActivityStatus, string>
const groups = computed(() => {
  if (!activity.value) return null
  return [
    {
      key: 'coordinator',
      rows: [{ id: 'coordinator', title: t('coordinator.title'), ...activity.value.coordinator }],
    },
    { key: 'threads', label: t('panel.threads'), rows: activity.value.threads },
  ]
})

function clearTouchPress() {
  touchPress.value = false
  if (releaseListening && typeof window !== 'undefined') {
    window.removeEventListener('pointerup', clearTouchPress, true)
    window.removeEventListener('pointercancel', clearTouchPress, true)
    releaseListening = false
  }
}

onScopeDispose(clearTouchPress)

function trackPointer(event: PointerEvent) {
  if (event.type === 'pointerdown') {
    if (event.pointerType === 'touch' || event.pointerType === 'pen') {
      touchPress.value = true
      if (!releaseListening && typeof window !== 'undefined') {
        window.addEventListener('pointerup', clearTouchPress, true)
        window.addEventListener('pointercancel', clearTouchPress, true)
        releaseListening = true
      }
    } else {
      clearTouchPress()
    }
  } else if (event.type === 'pointercancel') {
    clearTouchPress()
  } else if (event.type === 'pointerenter' && event.pointerType === 'mouse') {
    clearTouchPress()
  }
}

function inspect(event: FocusEvent) {
  if (!effectiveDisabled.value && (event.currentTarget as HTMLElement).matches(':focus-visible')) {
    open.value = true
  }
}

function dismiss() {
  suppressOpen = true
  queueMicrotask(() => {
    suppressOpen = false
    clearTouchPress()
  })
  popover.value?.close()
  open.value = false
}

function updateOpen(value: boolean) {
  if (value && (effectiveDisabled.value || suppressOpen)) {
    popover.value?.close()
    open.value = false
    return
  }
  open.value = value
}

watch(effectiveDisabled, (disabled) => {
  if (!disabled) return
  popover.value?.close()
  open.value = false
})

watch([open, () => props.projectId], ([visible, id], _, onCleanup) => {
  if (!visible) return
  activity.value = undefined
  const controller = new AbortController()
  const { signal } = controller
  const refresh = coalescedRefresh<never, void>(async () => {
    if (signal.aborted) return
    try {
      const result = await api.getProjectActivity(id, signal)
      if (!signal.aborted) activity.value = result
    } catch {
      if (!signal.aborted) activity.value = null
    }
  })
  const unsubscribe = onLiveEvent((event) => {
    if (
      event.projectId === id &&
      event.type !== 'turn.step' &&
      event.type !== 'tool.finished' &&
      partsOf(event.type).includes('detail')
    ) {
      void refresh()
    }
  })
  onCleanup(() => {
    controller.abort()
    unsubscribe()
  })
  void refresh()
})
</script>

<template>
  <DouPopover
    ref="popover"
    :open="open"
    :disabled="effectiveDisabled"
    trigger="hover"
    placement="right-start"
    :width="308"
    :show-delay="400"
    :hide-delay="150"
    :title="name"
    :aria-label="t('project.activity')"
    @update:open="updateOpen"
  >
    <slot :inspect="inspect" :dismiss="dismiss" :track-pointer="trackPointer" />
    <template #content>
      <div v-if="groups" class="project-activity">
        <div v-for="group in groups" :key="group.key" class="activity-group">
          <DouText v-if="group.label" variant="secondary" size="sm">{{ group.label }}</DouText>
          <ul v-if="group.rows.length" class="activity-list">
            <li v-for="row in group.rows" :key="row.id" class="activity-row">
              <span class="activity-title" :title="group.label ? row.title : undefined">{{
                row.title
              }}</span>
              <DouText variant="secondary" size="sm" class="activity-status">
                <template v-if="row.status === 'running'">
                  <span v-if="row.progress">{{ t('thread.progressShort', row.progress) }}</span>
                  <DouStatusDot tone="primary" role="img" :aria-label="t(labels.running)" />
                </template>
                <template v-else>{{ t(labels[row.status]) }}</template>
              </DouText>
            </li>
          </ul>
          <DouText v-else variant="tertiary" size="sm">{{
            t('project.activity.noThreads')
          }}</DouText>
        </div>
      </div>
      <DouText v-else-if="activity === null" variant="secondary" size="sm">
        {{ t('project.activity.unavailable') }}
      </DouText>
      <DouSkeleton v-else :lines="3" />
    </template>
  </DouPopover>
</template>

<style scoped>
.project-activity,
.activity-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.activity-group + .activity-group {
  padding-top: 8px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.activity-list {
  display: grid;
  gap: 6px;
  max-height: 220px;
  margin: 0;
  padding: 0;
  overflow: auto;
  list-style: none;
}

.activity-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
  font-size: var(--dou-font-size-sm);
}

.activity-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.activity-status {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 6px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
</style>
