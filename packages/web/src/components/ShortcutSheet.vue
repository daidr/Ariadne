<script setup lang="ts">
import { DouDialog, DouKbd, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { sheetOpen } from '../shortcuts'
import { state } from '../store'

/** Every key the app answers to, `?` anywhere. */

const sections = computed(() => {
  const enterSends = state.input.submitOn === 'enter'
  return [
    {
      title: t('shortcuts.global'),
      rows: [
        { label: t('shortcuts.search'), keys: ['mod+k'] },
        { label: t('shortcuts.focus'), keys: ['/'] },
        { label: t('shortcuts.previousThread'), keys: ['mod+up'] },
        { label: t('shortcuts.nextThread'), keys: ['mod+down'] },
        { label: t('shortcuts.stop'), keys: ['escape'] },
        { label: t('shortcuts.sheet'), keys: ['?'] },
      ],
    },
    {
      title: t('shortcuts.input'),
      rows: [
        { label: t('shortcuts.send'), keys: [enterSends ? 'enter' : 'mod+enter'] },
        { label: t('shortcuts.newline'), keys: [enterSends ? 'shift+enter' : 'enter'] },
        { label: t('shortcuts.history'), keys: ['up', 'down'] },
        { label: t('shortcuts.snippet'), keys: ['/'] },
      ],
    },
  ]
})
</script>

<template>
  <DouDialog v-model:open="sheetOpen" :title="t('shortcuts.title')" :width="420">
    <div class="sheet">
      <section v-for="section in sections" :key="section.title" class="sheet-section">
        <DouText variant="tertiary" size="sm">{{ section.title }}</DouText>
        <div v-for="row in section.rows" :key="row.label" class="sheet-row">
          <span>{{ row.label }}</span>
          <span class="sheet-keys">
            <DouKbd v-for="keys in row.keys" :key="keys" size="sm" :keys="keys" />
          </span>
        </div>
      </section>
    </div>
  </DouDialog>
</template>

<style scoped>
.sheet {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.sheet-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.sheet-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.sheet-keys {
  display: inline-flex;
  gap: 4px;
}
</style>
