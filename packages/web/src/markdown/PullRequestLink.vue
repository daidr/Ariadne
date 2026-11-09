<script setup lang="ts">
import type { LinkPreview } from '@ariadne/plugin-sdk'
import {
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
  DouTag,
  DouText,
  type TagTone,
} from 'dou-ui'
import { computed, ref, type Component, type VNode } from 'vue'
import { t } from '../i18n'
import { linkPreview } from './link-summaries'

/**
 * A pull request in a message: `owner/name#12` (or the link's own words), with a card of
 * its state, title, author and size when the user points at it. It is a chip: the icon
 * already says it leaves Ariadne, so it has no external mark.
 */
const props = defineProps<{ href: string; label: string; children: VNode | null }>()

const preview = ref<LinkPreview | null | undefined>(undefined)
function load(open: boolean) {
  if (!open) return
  void linkPreview(props.href).then((value) => (preview.value = value))
}

type State = 'open' | 'draft' | 'merged' | 'closed'
const state = computed<State | null>(() => {
  const pr = preview.value
  if (!pr) return null
  return pr.state === 'open' && pr.draft ? 'draft' : pr.state
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
</script>

<template>
  <DouPopover trigger="hover" placement="top" :width="380" :show-delay="300" @update:open="load">
    <DouLink
      :href="href"
      target="_blank"
      rel="noopener noreferrer"
      variant="soft"
      class="inline-ref"
    >
      <template #leading><component :is="state ? icons[state] : GitPullRequestIcon" /></template>
      <component :is="children" v-if="children" />
      <template v-else>{{ label }}</template>
    </DouLink>
    <template #content>
      <div v-if="preview" class="pr-card">
        <div class="pr-card-head">
          <DouTag :tone="tones[state!]" size="sm" round>
            <template #leading><component :is="icons[state!]" /></template>
            {{ t(`link.pr.${state!}`) }}
          </DouTag>
          <DouText variant="secondary" size="sm" class="pr-card-ref">
            {{ preview.repository }} {{ preview.reference ?? `#${preview.number}` }}
          </DouText>
          <DouText variant="tertiary" size="sm"><DouRelativeTime :value="preview.at" /></DouText>
        </div>
        <DouText class="pr-card-title">{{ preview.title }}</DouText>
        <div class="pr-card-foot">
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
      <DouText v-else-if="preview === null" variant="secondary" size="sm">
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
