<script setup lang="ts">
import type { ThreadView } from '@ariadne/core'
import { DouButton, DouEmpty, DouMenu, DouText, type MenuOption } from 'dou-ui'
import { ChevronDownIcon, ChevronRightIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { locale, t } from '../i18n'
import { groupOf, sortThreads, threadGroups, type ThreadGroup } from '../stage'
import { openThread, state } from '../store'
import ThreadRow from './ThreadRow.vue'
import { openGroups, threadSort } from './thread-list'

/** How many threads a group shows before "show more". */
const LIMIT = 6

const threads = computed(() => state.detail?.threads ?? [])
const byId = computed(() => new Map(threads.value.map((thread) => [thread.id, thread])))
const showingAll = ref(new Set<ThreadGroup>())

const groups = computed(() =>
  threadGroups
    .map((group) => ({
      group,
      threads: sortThreads(
        threads.value.filter((thread) => groupOf[thread.stage] === group),
        threadSort.value,
        locale.value,
      ),
    }))
    .filter((entry) => entry.threads.length),
)

const MORE = 'more:'
/** One menu per group: its threads, and "show more" while some are left out. */
function itemsOf(group: ThreadGroup, list: ThreadView[]): MenuOption[] {
  const shown = showingAll.value.has(group) ? list : list.slice(0, LIMIT)
  return [
    ...shown.map((thread) => ({ key: thread.id, label: thread.title })),
    ...(shown.length < list.length ? [{ key: `${MORE}${group}`, label: t('threads.more') }] : []),
  ]
}

const isOpen = (group: ThreadGroup) => openGroups.value.includes(group)
function toggle(group: ThreadGroup) {
  openGroups.value = isOpen(group)
    ? openGroups.value.filter((item) => item !== group)
    : [...openGroups.value, group]
}

function select(key: string | number) {
  const id = String(key)
  if (id.startsWith(MORE)) {
    showingAll.value = new Set([...showingAll.value, id.slice(MORE.length) as ThreadGroup])
    return
  }
  openThread(id)
}
</script>

<template>
  <div v-if="groups.length" class="thread-list">
    <section v-for="{ group, threads: list } in groups" :key="group" class="thread-group">
      <DouButton
        size="sm"
        variant="ghost"
        class="group-toggle"
        :aria-expanded="isOpen(group)"
        :aria-controls="`thread-group-${group}`"
        @click="toggle(group)"
      >
        {{ t(`threads.group.${group}`) }} · {{ list.length }}
        <template #trailing>
          <ChevronDownIcon v-if="isOpen(group)" />
          <ChevronRightIcon v-else />
        </template>
      </DouButton>
      <DouMenu
        v-if="isOpen(group)"
        :id="`thread-group-${group}`"
        class="thread-menu"
        :aria-label="t(`threads.group.${group}`)"
        :model-value="state.threadId ?? undefined"
        :items="itemsOf(group, list)"
        @select="select"
      >
        <template #label="{ item }">
          <DouText
            v-if="String(item.key).startsWith(MORE)"
            size="sm"
            variant="tertiary"
            class="more"
          >
            {{ item.label }}
          </DouText>
          <ThreadRow v-else-if="byId.get(String(item.key))" :thread="byId.get(String(item.key))!" />
        </template>
      </DouMenu>
    </section>
  </div>
  <DouEmpty v-else size="sm" />
</template>

<style scoped>
.thread-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.thread-group {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
}

/* The group's name is a small button the size of its text, quieter than the threads. */
.group-toggle {
  --dou-button-fg: var(--dou-color-text-tertiary);
}

/* Rows grow for the activity line. */
.thread-menu {
  align-self: stretch;
  --dou-menu-item-height: auto;
}

.more {
  padding: 6px 0 6px 24px;
}
</style>
