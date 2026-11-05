<script setup lang="ts">
import { DouButton, DouStatusDot } from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { setAutopilotPaused, state } from '../store'

/**
 * The open project's autopilot, while it is switched on: a rippling dot while it runs, a
 * still one while paused. One click pauses it or lets it go on.
 */
const project = computed(() => state.detail?.project ?? null)
const on = computed(() => !!project.value?.settings.autopilot.enabled)
const paused = computed(() => !!project.value?.autopilotPaused)
const busy = ref(false)

async function toggle() {
  busy.value = true
  await setAutopilotPaused(!paused.value)
  busy.value = false
}
</script>

<template>
  <DouButton
    v-if="on"
    variant="ghost"
    size="sm"
    :loading="busy"
    :aria-pressed="!paused"
    :title="paused ? t('autopilot.resume') : t('autopilot.pause')"
    @click="toggle"
  >
    <template #leading>
      <DouStatusDot :tone="paused ? 'neutral' : 'success'" :ripple="!paused" />
    </template>
    {{ paused ? t('autopilot.paused') : t('autopilot.on') }}
  </DouButton>
</template>
