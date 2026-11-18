<script setup lang="ts">
import type { ReplyDraftBlock } from '@ariadne/core'
import { DouText } from 'dou-ui'
import AskBlock from './AskBlock.vue'
import ShowBlock from './ShowBlock.vue'

defineProps<{ block: ReplyDraftBlock }>()
</script>

<template>
  <div inert class="draft-block">
    <div v-if="block.type === 'preview'" class="partial-block">
      <DouText v-if="block.title" weight="medium" size="sm">{{ block.title }}</DouText>
      <DouText v-if="block.description" variant="secondary" size="sm">
        {{ block.description }}
      </DouText>
      <DouText v-for="(label, index) in block.labels" :key="index" variant="secondary" size="sm">
        {{ label }}
      </DouText>
    </div>
    <AskBlock
      v-else-if="block.type === 'choice' || block.type === 'confirm' || block.type === 'form'"
      :block="block"
      :answer="null"
    />
    <ShowBlock v-else :block="block" />
  </div>
</template>

<style scoped>
.draft-block :deep(button) {
  opacity: 0.55;
}

.partial-block {
  display: flex;
  flex-direction: column;
  gap: 4px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
