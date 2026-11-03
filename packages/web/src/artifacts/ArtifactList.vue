<script setup lang="ts">
import { DouEmpty, DouMenu, DouText } from 'dou-ui'
import { FileTextIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { locale, t } from '../i18n'
import { openMain, state } from '../store'
import { authorName } from './author'

const items = computed(() =>
  state.artifacts.map((artifact) => ({
    key: artifact.id,
    label: artifact.title,
    icon: FileTextIcon,
  })),
)
const current = computed(() => (state.main.kind === 'artifact' ? state.main.id : undefined))
const versionOf = (id: string | number) => state.artifacts.find((a) => a.id === id)?.version ?? 1
const meta = (id: string | number) => {
  const artifact = state.artifacts.find((a) => a.id === id)
  if (!artifact) return ''
  const time = new Date(artifact.updatedAt).toLocaleString(locale.value, {
    dateStyle: 'short',
    timeStyle: 'short',
  })
  return t('artifact.updated', { who: authorName(artifact.updatedBy), time })
}
</script>

<template>
  <DouMenu
    v-if="items.length"
    :model-value="current"
    :items="items"
    @select="(key) => openMain({ kind: 'artifact', id: String(key) })"
  >
    <template #label="{ item }">
      <span class="artifact-item" :title="meta(item.key)">
        <DouText truncate>{{ item.label }}</DouText>
        <DouText variant="tertiary" size="sm" class="artifact-version">
          {{ t('artifact.version', { version: versionOf(item.key) }) }}
        </DouText>
      </span>
    </template>
  </DouMenu>
  <DouEmpty v-else size="sm" :description="t('artifacts.empty')" />
</template>

<style scoped>
.artifact-item {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}

.artifact-version {
  flex: none;
}
</style>
