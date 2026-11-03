<script setup lang="ts">
import { DouTag, DouText } from 'dou-ui'
import { GitMergeIcon, GitPullRequestClosedIcon, GitPullRequestIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { checksTone, stageTone } from '../stage'
import type { PlanNodeData } from './layout'

const props = defineProps<{ data: PlanNodeData }>()

const tone = {
  none: 'neutral',
  process: 'primary',
  wait: 'warning',
  error: 'danger',
  finish: 'success',
} as const

/** A node shows two threads; the rest are counted and listed in the side panel. */
const SHOWN = 2
const shown = computed(() => props.data.threads.slice(0, SHOWN))
const waiting = computed(() => props.data.status === 'wait')
const mark = computed(() => {
  if (props.data.mark) {
    const tones = { added: 'success', removed: 'danger', changed: 'primary' } as const
    return { label: t(`plan.mark.${props.data.mark}`), tone: tones[props.data.mark] }
  }
  if (props.data.proposed) return { label: t('plan.mark.proposed'), tone: 'warning' as const }
  if (props.data.fresh) return { label: t('plan.mark.fresh'), tone: 'primary' as const }
  return null
})
</script>

<template>
  <div
    class="plan-node"
    :class="{
      'plan-node-wait': waiting,
      'plan-node-fresh': data.fresh,
      'plan-node-proposed': data.proposed,
      [`plan-node-${data.mark}`]: !!data.mark,
    }"
  >
    <div class="plan-node-head">
      <DouText size="sm" variant="tertiary">{{ t(`plan.kind.${data.kind}`) }}</DouText>
      <span class="plan-node-tags">
        <DouTag v-if="mark" size="sm" variant="outline" :tone="mark.tone">{{ mark.label }}</DouTag>
        <DouTag v-if="waiting" size="sm" variant="solid" tone="warning">
          {{ t('plan.waiting') }}
        </DouTag>
        <DouTag v-else size="sm" :tone="tone[data.status]">
          {{ t(`plan.status.${data.status}`) }}
        </DouTag>
      </span>
    </div>
    <DouText class="plan-node-title" weight="medium">{{ data.title }}</DouText>
    <ul v-if="data.threads.length" class="plan-node-threads">
      <li v-for="thread in shown" :key="thread.id" class="plan-node-thread">
        <DouText size="sm" class="plan-node-thread-title" :title="thread.title">
          {{ thread.title }}
        </DouText>
        <span class="plan-node-tags">
          <span v-if="thread.pullRequest" class="plan-node-pr">
            <GitMergeIcon v-if="thread.pullRequest.state === 'merged'" />
            <GitPullRequestClosedIcon v-else-if="thread.pullRequest.state === 'closed'" />
            <GitPullRequestIcon v-else />
            #{{ thread.pullRequest.number }}
          </span>
          <DouTag
            v-if="
              thread.pullRequest?.state === 'open' && thread.pullRequest.checks.state !== 'none'
            "
            size="sm"
            :tone="checksTone(thread.pullRequest.checks.state)"
            :title="thread.pullRequest.checks.failed.join(', ')"
          >
            {{ t(`pr.checks.${thread.pullRequest.checks.state}`) }}
          </DouTag>
          <DouTag size="sm" :tone="stageTone[thread.stage]">
            {{ t(`thread.stage.${thread.stage}`) }}
          </DouTag>
        </span>
      </li>
      <li v-if="data.threads.length > SHOWN">
        <DouText size="sm" variant="tertiary">
          {{ t('plan.moreThreads', { count: data.threads.length - SHOWN }) }}
        </DouText>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.plan-node {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 248px;
  padding: 8px 12px 10px;
  border-radius: var(--dou-radius-container);
}

/* A node waiting on the user stands out at a glance, before any text is read. */
.plan-node-wait {
  background: color-mix(in srgb, var(--dou-color-warning) 12%, transparent);
  box-shadow: inset 3px 0 0 var(--dou-color-warning);
}

/* Changed by the coordinator since the user last looked. */
.plan-node-fresh {
  outline: 2px solid var(--dou-color-primary);
  outline-offset: 2px;
}

/* Touched by a change that waits on the user's yes. */
.plan-node-proposed {
  outline: 2px dashed var(--dou-color-warning);
  outline-offset: 2px;
}

/* What a previewed change would do. */
.plan-node-added {
  outline: 2px dashed var(--dou-color-success);
  outline-offset: 2px;
}

.plan-node-changed {
  outline: 2px dashed var(--dou-color-primary);
  outline-offset: 2px;
}

.plan-node-removed {
  outline: 2px dashed var(--dou-color-danger);
  outline-offset: 2px;
  opacity: 0.6;
}

.plan-node-removed .plan-node-title {
  text-decoration: line-through;
}

.plan-node-head,
.plan-node-thread {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.plan-node-tags {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  gap: 4px;
}

.plan-node-title {
  overflow-wrap: anywhere;
}

.plan-node-threads {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 2px 0 0;
  padding: 4px 0 0;
  list-style: none;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.plan-node-thread-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.plan-node-pr {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 12px;
  color: var(--dou-color-text-secondary, inherit);
}

.plan-node-pr :deep(svg) {
  width: 12px;
  height: 12px;
}
</style>
