<script setup lang="ts">
import type { ArtifactVersionView } from '@ariadne/core'
import { CheckIcon } from '@dou-ui/icons'
import { DouButton, DouSpinner, DouText } from 'dou-ui'
import { onMounted, useTemplateRef } from 'vue'
import { locale, t } from '../i18n'
import { authorName } from './author'

defineProps<{
  versions: ArtifactVersionView[]
  selected?: number
  status: 'loading' | 'ready' | 'error'
}>()
const emit = defineEmits<{ select: [version: number] }>()
const panel = useTemplateRef<HTMLElement>('panel')
onMounted(() => panel.value?.focus({ preventScroll: true }))

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, { dateStyle: 'short', timeStyle: 'short' })
</script>

<template>
  <div ref="panel" class="history-panel" tabindex="-1">
    <div v-if="status === 'loading'" class="history-state"><DouSpinner size="sm" /></div>
    <DouText v-else-if="status === 'error'" variant="secondary" class="history-state">
      {{ t('artifact.historyUnavailable') }}
    </DouText>
    <ol v-else class="history-list">
      <li v-for="(version, index) in versions" :key="version.version">
        <DouButton
          variant="ghost"
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
            <DouText size="sm" variant="secondary">
              {{ authorName(version.author) }} · {{ time(version.createdAt) }}
            </DouText>
            <DouText v-if="version.reason" size="sm" variant="secondary" class="history-reason">
              {{ version.reason }}
            </DouText>
          </span>
          <template v-if="version.version === selected" #trailing>
            <CheckIcon aria-hidden="true" />
          </template>
        </DouButton>
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

.history-list {
  display: grid;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.history-row {
  width: 100%;
  height: auto;
  justify-content: space-between;
  padding: 10px 12px;
  text-align: start;
  white-space: normal;
  line-height: 1.35;
}

.history-detail {
  display: grid;
  min-width: 0;
  gap: 3px;
}

.history-heading {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.history-reason {
  overflow-wrap: anywhere;
}
</style>
