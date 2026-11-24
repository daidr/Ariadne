<script setup lang="ts">
import { RotateCwIcon } from '@dou-ui/icons'
import { DouAlert, DouButton } from 'dou-ui'
import { t } from '../i18n'

function reload() {
  window.location.reload()
}
</script>

<template>
  <div class="async-view-error">
    <DouAlert tone="danger">
      {{ t('view.loadError') }}
      <template #action>
        <DouButton size="sm" variant="outline" @click="reload">
          <template #leading><RotateCwIcon /></template>
          {{ t('view.retry') }}
        </DouButton>
      </template>
    </DouAlert>
  </div>
</template>

<style scoped>
.async-view-error {
  display: grid;
  place-items: center;
  width: 100%;
  height: 100%;
  min-height: 120px;
  padding: 16px;
  box-sizing: border-box;
}
</style>
