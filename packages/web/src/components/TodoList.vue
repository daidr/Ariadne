<script setup lang="ts">
import { DouTag, DouText } from 'dou-ui'
import type { TodoStatus, TodoView } from '@ariadne/core'
import { computed } from 'vue'
import { t } from '../i18n'

const props = defineProps<{ todo: TodoView | null }>()

const order: TodoStatus[] = ['doing', 'waiting', 'next', 'done']
const tone = { doing: 'primary', waiting: 'warning', next: 'neutral', done: 'success' } as const

// The list is the answer to "what is it doing, what is next, what is it waiting on".
const groups = computed(() =>
  order
    .map((status) => ({
      status,
      items: (props.todo?.items ?? []).filter((item) => item.status === status),
    }))
    .filter((group) => group.items.length),
)
</script>

<template>
  <section class="todo" :aria-label="t('todo.title')">
    <DouText variant="secondary" size="sm">{{ t('todo.title') }}</DouText>
    <ul v-if="groups.length" class="todo-list">
      <template v-for="group in groups" :key="group.status">
        <li
          v-for="(item, index) in group.items"
          :key="`${group.status}-${index}`"
          class="todo-item"
        >
          <DouTag size="sm" :tone="tone[group.status]">{{ t(`todo.${group.status}`) }}</DouTag>
          <span :class="{ 'todo-done': group.status === 'done' }">{{ item.label }}</span>
          <DouText v-if="item.detail" variant="tertiary" size="sm">{{ item.detail }}</DouText>
        </li>
      </template>
    </ul>
    <DouText v-else variant="tertiary" size="sm">—</DouText>
  </section>
</template>

<style scoped>
.todo {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.todo-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.todo-item {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.todo-done {
  color: var(--dou-color-text-tertiary);
}
</style>
