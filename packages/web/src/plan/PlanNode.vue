<script setup lang="ts">
import { DouBadge, DouTag, DouText, DouTooltip } from 'dou-ui'
import {
  AlertTriangleIcon,
  FlagIcon,
  GitMergeIcon,
  GitPullRequestClosedIcon,
  GitPullRequestIcon,
  SignpostIcon,
} from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { checksTone, questionOf, showsHeadline, statusLine } from '../stage'
import { state } from '../store'
import type { PlanNodeData } from './layout'
import StageIcon from '../components/StageIcon.vue'
import PlanStatusIcon from './PlanStatusIcon.vue'
import ThreadHeadline from '../components/ThreadHeadline.vue'

const props = defineProps<{ data: PlanNodeData }>()

/** A node shows two threads; the rest are counted and listed in the side panel. */
const SHOWN = 2
const shown = computed(() => props.data.threads.slice(0, SHOWN))
const waiting = computed(() => props.data.status === 'waiting')
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
// Most nodes are tasks: only the other kinds say what they are.
const kindIcon = computed(() =>
  props.data.kind === 'milestone' ? FlagIcon : props.data.kind === 'decision' ? SignpostIcon : null,
)
const ahead = computed(() =>
  props.data.ahead.length ? t('plan.ahead', { titles: props.data.ahead.join('、') }) : null,
)
/** A thread named as its node only adds its state, which the icon already gives. */
const named = (title: string) => title.trim() !== props.data.title.trim()
const hasHeadline = (thread: PlanNodeData['threads'][number]) =>
  !!thread.lastHeadline && showsHeadline(thread.stage)
/** For a thread named as its node: what it is doing or asks, from the live thread. */
function lineOf(id: string): string | null {
  const thread = state.detail?.threads.find((item) => item.id === id)
  return thread ? (statusLine(thread, questionOf(id, state.detail?.needsMe))?.text ?? null) : null
}
</script>

<template>
  <div
    class="plan-node"
    :class="{
      'plan-node-compact': data.compact,
      'plan-node-wait': waiting,
      'plan-node-proposed': data.proposed,
      [`plan-node-${data.mark}`]: !!data.mark,
      [`plan-node-${data.kind}`]: true,
    }"
  >
    <div class="plan-node-head">
      <span class="plan-node-name">
        <DouTooltip v-if="kindIcon" :content="t(`plan.kind.${data.kind}`)">
          <DouText
            variant="tertiary"
            class="plan-node-kind"
            role="img"
            :aria-label="t(`plan.kind.${data.kind}`)"
          >
            <component :is="kindIcon" aria-hidden="true" />
          </DouText>
        </DouTooltip>
        <DouText class="plan-node-title" :size="data.compact ? 'sm' : undefined" weight="medium">
          {{ data.title }}
        </DouText>
      </span>
      <span class="plan-node-tags">
        <DouTooltip v-if="ahead" :content="ahead">
          <DouText tone="warning" class="plan-node-ahead" role="img" :aria-label="ahead">
            <AlertTriangleIcon aria-hidden="true" />
          </DouText>
        </DouTooltip>
        <DouTooltip v-if="mark" :content="mark.label">
          <span class="plan-node-mark" role="img" :aria-label="mark.label">
            <DouBadge dot :tone="mark.tone" />
          </span>
        </DouTooltip>
        <PlanStatusIcon :status="data.status" />
      </span>
    </div>
    <ul v-if="data.threads.length && !data.compact" class="plan-node-threads">
      <li v-for="thread in shown" :key="thread.id" class="plan-node-thread">
        <DouText
          v-if="named(thread.title)"
          size="sm"
          class="plan-node-thread-title"
          :title="thread.title"
        >
          {{ thread.title }}
        </DouText>
        <DouText
          v-else-if="!hasHeadline(thread)"
          size="sm"
          variant="secondary"
          class="plan-node-thread-title"
          :title="lineOf(thread.id) ?? thread.title"
        >
          {{ lineOf(thread.id) ?? t(`thread.stage.${thread.stage}`) }}
        </DouText>
        <ThreadHeadline
          v-else
          class="plan-node-thread-title"
          :stage="thread.stage"
          :headline="thread.lastHeadline"
          :lines="1"
        />
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
          v-if="named(thread.title)"
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

.plan-node-compact {
  width: 152px;
  padding: 6px 12px;
}

.plan-node-name {
  display: inline-flex;
  flex: 1;
  align-items: first baseline;
  gap: 4px;
  min-width: 0;
}

.plan-node-kind,
.plan-node-ahead {
  display: inline-flex;
  flex: none;
  align-items: center;
  font-size: 14px;
}

/*
 * The kind icon and the status icons sit on the title's first line, centred on its letters:
 * each box is one cap tall and shares the title's first baseline. A box as tall as an
 * assumed line height centres on the line box instead, which Windows' fonts fill with more
 * room above the letters than below, so the icons sat high there. Layout containment keeps
 * the icons inside from lending the box a baseline of their own.
 */
.plan-node-kind,
.plan-node-head > .plan-node-tags {
  height: 1cap;
  contain: layout;
}

.plan-node-head {
  display: flex;
  align-items: first baseline;
  justify-content: space-between;
  gap: 8px;
}

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

.plan-node-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
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
