<script setup lang="ts">
import type { TableBlock } from '@ariadne/core'
import { DouTable } from 'dou-ui'

defineProps<{ block: TableBlock }>()
</script>

<template>
  <DouTable :data="block.rows" :columns="block.columns" size="sm" :max-height="360" />
</template>
