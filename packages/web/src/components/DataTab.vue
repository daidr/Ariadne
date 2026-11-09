<script setup lang="ts">
import { DouText } from 'dou-ui'
import { t } from '../i18n'
import DataSettings from './DataSettings.vue'
import PluginSettings from './PluginSettings.vue'
import WorktreeSettings from './WorktreeSettings.vue'

/**
 * What Ariadne keeps on this machine and how it runs there: data and backups, threads'
 * worktrees, and plugins loaded from a folder. Backups, removals and plugins act at once.
 */
const emit = defineEmits<{ pluginsChanged: [] }>()
</script>

<template>
  <div class="data-tab">
    <DataSettings />
    <section class="data-tab-section">
      <DouText weight="medium">{{ t('settings.tab.worktrees') }}</DouText>
      <WorktreeSettings />
    </section>
    <section class="data-tab-section">
      <DouText weight="medium">{{ t('settings.tab.plugins') }}</DouText>
      <PluginSettings @changed="emit('pluginsChanged')" />
    </section>
  </div>
</template>

<style scoped>
.data-tab {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.data-tab-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
