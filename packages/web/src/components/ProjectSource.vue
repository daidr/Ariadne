<script setup lang="ts">
import { DouButton, DouInput, DouText } from 'dou-ui'
import { FolderOpenIcon, PencilIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { setSource, state } from '../store'

const editing = ref(false)
const draft = ref('')
const saving = ref(false)
const problem = ref<string | null>(null)

const path = computed(() => {
  const value = state.detail?.project.source?.settings.path
  return typeof value === 'string' ? value : null
})

function edit() {
  draft.value = path.value ?? ''
  problem.value = null
  editing.value = true
}

async function save(next: string | null) {
  saving.value = true
  problem.value = await setSource(next)
  saving.value = false
  if (!problem.value) editing.value = false
}
</script>

<template>
  <section v-if="state.detail" class="source" :aria-label="t('project.source.title')">
    <DouText variant="secondary" size="sm">{{ t('project.source.title') }}</DouText>
    <form v-if="editing" class="source-form" @submit.prevent="save(draft.trim() || null)">
      <DouInput
        v-model="draft"
        size="sm"
        :placeholder="t('project.source.placeholder')"
        :invalid="!!problem"
        autofocus
      />
      <DouText v-if="problem" tone="danger" size="sm" role="alert">{{ problem }}</DouText>
      <DouText v-else variant="tertiary" size="sm">{{ t('project.source.help') }}</DouText>
      <div class="source-actions">
        <DouButton v-if="path" size="sm" variant="ghost" tone="danger" @click="save(null)">
          {{ t('project.source.clear') }}
        </DouButton>
        <DouButton size="sm" variant="ghost" @click="editing = false">
          {{ t('settings.cancel') }}
        </DouButton>
        <DouButton size="sm" tone="primary" native-type="submit" :loading="saving">
          {{ t('project.source.save') }}
        </DouButton>
      </div>
    </form>
    <div v-else class="source-row">
      <FolderOpenIcon class="source-icon" />
      <DouText v-if="path" size="sm" truncate :title="path" class="source-path">{{ path }}</DouText>
      <DouText v-else variant="tertiary" size="sm" class="source-path">
        {{ t('project.source.none') }}
      </DouText>
      <DouButton
        size="sm"
        variant="ghost"
        :aria-label="t('project.source.edit')"
        :title="t('project.source.edit')"
        @click="edit"
      >
        <PencilIcon />
      </DouButton>
    </div>
  </section>
</template>

<style scoped>
.source {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.source-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.source-icon {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.source-path {
  flex: 1;
  min-width: 0;
}

.source-form {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.source-actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
</style>
