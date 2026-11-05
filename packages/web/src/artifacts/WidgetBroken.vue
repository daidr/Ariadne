<script setup lang="ts">
import { AlertCircleIcon } from '@dou-ui/icons'
import { DouText } from 'dou-ui'
import { t } from '../i18n'
</script>

<template>
  <div class="widget-broken" role="alert">
    <AlertCircleIcon class="widget-broken-icon" />
    <DouText variant="secondary" size="sm">{{ t('artifact.dashboard.widgetBroken') }}</DouText>
  </div>
</template>

<style scoped>
.widget-broken {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 100%;
}

.widget-broken-icon {
  flex: none;
  color: var(--dou-color-danger-text);
}
</style>
