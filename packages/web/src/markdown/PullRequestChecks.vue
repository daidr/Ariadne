<script setup lang="ts">
import type { ChecksState } from '@ariadne/core'
import { DouButton, DouSpinner, DouStatusDot } from 'dou-ui'
import { shallowRef, useId, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { checksTone } from '../stage'

const props = defineProps<{
  threadId: string
  head: string
  summary: Exclude<ChecksState, 'none'>
}>()
type Check = Awaited<ReturnType<typeof api.pullRequestChecks>>['checks'][number]
const listId = useId()
const expanded = shallowRef(false)
const checks = shallowRef<Check[] | null>()

watch(
  [expanded, () => props.threadId, () => props.head, () => props.summary],
  ([show, id, head], _, cleanup) => {
    if (!show) return
    checks.value = undefined
    const controller = new AbortController()
    cleanup(() => controller.abort())
    void api.pullRequestChecks(id, controller.signal).then(
      (result) => {
        if (!controller.signal.aborted)
          checks.value = result.headSha === head ? result.checks : null
      },
      () => {
        if (!controller.signal.aborted) checks.value = null
      },
    )
  },
)

function status(check: Check) {
  if (check.status === 'in_progress') return 'running'
  if (check.status !== 'completed') return 'queued'
  switch (check.conclusion) {
    case 'success':
      return 'passed'
    case 'skipped':
      return 'skipped'
    case 'cancelled':
      return 'cancelled'
    case 'timed_out':
      return 'timedOut'
    case 'failure':
    case 'action_required':
    case 'error':
    case 'startup_failure':
      return 'failed'
    default:
      return 'finished'
  }
}

const tone = {
  running: 'warning',
  queued: 'neutral',
  passed: 'success',
  skipped: 'neutral',
  cancelled: 'neutral',
  timedOut: 'danger',
  failed: 'danger',
  finished: 'neutral',
} as const
</script>

<template>
  <div class="pr-checks">
    <DouButton
      variant="ghost"
      size="xs"
      :aria-description="t(`pr.checks.${summary}`)"
      :aria-expanded="expanded"
      :aria-controls="listId"
      @click="expanded = !expanded"
    >
      <template #leading><DouStatusDot :tone="checksTone(summary)" size="sm" /></template>
      CI
    </DouButton>
    <div v-show="expanded" :id="listId" class="pr-checks-details">
      <DouSpinner v-if="checks === undefined" size="sm" />
      <span v-else-if="checks === null">{{ t('pr.ci.unavailable') }}</span>
      <span v-else-if="!checks.length">{{ t('pr.ci.empty') }}</span>
      <ul v-else class="pr-check-list" :aria-label="t('pr.ci.list')">
        <li v-for="check in checks" :key="check.id" class="pr-check">
          <span class="pr-check-name">{{ check.name }}</span>
          <DouStatusDot :tone="tone[status(check)]" size="sm" class="pr-check-status">
            {{ t(`pr.ci.${status(check)}`) }}
          </DouStatusDot>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.pr-checks-details {
  margin-block-start: 4px;
  color: var(--dou-color-text-secondary);
  font-size: var(--dou-font-size-sm);
}
.pr-check-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: min(180px, 28dvh);
  overflow-y: auto;
  overscroll-behavior-y: contain;
  list-style: none;
  margin: 0;
  padding: 0 6px;
}
.pr-check {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 12px;
}
.pr-check-name {
  min-width: 0;
  overflow-wrap: anywhere;
}
.pr-check-status {
  flex: none;
}
</style>
