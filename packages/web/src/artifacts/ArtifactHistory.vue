<script setup lang="ts">
import type { ArtifactVersionView } from '@ariadne/core'
import { CheckIcon, RotateCwIcon } from '@dou-ui/icons'
import { DouButton, DouSpinner, DouText } from 'dou-ui'
import { onMounted, useTemplateRef } from 'vue'
import { locale, t } from '../i18n'
import { authorName } from './author'

defineProps<{
  versions: ArtifactVersionView[]
  selected?: number
  status: 'loading' | 'ready' | 'error'
}>()
const emit = defineEmits<{ select: [version: number]; retry: [] }>()
const panel = useTemplateRef<HTMLElement>('panel')
onMounted(() => panel.value?.focus({ preventScroll: true }))

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div ref="panel" class="history-panel" tabindex="-1">
    <div v-if="status === 'loading'" class="history-state"><DouSpinner size="sm" /></div>
    <div v-else-if="status === 'error'" class="history-state history-error">
      <DouText variant="secondary">{{ t('artifact.historyUnavailable') }}</DouText>
      <DouButton size="sm" variant="outline" @click="emit('retry')">
        <template #leading><RotateCwIcon /></template>
        {{ t('conversation.retry') }}
      </DouButton>
    </div>
    <DouText v-else-if="!versions.length" variant="secondary" class="history-state">
      {{ t('artifact.historyEmpty') }}
    </DouText>
    <ol v-else class="history-list">
      <li v-for="(version, index) in versions" :key="version.version">
        <button
          type="button"
          class="history-row"
          :aria-current="version.version === selected ? 'true' : undefined"
          @click="emit('select', version.version)"
        >
          <span class="history-detail">
            <span class="history-heading">
              <DouText weight="medium">{{
                t('artifact.version', { version: version.version })
              }}</DouText>
              <DouText v-if="index === 0" size="sm" variant="tertiary">
                {{ t('artifact.current') }}
              </DouText>
            </span>
            <DouText size="sm" variant="secondary" class="history-meta">
              {{ authorName(version.author) }} · {{ time(version.createdAt) }}
            </DouText>
            <DouText v-if="version.reason" size="sm" variant="secondary" class="history-reason">
              {{ version.reason }}
            </DouText>
          </span>
          <CheckIcon v-if="version.version === selected" aria-hidden="true" />
        </button>
      </li>
    </ol>
  </div>
</template>

<style scoped>
.history-panel {
  max-block-size: min(340px, 50dvh);
  overflow-y: auto;
  overscroll-behavior: contain;
  outline: none;
}

.history-state {
  display: block;
  padding: 24px 0;
  text-align: center;
}

.history-error {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.history-list {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.history-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 1em;
  align-items: center;
  gap: 8px;
  inline-size: 100%;
  padding: 10px 12px;
  border: 0;
  border-radius: var(--dou-radius-control);
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  line-height: 1.35;
  cursor: pointer;
}

.history-row:hover {
  background: var(--dou-color-neutral-soft);
}

.history-row:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.history-detail {
  display: grid;
  min-width: 0;
  gap: 3px;
}

.history-heading {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
}

.history-reason,
.history-meta {
  overflow-wrap: anywhere;
}
</style>
