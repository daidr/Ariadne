<script setup lang="ts">
import type { Reaction } from '@ariadne/core'
import { DouTag } from 'dou-ui'

/** The emojis an agent put on one of the user's messages, under it: shown, not answered. */
defineProps<{ reactions: Reaction[] }>()
</script>

<template>
  <span class="reactions">
    <DouTag
      v-for="reaction in reactions"
      :key="`${reaction.by}:${reaction.emoji}`"
      variant="soft"
      size="sm"
      round
      class="reaction"
    >
      {{ reaction.emoji }}
    </DouTag>
  </span>
</template>

<style scoped>
.reactions {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
</style>
