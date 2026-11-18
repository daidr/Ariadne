<script setup lang="ts">
import { InfoIcon } from '@dou-ui/icons'
import { DouTooltip } from 'dou-ui'
import { ref } from 'vue'
import { t } from '../i18n'

defineProps<{ label: string; content: string }>()

const open = ref(false)
</script>

<template>
  <span class="settings-label-with-help">
    <span class="settings-label-title">{{ label }}</span>
    <DouTooltip v-model:open="open" :content="content">
      <span
        class="settings-label-help"
        role="button"
        tabindex="0"
        :aria-label="t('settings.fieldHelp')"
        @click.stop.prevent="open = true"
        @keydown.enter.space.stop.prevent="open = true"
      >
        <InfoIcon aria-hidden="true" />
      </span>
    </DouTooltip>
  </span>
</template>

<style scoped>
.settings-label-with-help {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.settings-label-title {
  line-height: 1;
}

.settings-label-help {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 16px;
  height: 16px;
  border-radius: var(--dou-radius-control);
  color: var(--dou-color-text-secondary);
  cursor: help;
}

.settings-label-help:focus-visible {
  outline: 2px solid var(--dou-color-focus);
}
</style>
