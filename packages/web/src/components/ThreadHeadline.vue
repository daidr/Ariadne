<script setup lang="ts">
import { DouText } from 'dou-ui'
import type { ThreadStage } from '@ariadne/core'
import { showsHeadline } from '../stage'

/** What a thread waiting on the user asks of them, in the line its report gave for it. */
const props = withDefaults(
  defineProps<{ stage: ThreadStage; headline: string | null; lines?: number }>(),
  { lines: 2 },
)
</script>

<template>
  <DouText
    v-if="props.headline && showsHeadline(props.stage)"
    variant="secondary"
    size="sm"
    :truncate="props.lines === 1 ? true : props.lines"
    :title="props.headline"
  >
    {{ props.headline }}
  </DouText>
</template>
