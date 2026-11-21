<script setup lang="ts">
import {
  AlertTriangleIcon,
  GitMergeIcon,
  GitPullRequestClosedIcon,
  GitPullRequestDraftIcon,
  GitPullRequestIcon,
} from '@dou-ui/icons'
import {
  DouLink,
  DouPopover,
  DouRelativeTime,
  DouSkeleton,
  DouStatusDot,
  DouTag,
  DouText,
  type TagTone,
} from 'dou-ui'
import { computed, shallowRef, useTemplateRef, watch, type Component, type VNode } from 'vue'
import { t } from '../i18n'
import { checksTone } from '../stage'
import { useLinkPreview } from './useLinkPreview'
import PullRequestChecks from './PullRequestChecks.vue'
import { pullRequestLabel, type PullRequestThread } from './tracked-pull-requests'

/**
 * A PR link with a card on hover and keyboard focus.
 * Tracked PRs get their state and checks from the project updates.
 */
const props = withDefaults(
  defineProps<{
    href: string
    label: string
    children?: VNode | null
    tracked?: PullRequestThread
    variant?: 'soft' | 'text' | 'pill'
  }>(),
  { variant: 'soft' },
)

const open = shallowRef(false)
const anchor = useTemplateRef<InstanceType<typeof DouLink>>('anchor')
const link = useLinkPreview(
  () => props.tracked?.pullRequest?.url ?? props.href,
  anchor,
  open,
  () => !props.tracked,
)
const preview = computed(() => (link.value?.kind === 'pull-request' ? link.value : null))
// Expanding CI can move the card away from the pointer while its controls have focus.
const cardFocused = shallowRef(false)
const card = useTemplateRef<HTMLElement>('card')
watch(open, (isOpen) => {
  if (!isOpen) cardFocused.value = false
})

function focusLink(event: FocusEvent) {
  if (!focusCameFromCard(event)) open.value = true
}

function blurLink(event: FocusEvent) {
  if (!focusCameFromCard(event)) open.value = false
}

function focusCameFromCard(event: FocusEvent) {
  return event.relatedTarget instanceof Node && card.value?.contains(event.relatedTarget)
}

function leaveCard(event: FocusEvent) {
  if (focusCameFromCard(event)) return
  cardFocused.value = false
  if (event.relatedTarget !== anchor.value?.$el) open.value = false
}

type State = 'open' | 'draft' | 'merged' | 'closed'
const trackedPR = computed(() => props.tracked?.pullRequest)
const pr = computed(() => trackedPR.value ?? preview.value)
const title = computed(() => trackedPR.value?.title || preview.value?.title)
const state = computed<State | undefined>(() =>
  pr.value?.state === 'open' && preview.value?.state === 'open' && preview.value.draft
    ? 'draft'
    : pr.value?.state,
)
const conflicted = computed(
  () =>
    pr.value?.state === 'open' &&
    Boolean(
      trackedPR.value?.conflicted || (preview.value?.state === 'open' && preview.value.conflicted),
    ),
)
const checks = computed(() => {
  const result = trackedPR.value?.checks.state
  return result && result !== 'none' ? result : null
})
const icons: Record<State, Component> = {
  open: GitPullRequestIcon,
  draft: GitPullRequestDraftIcon,
  merged: GitMergeIcon,
  closed: GitPullRequestClosedIcon,
}
const tones: Record<State, TagTone> = {
  open: 'success',
  draft: 'neutral',
  merged: 'primary',
  closed: 'danger',
}
const linkIcon = computed(() =>
  conflicted.value ? AlertTriangleIcon : icons[state.value ?? 'open'],
)
const linkIconTone = computed<TagTone | undefined>(() =>
  conflicted.value ? 'warning' : state.value ? tones[state.value] : undefined,
)
const description = computed(() => {
  if (!state.value) return undefined
  if (!conflicted.value) return t(`link.pr.${state.value}`)
  return state.value === 'draft'
    ? `${t('link.pr.draft')} · ${t('link.pr.conflicted')}`
    : t('link.pr.conflicted')
})
</script>

<template>
  <DouPopover
    v-model:open="open"
    :trigger="cardFocused ? 'manual' : 'hover'"
    :placement="variant === 'pill' ? 'top-start' : 'top'"
    :width="380"
    :show-delay="300"
  >
    <DouLink
      ref="anchor"
      :href="href"
      target="_blank"
      rel="noopener noreferrer"
      :variant="variant === 'pill' ? 'text' : variant"
      :underline="variant === 'pill' ? 'none' : undefined"
      :class="{
        'inline-ref': variant === 'soft',
        'pr-pill': variant === 'pill',
        'pr-pill-conflicted': variant === 'pill' && conflicted,
      }"
      :aria-description="description"
      :aria-expanded="open"
      @focus="focusLink"
      @blur="blurLink"
    >
      <template #leading>
        <component
          :is="linkIcon"
          :class="linkIconTone && `pr-icon-${linkIconTone}`"
          aria-hidden="true"
        />
      </template>
      <component :is="children" v-if="children" />
      <template v-else>{{ label }}</template>
      <template v-if="variant !== 'soft' && checks" #trailing>
        <DouStatusDot :tone="checksTone(checks)" :label="t(`pr.checks.${checks}`)" size="sm" />
      </template>
    </DouLink>
    <template #content>
      <div
        v-if="pr && state"
        ref="card"
        class="pr-card"
        @focusin="cardFocused = true"
        @focusout="leaveCard"
      >
        <div class="pr-card-head">
          <DouTag v-if="conflicted && state === 'open'" tone="warning" size="sm" round>
            <template #leading><AlertTriangleIcon /></template>
            {{ t('link.pr.conflicted') }}
          </DouTag>
          <template v-else>
            <DouTag :tone="tones[state]" size="sm" round>
              <template #leading><component :is="icons[state]" /></template>
              {{ t(`link.pr.${state}`) }}
            </DouTag>
            <DouTag v-if="conflicted" tone="warning" size="sm" round>
              <template #leading><AlertTriangleIcon /></template>
              {{ t('link.pr.conflicted') }}
            </DouTag>
          </template>
          <DouText variant="secondary" size="sm" class="pr-card-ref">
            {{ preview?.repository }} {{ pullRequestLabel(pr) }}
          </DouText>
          <DouText v-if="preview" variant="tertiary" size="sm">
            <DouRelativeTime :value="preview.at" />
          </DouText>
        </div>
        <DouText v-if="title" class="pr-card-title">{{ title }}</DouText>
        <PullRequestChecks
          v-if="open && tracked && trackedPR?.headSha && checks"
          :thread-id="tracked.id"
          :head="trackedPR.headSha"
          :summary="checks"
        />
        <div v-if="preview" class="pr-card-foot">
          <DouText variant="secondary" size="sm" class="pr-card-author">{{
            preview.author
          }}</DouText>
          <DouText
            v-if="preview.additions !== null && preview.deletions !== null"
            size="sm"
            class="pr-card-diff"
          >
            <span class="pr-card-added">+{{ preview.additions }}</span>
            <span class="pr-card-deleted">−{{ preview.deletions }}</span>
          </DouText>
          <DouText v-if="preview.changedFiles !== null" variant="secondary" size="sm">
            {{ t('link.pr.files', { count: preview.changedFiles }) }}
          </DouText>
        </div>
      </div>
      <DouText v-else-if="link === null" variant="secondary" size="sm">
        {{ t('link.pr.unavailable') }}
      </DouText>
      <DouSkeleton v-else :lines="3" />
    </template>
  </DouPopover>
</template>

<style scoped>
/* Kept apart from the words around it, as MessageLink does. */
.inline-ref {
  margin-inline: 0.25em;
}
.pr-pill {
  flex: none;
  align-items: center;
  gap: var(--dou-control-gap-sm);
  box-sizing: border-box;
  height: var(--dou-control-height-sm);
  padding: 0 var(--dou-control-padding-x-sm);
  border: 1px solid var(--dou-color-border);
  border-radius: 999px;
  background-color: var(--dou-color-bg-surface);
  font-size: var(--dou-font-size-sm);
  font-variant-numeric: tabular-nums;
  line-height: 1;
  white-space: nowrap;
}
.pr-pill:hover {
  border-color: var(--dou-color-border-strong);
  background-color: color-mix(in oklab, var(--dou-color-text) 4%, var(--dou-color-bg-surface));
}
.pr-pill-conflicted {
  border-color: color-mix(in oklab, var(--dou-color-warning) 45%, var(--dou-color-border));
  background-color: var(--dou-color-warning-soft);
}
.pr-pill-conflicted:hover {
  border-color: color-mix(in oklab, var(--dou-color-warning) 65%, var(--dou-color-border));
  background-color: color-mix(in oklab, var(--dou-color-warning) 16%, var(--dou-color-bg-surface));
}
.pr-icon-success {
  color: var(--dou-color-success-text);
}
.pr-icon-warning {
  color: var(--dou-color-warning-text);
}
.pr-icon-primary {
  color: var(--dou-color-primary-text);
}
.pr-icon-danger {
  color: var(--dou-color-danger-text);
}
.pr-icon-neutral {
  color: var(--dou-color-text-tertiary);
}
.pr-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.pr-card-head,
.pr-card-foot {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pr-card-ref,
.pr-card-author {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pr-card-title {
  font-weight: 500;
  overflow-wrap: anywhere;
}
.pr-card-diff {
  display: inline-flex;
  gap: 4px;
  font-variant-numeric: tabular-nums;
}
.pr-card-added {
  color: var(--dou-color-success-text);
}
.pr-card-deleted {
  color: var(--dou-color-danger-text);
}
</style>
