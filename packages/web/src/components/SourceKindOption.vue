<script setup lang="ts">
import { DouTooltip } from 'dou-ui'
import type { SourceKind } from '../api'
import { t } from '../i18n'
import { openPluginSettings } from '../plugin-config'

defineProps<{ label?: string | undefined; plugin?: SourceKind['plugin'] }>()
</script>

<template>
  <DouTooltip v-if="plugin && !plugin.ready" placement="right" interactive :hide-delay="220">
    <span class="source-option">{{ label }}</span>
    <template #content>
      <span class="source-tip">
        {{ t('plugins.sourceUnconfigured') }}
        <button class="source-shortcut" type="button" @click.stop="openPluginSettings(plugin.id)">
          {{ t('plugins.openSettings') }}
        </button>
      </span>
    </template>
  </DouTooltip>
  <span v-else>{{ label }}</span>
</template>

<style scoped>
.source-option {
  display: block;
}

.source-tip {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.source-shortcut {
  padding: 0;
  border: 0;
  color: inherit;
  font: inherit;
  text-decoration: underline;
  background: transparent;
  cursor: pointer;
}
</style>
