<script setup lang="ts">
import { DouEmpty, DouMenu, DouText, type MenuOption } from 'dou-ui'
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

const GROUP = 'group:'
const MORE = 'more:'
const items = computed<MenuOption[]>(() =>
  groups.value.map(({ group, threads: list }) => {
    const shown = showingAll.value.has(group) ? list : list.slice(0, LIMIT)
    return {
      key: `${GROUP}${group}`,
      label: `${t(`threads.group.${group}`)} · ${list.length}`,
      children: [
        ...shown.map((thread) => ({ key: thread.id, label: thread.title })),
        ...(shown.length < list.length
          ? [{ key: `${MORE}${group}`, label: t('threads.more') }]
          : []),
      ],
    }
  }),
)

const expanded = computed({
  get: () => openGroups.value.map((group) => `${GROUP}${group}`),
  set: (keys) => {
    openGroups.value = keys.map((key) => String(key).slice(GROUP.length) as ThreadGroup)
  },
})

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
  <DouMenu
    v-if="items.length"
    v-model:expanded="expanded"
    class="thread-list"
    :model-value="state.threadId ?? undefined"
    :items="items"
    @select="select"
  >
    <template #label="{ item }">
      <DouText v-if="String(item.key).startsWith(GROUP)" size="sm" variant="tertiary" class="group">
        {{ item.label }}
      </DouText>
      <DouText
        v-else-if="String(item.key).startsWith(MORE)"
        size="sm"
        variant="tertiary"
        class="more"
      >
        {{ item.label }}
      </DouText>
      <ThreadRow v-else-if="byId.get(String(item.key))" :thread="byId.get(String(item.key))!" />
    </template>
  </DouMenu>
  <DouEmpty v-else size="sm" />
</template>

<style scoped>
/* Group headers sit flush with the threads under them; rows grow for the activity line. */
.thread-list {
  --dou-menu-indent: 0px;
  --dou-menu-item-height: auto;
}

.group {
  padding: 6px 0;
}

.more {
  padding: 6px 0 6px 24px;
}
</style>
