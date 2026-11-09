<script setup lang="ts">
import { useDashboardWidget } from '@dou-ui/dashboard'

/** A placeholder: what will be in this tile. */
const { props } = useDashboardWidget<{ text?: string }>()
</script>

<template>
  <div class="pending-widget">{{ props.text }}</div>
</template>

<style scoped>
.pending-widget {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--dou-color-text-tertiary);
  font-size: 13px;
  line-height: 1.5;
  text-align: center;
  white-space: pre-line;
}
</style>
