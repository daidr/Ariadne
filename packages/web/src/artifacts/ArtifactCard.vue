<script setup lang="ts">
import type { ArtifactSummary } from '@ariadne/core'
import { DouAvatar, DouCard, DouText } from 'dou-ui'
import { computed } from 'vue'
import { locale, t } from '../i18n'
import { useServerNow } from '../server-clock'
import { dragArtifact } from '../side/dock'
import ArtifactThumb from './ArtifactThumb.vue'
import { kindIcon } from './kinds'

/** An artifact as a card: a picture of what it holds, its name and when it last changed. */
const props = defineProps<{ artifact: ArtifactSummary; current?: boolean }>()
const now = useServerNow()

/** When it was last changed: the time today, the day this year, the date before. */
const editedLabel = computed(() => {
  const date = new Date(props.artifact.updatedAt)
  const current = new Date(now.value)
  const sameYear = date.getFullYear() === current.getFullYear()
  const sameDay =
    sameYear && date.getMonth() === current.getMonth() && date.getDate() === current.getDate()
  const options: Intl.DateTimeFormatOptions = sameDay
    ? { hour: '2-digit', minute: '2-digit' }
    : sameYear
      ? { month: 'long', day: 'numeric' }
      : { dateStyle: 'medium' }
  return t('artifacts.editedAt', { time: date.toLocaleString(locale.value, options) })
})
</script>

<template>
  <DouCard
    as="button"
    interactive
    padding="none"
    class="artifact-card"
    :class="{ 'artifact-card-current': current }"
    :aria-current="current ? 'page' : undefined"
    @pointerdown="dragArtifact($event, artifact.id)"
  >
    <ArtifactThumb :artifact="artifact" />
    <span class="card-info">
      <DouAvatar shape="square" :size="36" class="card-icon" :class="`card-icon-${artifact.kind}`">
        <component :is="kindIcon[artifact.kind]" />
      </DouAvatar>
      <span class="card-text">
        <DouText weight="medium" truncate>{{ artifact.title }}</DouText>
        <DouText variant="tertiary" size="sm" truncate>
          {{ editedLabel }}
        </DouText>
      </span>
    </span>
  </DouCard>
</template>

<style scoped>
.artifact-card {
  display: flex;
  flex-direction: column;
  min-width: 0;
  overflow: hidden;
  touch-action: pan-y;
}

.card-info {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  padding: 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.card-icon {
  flex: none;
  --dou-avatar-bg: var(--dou-color-primary-soft);
  --dou-avatar-fg: var(--dou-color-primary-text);
  --dou-avatar-font-size: 18px;
}

.card-icon-whiteboard {
  --dou-avatar-bg: var(--dou-color-warning-soft);
  --dou-avatar-fg: var(--dou-color-warning-text);
}

.card-icon-dashboard {
  --dou-avatar-bg: var(--dou-color-success-soft);
  --dou-avatar-fg: var(--dou-color-success-text);
}

.card-text {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.artifact-card-current {
  border-color: var(--dou-color-primary);
}
</style>
