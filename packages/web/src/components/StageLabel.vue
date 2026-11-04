<script setup lang="ts">
import type { ThreadStage } from '@ariadne/core'
import { DouShimmer, DouText } from 'dou-ui'
import { t } from '../i18n'
import { stageTone } from '../stage'

/** A thread's stage as a word: working shimmers, stages that want the user take their tone. */
defineProps<{ stage: ThreadStage }>()
</script>

<template>
  <DouText
    size="sm"
    :tone="stage === 'working' ? 'neutral' : stageTone[stage]"
    :variant="stageTone[stage] === 'neutral' ? 'tertiary' : 'default'"
    class="thread-stage"
  >
    <DouShimmer v-if="stage === 'working'">{{ t('thread.stage.working') }}</DouShimmer>
    <template v-else>{{ t(`thread.stage.${stage}`) }}</template>
  </DouText>
</template>

<style scoped>
.thread-stage {
  flex: none;
  white-space: nowrap;
}
</style>
