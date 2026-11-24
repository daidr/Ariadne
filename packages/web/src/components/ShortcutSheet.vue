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
        { label: t('shortcuts.search'), keys: [state.input.keys.search] },
        { label: t('shortcuts.focus'), keys: [state.input.keys.focus] },
        { label: t('shortcuts.previousThread'), keys: [state.input.keys.previousThread] },
        { label: t('shortcuts.nextThread'), keys: [state.input.keys.nextThread] },
        { label: t('shortcuts.stop'), keys: [state.input.keys.stop] },
        { label: t('shortcuts.sheet'), keys: [state.input.keys.sheet] },
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
    {
      title: t('shortcuts.navigation'),
      rows: [
        { label: t('shortcuts.reorderProject'), keys: ['alt+up', 'alt+down'] },
        { label: t('shortcuts.openBeside'), text: t('shortcuts.altClick') },
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
            <template v-if="row.keys">
              <DouKbd v-for="keys in row.keys" :key="keys" size="sm" :keys="keys" />
            </template>
            <DouText v-else-if="row.text" size="sm" variant="secondary" class="sheet-text-key">
              {{ row.text }}
            </DouText>
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
  align-items: center;
  gap: 4px;
}

.sheet-text-key {
  padding: 1px 6px;
  border: 1px solid var(--dou-color-border);
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-muted);
  white-space: nowrap;
}
</style>
