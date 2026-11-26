<script setup lang="ts">
import { ActivityIcon } from '@dou-ui/icons'
import { DouButton, DouStatusDot } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { backMain, openMain, state } from '../store'

const props = defineProps<{ compact?: boolean }>()

const active = computed(() => state.main.kind === 'pulse')

const currentProject = computed(
  () => state.projects.find((project) => project.id === state.currentId) ?? null,
)

const runningCount = computed(() => {
  const threads = state.detail?.threads ?? []
  const workingThreads = threads.filter((thread) => thread.stage === 'working').length
  const coordinatorBusy = state.coordinator.conversation?.turn ? 1 : 0
  return Math.max(workingThreads + coordinatorBusy, currentProject.value?.runningTurns ?? 0)
})

const queuedCount = computed(
  () => (state.detail?.threads ?? []).filter((thread) => thread.stage === 'queued').length,
)

const needsCount = computed(() => state.detail?.needsMe.length ?? 0)

const summaryParts = computed(() => {
  const parts: string[] = []
  if (runningCount.value > 0) parts.push(t('pulse.pill.running', { count: runningCount.value }))
  if (queuedCount.value > 0) parts.push(t('pulse.pill.queued', { count: queuedCount.value }))
  if (needsCount.value > 0) parts.push(t('pulse.pill.attention', { count: needsCount.value }))
  return parts
})

const buttonLabel = computed(() => {
  if (active.value) return t('pulse.close')
  if (summaryParts.value.length) {
    return t('pulse.pill.summary', { detail: summaryParts.value.join(' · ') })
  }
  return t('pulse.open')
})

const visibleText = computed(() => {
  if (props.compact) {
    return runningCount.value > 0 ? String(runningCount.value) : null
  }
  if (runningCount.value > 0) return t('pulse.pill.running', { count: runningCount.value })
  if (queuedCount.value > 0) return t('pulse.pill.queued', { count: queuedCount.value })
  return t('pulse.pill.idle')
})

function togglePulse() {
  if (active.value) backMain()
  else openMain({ kind: 'pulse' })
}
</script>

<template>
  <DouButton
    :variant="active || runningCount > 0 ? 'soft' : 'ghost'"
    :tone="active || runningCount > 0 ? 'primary' : 'neutral'"
    size="sm"
    class="pulse-activity-pill"
    :class="{ 'is-active': active, 'is-running': runningCount > 0 }"
    data-pulse-pill
    :aria-pressed="active"
    :aria-label="buttonLabel"
    :title="buttonLabel"
    @click="togglePulse"
  >
    <template #leading>
      <DouStatusDot v-if="runningCount > 0" tone="primary" ripple aria-hidden="true" />
      <ActivityIcon v-else aria-hidden="true" />
    </template>
    <span v-if="visibleText" class="pulse-pill-text">{{ visibleText }}</span>
  </DouButton>
</template>

<style scoped>
.pulse-activity-pill {
  border-radius: 9999px;
}

.pulse-pill-text {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
</style>
