<script setup lang="ts">
import { CheckCircleIcon, CircleDotIcon, MinusCircleIcon, XCircleIcon } from '@dou-ui/icons'
import { DouLink, DouPopover, DouRelativeTime, DouSkeleton, DouTag, DouText } from 'dou-ui'
import { computed, shallowRef, useTemplateRef, type VNode } from 'vue'
import { t } from '../i18n'
import { useLinkPreview } from './useLinkPreview'

const props = withDefaults(
  defineProps<{
    href: string
    label: string
    children?: VNode | null
    variant?: 'text' | 'soft'
  }>(),
  { variant: 'soft' },
)

const anchor = useTemplateRef<InstanceType<typeof DouLink>>('anchor')
const open = shallowRef(false)
const preview = useLinkPreview(() => props.href, anchor, open)
const issue = computed(() => (preview.value?.kind === 'issue' ? preview.value : null))
const status = computed(() => {
  if (issue.value?.state !== 'closed') return issue.value?.state
  if (issue.value.stateReason === 'completed') return 'completed'
  if (issue.value.stateReason === 'not_planned') return 'notPlanned'
  return 'closed'
})
const icons = {
  open: CircleDotIcon,
  completed: CheckCircleIcon,
  notPlanned: MinusCircleIcon,
  closed: XCircleIcon,
}
const icon = computed(() => icons[status.value ?? 'open'])
const statusText = computed(() => status.value && t(`link.issue.${status.value}`))
const labels = computed(() => issue.value?.labels?.slice(0, 4))
</script>

<template>
  <DouPopover v-model:open="open" trigger="hover" placement="top" :show-delay="300" :width="360">
    <DouLink
      ref="anchor"
      :href="href"
      :variant="variant"
      :external="variant === 'text'"
      target="_blank"
      rel="noopener noreferrer"
      :class="variant === 'soft' ? 'inline-issue' : 'issue-list-link'"
      :aria-description="statusText"
      :aria-expanded="open"
      @focus="open = true"
      @blur="open = false"
      @keydown.esc="open = false"
    >
      <template #leading>
        <component :is="icon" :class="status && `issue-icon--${status}`" aria-hidden="true" />
      </template>
      <component :is="children" v-if="children" />
      <template v-else>{{ label }}</template>
    </DouLink>
    <template #content>
      <div v-if="issue" class="issue-card">
        <div class="issue-card-meta">
          <DouTag :tone="status === 'open' ? 'success' : 'neutral'" size="sm" round>
            <template #leading
              ><component :is="icon" :class="status && `issue-icon--${status}`"
            /></template>
            {{ statusText }}
          </DouTag>
          <DouText size="sm" variant="secondary">{{ issue.repository }}#{{ issue.number }}</DouText>
        </div>
        <DouText>{{ issue.title }}</DouText>
        <div v-if="labels?.length || issue.updatedAt" class="issue-card-meta">
          <DouTag v-for="label in labels" :key="label" size="sm">{{ label }}</DouTag>
          <DouText v-if="issue.updatedAt" size="sm" variant="tertiary">
            {{ t('link.issue.updated') }} <DouRelativeTime :value="issue.updatedAt" />
          </DouText>
        </div>
      </div>
      <DouText v-else-if="preview !== undefined" size="sm" variant="secondary">
        {{ t('link.issue.unavailable') }}
      </DouText>
      <DouSkeleton v-else :lines="3" />
    </template>
  </DouPopover>
</template>

<style scoped>
.inline-issue {
  margin-inline: 0.25em;
  text-decoration-line: none;
}
.issue-list-link {
  min-width: 0;
  font-size: var(--dou-font-size-sm);
  overflow-wrap: anywhere;
}
.issue-icon--open {
  color: var(--dou-color-success-text);
}
.issue-icon--completed {
  color: light-dark(#7545c7, #bc97f1);
}
.issue-icon--notPlanned,
.issue-icon--closed {
  color: var(--dou-color-text-tertiary);
}
.issue-card {
  display: grid;
  gap: 8px;
  overflow-wrap: anywhere;
}
.issue-card-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
}
</style>
