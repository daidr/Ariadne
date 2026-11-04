<script setup lang="ts">
import { DouText } from 'dou-ui'

/** A titled part of the plan's side panel: a small heading, with a count when it lists things. */
defineProps<{ title: string; count?: number }>()
</script>

<template>
  <section class="plan-section" :aria-label="title">
    <div class="plan-section-head">
      <DouText as="h3" size="sm" weight="medium" variant="secondary" class="plan-section-title">
        {{ title }}
      </DouText>
      <DouText v-if="count" size="sm" variant="tertiary">{{ count }}</DouText>
    </div>
    <slot />
  </section>
</template>

<style scoped>
.plan-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.plan-section-head {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.plan-section-title {
  margin: 0;
}
</style>
