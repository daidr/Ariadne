<script setup lang="ts">
import type { ArtifactSummary } from '@ariadne/core'
import { DouAvatar, DouCard, DouText } from 'dou-ui'
import { locale, t } from '../i18n'
import ArtifactThumb from './ArtifactThumb.vue'
import { kindIcon } from './kinds'

/** An artifact as a card: a picture of what it holds, its name and when it last changed. */
defineProps<{ artifact: ArtifactSummary; current?: boolean }>()

/** When it was last changed: the time today, the day this year, the date before. */
function editedAt(at: number): string {
  const date = new Date(at)
  const now = new Date()
  const options: Intl.DateTimeFormatOptions =
    date.toDateString() === now.toDateString()
      ? { hour: '2-digit', minute: '2-digit' }
      : date.getFullYear() === now.getFullYear()
        ? { month: 'long', day: 'numeric' }
        : { dateStyle: 'medium' }
  return t('artifacts.editedAt', { time: date.toLocaleString(locale.value, options) })
}
</script>

<template>
  <DouCard
    as="button"
    interactive
    padding="none"
    class="artifact-card"
    :class="{ 'artifact-card-current': current }"
    :aria-current="current ? 'page' : undefined"
  >
    <ArtifactThumb :preview="artifact.preview" />
    <div class="card-info">
      <DouAvatar shape="square" :size="36" class="card-icon" :class="`card-icon-${artifact.kind}`">
        <component :is="kindIcon[artifact.kind]" />
      </DouAvatar>
      <span class="card-text">
        <DouText weight="medium" truncate>{{ artifact.title }}</DouText>
        <DouText variant="tertiary" size="sm" truncate>
          {{ editedAt(artifact.updatedAt) }}
        </DouText>
      </span>
    </div>
  </DouCard>
</template>

<style scoped>
.artifact-card {
  display: flex;
  flex-direction: column;
  min-width: 0;
  overflow: hidden;
  /* DouCard as a button keeps the browser's button padding, font and centring. */
  padding: 0;
  text-align: left;
  font: inherit;
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
