<script setup lang="ts">
import { DouTooltip } from 'dou-ui'

// A tab on the side panel shows only its icon; the name is in the tooltip and read out by
// screen readers.
defineProps<{ label: string }>()
</script>

<template>
  <DouTooltip :content="label" placement="left">
    <span class="side-tab" role="img" :aria-label="label"><slot /></span>
  </DouTooltip>
</template>

<style scoped>
/* The icon takes the whole tab, so hovering anywhere on it shows the name. */
.side-tab {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: var(--dou-tabs-height);
  padding: 0 var(--dou-space-sm);
}
</style>
