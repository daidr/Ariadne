<script setup lang="ts">
import { DouEmpty, DouTabPanel, DouTabs, DouTag, DouText } from 'dou-ui'
import { ref } from 'vue'
import { t } from '../i18n'
import { state } from '../store'
import TodoList from './TodoList.vue'

const tab = ref('plan')
const stageTone = {
  answer: 'warning',
  review: 'primary',
  blocked: 'danger',
  queued: 'neutral',
  working: 'primary',
  idle: 'neutral',
  done: 'success',
} as const
</script>

<template>
  <div class="side">
    <TodoList :todo="state.todo" />
    <DouTabs v-model="tab" class="side-tabs">
      <DouTabPanel value="plan" :label="t('panel.plan')">
        <DouEmpty size="sm" :description="t('coordinator.empty')" />
      </DouTabPanel>
      <DouTabPanel value="board" :label="t('panel.board')">
        <DouEmpty size="sm" />
      </DouTabPanel>
      <DouTabPanel value="threads" :label="t('panel.threads')">
        <ul v-if="state.detail?.threads.length" class="threads">
          <li v-for="thread in state.detail.threads" :key="thread.id" class="thread">
            <DouText truncate>{{ thread.title }}</DouText>
            <DouTag size="sm" :tone="stageTone[thread.stage]">
              {{ t(`thread.stage.${thread.stage}`) }}
            </DouTag>
          </li>
        </ul>
        <DouEmpty v-else size="sm" />
      </DouTabPanel>
      <DouTabPanel value="artifacts" :label="t('panel.artifacts')">
        <DouEmpty size="sm" />
      </DouTabPanel>
    </DouTabs>
  </div>
</template>

<style scoped>
.side {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.side-tabs {
  flex: 1;
  min-height: 0;
  padding: 8px 16px;
}

.threads {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.thread {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
</style>
