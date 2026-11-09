<script setup lang="ts">
import type { SearchHit, SearchResult, ThreadStage } from '@ariadne/core'
import {
  DouButton,
  DouCommandDialog,
  DouHighlight,
  DouSpinner,
  DouText,
  DouTooltip,
  useColorMode,
  type CommandItem,
} from 'dou-ui'
import {
  CoinsIcon,
  DownloadIcon,
  FolderCogIcon,
  KeyboardIcon,
  ListTreeIcon,
  MessagesIcon,
  MoonIcon,
  PlusIcon,
  SearchIcon,
  SettingsIcon,
  SunIcon,
} from '@dou-ui/icons'
import { computed, inject, ref, watch, type Component } from 'vue'
import { api } from '../api'
import { kindIcon } from '../artifacts/kinds'
import { locale, t } from '../i18n'
import {
  exportProject,
  openMain,
  openMessage,
  openProjectSettings,
  openThread,
  report,
  state,
} from '../store'
import { openSettingsKey } from '../open-settings'
import { sheetOpen } from '../shortcuts'
import StageIcon from './StageIcon.vue'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/**
 * Find what was said and made in the open project, or something to do: ⌘K or Ctrl+K
 * anywhere, unless rebound. What the header has no button for is reached from here.
 */

const query = ref('')
const result = ref<SearchResult | null>(null)
const loading = ref(false)

// Ask the server once typing pauses; an answer to an older query is dropped.
let timer: ReturnType<typeof setTimeout> | undefined
let asked = 0
watch(query, (value) => {
  clearTimeout(timer)
  const projectId = state.currentId
  const text = value.trim()
  if (!text || !projectId) {
    result.value = null
    loading.value = false
    return
  }
  loading.value = true
  const ask = ++asked
  timer = setTimeout(async () => {
    try {
      const found = await api.search(projectId, text)
      if (ask === asked) result.value = found
    } catch (error) {
      if (ask === asked) report(error)
    } finally {
      if (ask === asked) loading.value = false
    }
  }, 200)
})

const stageOf = (threadId: string): ThreadStage | null =>
  state.detail?.threads.find((thread) => thread.id === threadId)?.stage ?? null

const time = (at: number) =>
  new Date(at).toLocaleString(locale.value, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })

/** What each item of the list was found as, by its value. */
const hits = computed(() => {
  const found = result.value
  const all: SearchHit[] = found ? [...found.threads, ...found.artifacts, ...found.messages] : []
  return new Map(all.map((hit) => [`${hit.kind}:${hit.id}`, hit]))
})

const openSettings = inject(openSettingsKey, () => {})
const { isDark } = useColorMode()

interface Action {
  label: string
  icon: Component
  shortcut?: string
  run: () => void
}

/** What can be done from here; the project's own only while one is open. */
const actions = computed(() => {
  const project = state.detail?.project
  const own: [string, Action][] = project
    ? [
        [
          'projectSettings',
          {
            label: t('projectSettings.open'),
            icon: FolderCogIcon,
            run: () => openProjectSettings(),
          },
        ],
        [
          'usage',
          { label: t('usage.title'), icon: CoinsIcon, run: () => openProjectSettings('usage') },
        ],
        [
          'trace',
          {
            label: t('trace.coordinator'),
            icon: ListTreeIcon,
            run: () => openMain({ kind: 'trace', conversationId: project.coordinatorId }),
          },
        ],
        [
          'export',
          {
            label: t('project.export'),
            icon: DownloadIcon,
            run: () => void exportProject(project.id),
          },
        ],
      ]
    : []
  return new Map<string, Action>([
    [
      'newProject',
      { label: t('nav.newProject'), icon: PlusIcon, run: () => (state.creatingProject = true) },
    ],
    ...own,
    ['settings', { label: t('settings.open'), icon: SettingsIcon, run: () => openSettings() }],
    [
      'theme',
      {
        label: t(isDark.value ? 'appearance.toLight' : 'appearance.toDark'),
        icon: isDark.value ? SunIcon : MoonIcon,
        run: () => (isDark.value = !isDark.value),
      },
    ],
    [
      'shortcuts',
      {
        label: t('shortcuts.title'),
        icon: KeyboardIcon,
        shortcut: state.input.keys.sheet,
        run: () => (sheetOpen.value = true),
      },
    ],
  ])
})

/** The actions the words typed so far name; all of them before anything is typed. */
const actionItems = computed<CommandItem[]>(() => {
  const words = query.value.trim().toLowerCase()
  return [...actions.value]
    .filter(([, action]) => action.label.toLowerCase().includes(words))
    .map(([key, action]) => ({
      value: `action:${key}`,
      label: action.label,
      group: t('search.actions'),
      ...(action.shortcut ? { shortcut: action.shortcut } : {}),
    }))
})
const actionOf = (value: unknown) => actions.value.get(String(value).slice('action:'.length))

const items = computed<CommandItem[]>(() => [...actionItems.value, ...foundItems.value])

const foundItems = computed<CommandItem[]>(() =>
  [...hits.value].map(([value, hit]) =>
    hit.kind === 'message'
      ? {
          value,
          label: hit.snippet,
          description: `${hit.where ?? t('coordinator.title')} · ${time(hit.at)}`,
          group: t('search.messages'),
        }
      : {
          value,
          label: hit.title,
          description: hit.snippet,
          group: t(hit.kind === 'thread' ? 'search.threads' : 'search.artifacts'),
        },
  ),
)

// The server already matched every item; keep its order, newest first in each group.
const keepAll = () => ({ score: 0, indices: [] })

function select(item: CommandItem) {
  if (String(item.value).startsWith('action:')) {
    actionOf(item.value)?.run()
    return
  }
  const hit = hits.value.get(String(item.value))
  if (!hit) return
  if (hit.kind === 'thread') void openThread(hit.id)
  else if (hit.kind === 'artifact') openView({ kind: 'artifact', id: hit.id })
  else void openMessage(hit.conversationId, hit.id)
}
</script>

<template>
  <DouTooltip :content="t('search.open')" placement="bottom">
    <DouButton
      variant="ghost"
      size="sm"
      :aria-label="t('search.open')"
      @click="state.searchOpen = true"
    >
      <SearchIcon />
    </DouButton>
  </DouTooltip>
  <DouCommandDialog
    v-model:open="state.searchOpen"
    v-model:query="query"
    :items="items"
    :filter="keepAll"
    :placeholder="t('search.placeholder')"
    :max-height="440"
    :width="600"
    :hotkey="state.input.keys.search"
    @select="select"
  >
    <template #item="{ item }">
      <span v-if="actionOf(item.value)" class="hit-line">
        <span class="hit-icon"><component :is="actionOf(item.value)!.icon" /></span>
        <DouText truncate class="hit-label">{{ item.label }}</DouText>
      </span>
      <div
        v-for="hit in hits.has(String(item.value)) ? [hits.get(String(item.value))!] : []"
        :key="item.value"
        class="hit"
      >
        <!-- The icon sits on the first line, centred on it; the second line starts under the text. -->
        <span class="hit-line">
          <span class="hit-icon">
            <template v-if="hit.kind === 'thread'">
              <StageIcon v-if="stageOf(hit.id)" :stage="stageOf(hit.id)!" />
            </template>
            <component :is="kindIcon[hit.artifactKind]" v-else-if="hit.kind === 'artifact'" />
            <MessagesIcon v-else />
          </span>
          <DouText truncate class="hit-label">
            <DouHighlight :text="item.label" :keywords="result?.terms ?? []" />
          </DouText>
        </span>
        <DouText v-if="item.description" variant="tertiary" size="sm" truncate class="hit-detail">
          <template v-if="hit.kind === 'message'">{{ item.description }}</template>
          <DouHighlight v-else :text="item.description" :keywords="result?.terms ?? []" />
        </DouText>
      </div>
    </template>
    <template #empty>
      <DouSpinner v-if="loading" size="sm" />
      <template v-else-if="query.trim()">{{ t('search.empty') }}</template>
    </template>
  </DouCommandDialog>
</template>

<style scoped>
.hit {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  width: 100%;
}

.hit-line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

/* Trimmed text is 2px shorter; keep the line as tall as before, so the two lines don't close up. */
@supports (text-box: trim-both cap alphabetic) {
  .hit-line {
    padding-block: 1px;
  }
}

.hit-icon {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 16px;
  font-size: 16px;
  color: var(--dou-color-text-tertiary);
}

.hit-label {
  flex: 1;
  min-width: 0;
  /* Centre the icon on the letters, not on the font's ascent and descent; see ThreadRow. */
  text-box: trim-both cap alphabetic;
}

.hit-detail {
  padding-left: 24px;
}
</style>
