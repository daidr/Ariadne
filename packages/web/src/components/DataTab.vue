<script setup lang="ts">
import { DouText } from 'dou-ui'
import { ref } from 'vue'
import { t } from '../i18n'
import DataSettings from './DataSettings.vue'
import PluginSettings from './PluginSettings.vue'
import SandboxSettings from './SandboxSettings.vue'
import WorktreeSettings from './WorktreeSettings.vue'

/**
 * What Ariadne keeps on this machine and how it runs there: data and backups, threads'
 * worktrees, the sandbox, and plugins loaded from a folder. Backups, removals and plugins
 * act at once; the sandbox is saved with the rest of the settings.
 */
const emit = defineEmits<{ pluginsChanged: [] }>()
const sandbox = ref<InstanceType<typeof SandboxSettings> | null>(null)

defineExpose({ save: () => sandbox.value?.save() ?? Promise.resolve(true) })
</script>

<template>
  <div class="data-tab">
    <DataSettings />
    <section class="data-tab-section">
      <DouText weight="medium">{{ t('settings.tab.worktrees') }}</DouText>
      <WorktreeSettings />
    </section>
    <SandboxSettings ref="sandbox" />
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
