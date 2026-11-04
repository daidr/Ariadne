<script setup lang="ts">
import { DouButton, DouSteps, DouText, type StepItem } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { state } from '../store'

// What a first run needs before the coordinator can do anything useful, in order.
const emit = defineEmits<{ openSettings: [] }>()

const done = computed(() => [
  state.modelReady === true,
  state.projects.length > 0,
  !!state.detail?.project.source,
])
// The working directory is optional: past the first two, the user can just start talking.
const current = computed(() => {
  const next = done.value.findIndex((step) => !step)
  return next === -1 ? 3 : next
})
const items = computed<StepItem[]>(() =>
  (['model', 'project', 'source', 'talk'] as const).map((step, index) => ({
    title: t(`start.${step}`),
    description: t(`start.${step}Hint`),
    ...(done.value[index] ? { status: 'finish' as const } : {}),
  })),
)
</script>

<template>
  <section class="start" :aria-label="t('start.title')">
    <DouText weight="medium">{{ t('start.title') }}</DouText>
    <DouSteps :items="items" :current="current" direction="vertical" size="sm" />
    <div class="start-actions">
      <DouButton v-if="current === 0" size="sm" tone="primary" @click="emit('openSettings')">
        {{ t('conversation.openSettings') }}
      </DouButton>
      <DouButton
        v-else-if="current === 1"
        size="sm"
        tone="primary"
        @click="state.creatingProject = true"
      >
        {{ t('nav.newProject') }}
      </DouButton>
      <DouButton v-else-if="current === 2" size="sm" @click="state.sourceOpen = true">
        {{ t('start.chooseSource') }}
      </DouButton>
    </div>
  </section>
</template>

<style scoped>
.start {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: min(420px, 100%);
}

.start-actions {
  display: flex;
  gap: 8px;
}
</style>
