<script setup lang="ts">
import { DouButton, DouEmpty, DouText, DouTextarea } from 'dou-ui'
import { PencilIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { ref } from 'vue'
import { authorName } from '../artifacts/author'
import { t } from '../i18n'
import { saveMemory, state } from '../store'

// The project's standing preferences: what every conversation of the project follows.
const editing = ref<string | 'new' | null>(null)
const draft = ref('')
const saving = ref(false)
const problem = ref<string | null>(null)

function start(id: string | 'new', text = '') {
  editing.value = id
  draft.value = text
  problem.value = null
}

async function save(id: string | null, text: string | null) {
  saving.value = true
  problem.value = await saveMemory(id, text)
  saving.value = false
  if (!problem.value) editing.value = null
}
</script>

<template>
  <section class="memory" :aria-label="t('memory.title')">
    <ul v-if="state.memories.length" class="memory-list">
      <li v-for="memory in state.memories" :key="memory.id" class="memory-item">
        <template v-if="editing === memory.id">
          <DouTextarea v-model="draft" size="sm" :autosize="{ minRows: 2, maxRows: 6 }" />
          <div class="memory-actions">
            <DouButton size="sm" variant="ghost" @click="editing = null">
              {{ t('settings.cancel') }}
            </DouButton>
            <DouButton
              size="sm"
              tone="primary"
              :loading="saving"
              :disabled="!draft.trim()"
              @click="save(memory.id, draft)"
            >
              {{ t('memory.save') }}
            </DouButton>
          </div>
        </template>
        <template v-else>
          <div class="memory-row">
            <span class="memory-text">{{ memory.text }}</span>
            <DouButton
              size="sm"
              variant="ghost"
              :aria-label="t('memory.edit')"
              :title="t('memory.edit')"
              @click="start(memory.id, memory.text)"
            >
              <PencilIcon />
            </DouButton>
            <DouButton
              size="sm"
              variant="ghost"
              tone="danger"
              :aria-label="t('memory.remove')"
              :title="t('memory.remove')"
              @click="save(memory.id, null)"
            >
              <TrashIcon />
            </DouButton>
          </div>
          <DouText variant="tertiary" size="sm">{{ authorName(memory.author) }}</DouText>
        </template>
      </li>
    </ul>
    <DouEmpty v-else-if="editing !== 'new'" size="sm" :description="t('memory.empty')" />
    <DouText v-if="problem" tone="danger" size="sm" role="alert">{{ problem }}</DouText>
    <div v-if="editing === 'new'" class="memory-item">
      <DouTextarea
        v-model="draft"
        size="sm"
        :autosize="{ minRows: 2, maxRows: 6 }"
        :aria-label="t('memory.add')"
      />
      <div class="memory-actions">
        <DouButton size="sm" variant="ghost" @click="editing = null">
          {{ t('settings.cancel') }}
        </DouButton>
        <DouButton
          size="sm"
          tone="primary"
          :loading="saving"
          :disabled="!draft.trim()"
          @click="save(null, draft)"
        >
          {{ t('memory.save') }}
        </DouButton>
      </div>
    </div>
    <div v-else class="memory-actions">
      <DouButton size="sm" variant="outline" @click="start('new')">
        <template #leading><PlusIcon /></template>
        {{ t('memory.add') }}
      </DouButton>
    </div>
  </section>
</template>

<style scoped>
.memory {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.memory-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.memory-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.memory-row {
  display: flex;
  align-items: flex-start;
  gap: 4px;
}

.memory-text {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}

.memory-actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
</style>
