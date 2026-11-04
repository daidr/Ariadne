<script setup lang="ts">
import { DouBadge, DouTag, DouText, DouTooltip } from 'dou-ui'
import {
  AlertCircleIcon,
  CheckCircleIcon,
  CircleDashedIcon,
  CircleHalfIcon,
  GitMergeIcon,
  GitPullRequestClosedIcon,
  GitPullRequestIcon,
  PauseCircleIcon,
  UserCircleIcon,
} from '@dou-ui/icons'
import { computed, type Component } from 'vue'
import { t } from '../i18n'
import { checksTone } from '../stage'
import type { PlanNodeData } from './layout'
import StageIcon from '../components/StageIcon.vue'
import ThreadHeadline from '../components/ThreadHeadline.vue'

const props = defineProps<{ data: PlanNodeData }>()

const icons: Record<PlanNodeData['status'], Component> = {
  none: CircleDashedIcon,
  running: CircleHalfIcon,
  waiting: UserCircleIcon,
  error: AlertCircleIcon,
  paused: PauseCircleIcon,
  done: CheckCircleIcon,
}
const tone = {
  none: 'neutral',
  running: 'primary',
  waiting: 'warning',
  error: 'danger',
  paused: 'neutral',
  done: 'success',
} as const

/** A node shows two threads; the rest are counted and listed in the side panel. */
const SHOWN = 2
const shown = computed(() => props.data.threads.slice(0, SHOWN))
const waiting = computed(() => props.data.status === 'waiting')
const status = computed(() =>
  waiting.value ? t('plan.waiting') : t(`plan.status.${props.data.status}`),
)
/** What a change does or did to the node, as a dot named on hover. */
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
      'plan-node-proposed': data.proposed,
      [`plan-node-${data.mark}`]: !!data.mark,
    }"
  >
    <div class="plan-node-head">
      <DouText size="sm" variant="tertiary">{{ t(`plan.kind.${data.kind}`) }}</DouText>
      <span class="plan-node-tags">
        <DouTooltip v-if="mark" :content="mark.label">
          <span class="plan-node-mark" role="img" :aria-label="mark.label">
            <DouBadge dot :tone="mark.tone" />
          </span>
        </DouTooltip>
        <DouTooltip :content="status">
          <DouText
            :tone="tone[data.status]"
            :variant="tone[data.status] === 'neutral' ? 'tertiary' : 'default'"
            class="plan-node-status"
            role="img"
            :aria-label="status"
          >
            <component :is="icons[data.status]" aria-hidden="true" />
          </DouText>
        </DouTooltip>
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
            {{ thread.pullRequest.reference ?? `#${thread.pullRequest.number}` }}
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
          <StageIcon :stage="thread.stage" />
        </span>
        <ThreadHeadline
          class="plan-node-headline"
          :stage="thread.stage"
          :headline="thread.lastHeadline"
          :lines="1"
        />
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
  background: var(--dou-color-warning-soft);
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
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0 8px;
}

/* A node is laid out at a fixed spacing, so its headline keeps to one line. */
.plan-node-headline {
  flex-basis: 100%;
  min-width: 0;
}

.plan-node-tags {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  gap: 4px;
}

.plan-node-mark,
.plan-node-status {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}

.plan-node-status {
  font-size: 16px;
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
  font-size: var(--dou-font-size-sm);
  color: var(--dou-color-text-secondary);
}
</style>
