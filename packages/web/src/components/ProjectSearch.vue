<script setup lang="ts">
import type { ProjectView, SearchHit, SearchResult, ThreadStage } from '@ariadne/core'
import {
  DouBadge,
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
  ActivityIcon,
  CoinsIcon,
  DownloadIcon,
  FolderCogIcon,
  FolderIcon,
  KeyboardIcon,
  LayoutGridIcon,
  ListTreeIcon,
  MessagesIcon,
  MoonIcon,
  PlusIcon,
  RotateCwIcon,
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
  openGallery,
  openMain,
  openProject,
  openProjectArtifact,
  openProjectMessage,
  openProjectSettings,
  openProjectThread,
  report,
  startNewProject,
  state,
  threadsById,
} from '../store'
import { openSettingsKey } from '../open-settings'
import { sheetOpen } from '../shortcuts'
import StageIcon from './StageIcon.vue'

/**
 * Find what was said and made across every project, switch projects, or run an action:
 * ⌘K or Ctrl+K anywhere, unless rebound. Hits in the open project come first.
 */

const query = ref('')
const result = ref<SearchResult | null>(null)
const loading = ref(false)
const searchError = ref(false)
let lastPreferProjectId: string | null = null

// Ask the server once typing pauses; an answer to an older query is dropped.
let timer: ReturnType<typeof setTimeout> | undefined
let asked = 0
function runSearch(value: string, immediate = false) {
  clearTimeout(timer)
  const text = value.trim()
  if (!text) {
    asked += 1
    result.value = null
    searchError.value = false
    loading.value = false
    lastPreferProjectId = null
    return
  }
  const preferProjectId = state.currentId ?? undefined
  lastPreferProjectId = state.currentId
  searchError.value = false
  loading.value = true
  const ask = ++asked
  const execute = async () => {
    try {
      const found = await api.search('', text, preferProjectId)
      if (ask === asked) {
        result.value = found
        searchError.value = false
      }
    } catch (error) {
      if (ask === asked) {
        result.value = null
        searchError.value = true
        report(error)
      }
    } finally {
      if (ask === asked) loading.value = false
    }
  }
  if (immediate) void execute()
  else timer = setTimeout(() => void execute(), 200)
}

watch(query, (value) => runSearch(value))

watch([() => state.currentId, () => state.searchOpen], ([currentId, open]) => {
  if (open && query.value.trim() && currentId !== lastPreferProjectId) {
    runSearch(query.value, true)
  }
})

function retrySearch() {
  runSearch(query.value, true)
}

const stageOf = (hit: Extract<SearchHit, { kind: 'thread' }>): ThreadStage | null =>
  hit.stage ?? threadsById.value.get(hit.id)?.stage ?? null

const isCrossProject = (hit: SearchHit) =>
  Boolean(hit.projectName && hit.projectId !== state.currentId)

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
          'pulse',
          {
            label: t('pulse.title'),
            icon: ActivityIcon,
            run: () => openMain({ kind: 'pulse' }),
          },
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
    ['gallery', { label: t('gallery.open'), icon: LayoutGridIcon, run: openGallery }],
    ['newProject', { label: t('nav.newProject'), icon: PlusIcon, run: startNewProject }],
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

const queryTerms = computed(() => query.value.trim().split(/\s+/).filter(Boolean))

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

const projectsById = computed(() => {
  const map = new Map<string, ProjectView>()
  for (const project of state.projects) map.set(project.id, project)
  return map
})

/** Projects whose names match what was typed, for quick switching from ⌘K. */
const projectItems = computed<CommandItem[]>(() => {
  const words = query.value.trim().toLowerCase()
  if (!words) return []
  return state.projects
    .filter((project) => project.name.toLowerCase().includes(words))
    .map((project) => ({
      value: `project:${project.id}`,
      label: project.name,
      group: t('search.projects'),
    }))
})
const projectOf = (value: unknown) => {
  const raw = String(value)
  if (!raw.startsWith('project:')) return undefined
  return projectsById.value.get(raw.slice('project:'.length))
}

const foundItems = computed<CommandItem[]>(() =>
  [...hits.value].map(([value, hit]) => {
    if (hit.kind === 'message') {
      const where = hit.where ?? t('coordinator.title')
      const detail = isCrossProject(hit)
        ? `${hit.projectName} · ${where} · ${time(hit.at)}`
        : `${where} · ${time(hit.at)}`
      return {
        value,
        label: hit.snippet,
        description: detail,
        group: t('search.messages'),
      }
    }
    return {
      value,
      label: hit.title,
      description: hit.snippet,
      group: t(hit.kind === 'thread' ? 'search.threads' : 'search.artifacts'),
    }
  }),
)

const items = computed<CommandItem[]>(() => [
  ...actionItems.value,
  ...projectItems.value,
  ...foundItems.value,
])

// The server already matched every item; keep its order, current project first then newest first.
const keepAll = () => ({ score: 0, indices: [] })

function select(item: CommandItem) {
  const value = String(item.value)
  if (value.startsWith('action:')) {
    actionOf(value)?.run()
    return
  }
  if (value.startsWith('project:')) {
    const projectId = value.slice('project:'.length)
    if (projectId) void openProject(projectId)
    return
  }
  const hit = hits.value.get(value)
  if (!hit) return
  if (hit.kind === 'thread') void openProjectThread(hit.projectId, hit.id)
  else if (hit.kind === 'artifact') void openProjectArtifact(hit.projectId, hit.id)
  else void openProjectMessage(hit.projectId, hit.conversationId, hit.id)
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
    <template #prefix>
      <DouSpinner v-if="loading" size="sm" />
      <SearchIcon v-else />
    </template>
    <template #item="{ item }">
      <span v-if="actionOf(item.value)" class="hit-line">
        <span class="hit-icon"><component :is="actionOf(item.value)!.icon" /></span>
        <DouText truncate class="hit-label">{{ item.label }}</DouText>
      </span>
      <span v-else-if="projectOf(item.value)" class="hit-line">
        <span class="hit-icon"><FolderIcon /></span>
        <DouText truncate class="hit-label">
          <DouHighlight :text="item.label" :keywords="queryTerms" />
        </DouText>
        <DouText
          v-if="projectOf(item.value)!.id === state.currentId"
          size="sm"
          variant="tertiary"
          class="hit-meta"
        >
          {{ t('attention.currentProject') }}
        </DouText>
        <DouBadge
          v-if="projectOf(item.value)!.needsMe"
          :value="projectOf(item.value)!.needsMe"
          tone="warning"
        />
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
              <StageIcon v-if="stageOf(hit)" :stage="stageOf(hit)!" />
            </template>
            <component :is="kindIcon[hit.artifactKind]" v-else-if="hit.kind === 'artifact'" />
            <MessagesIcon v-else />
          </span>
          <DouText truncate class="hit-label">
            <DouHighlight :text="item.label" :keywords="result?.terms ?? []" />
          </DouText>
        </span>
        <DouText
          v-if="item.description || isCrossProject(hit)"
          variant="tertiary"
          size="sm"
          truncate
          class="hit-detail"
        >
          <template v-if="hit.kind === 'message'">{{ item.description }}</template>
          <template v-else>
            <template v-if="isCrossProject(hit)">
              <span class="hit-project">{{ hit.projectName }}</span>
              <template v-if="item.description"> · </template>
            </template>
            <DouHighlight
              v-if="item.description"
              :text="item.description"
              :keywords="result?.terms ?? []"
            />
          </template>
        </DouText>
      </div>
    </template>
    <template #empty>
      <DouSpinner v-if="loading" size="sm" />
      <div v-else-if="searchError" class="search-status">
        <DouText size="sm" variant="secondary">{{ t('search.error') }}</DouText>
        <DouButton size="sm" variant="outline" @click="retrySearch">
          <template #leading><RotateCwIcon /></template>
          {{ t('conversation.retry') }}
        </DouButton>
      </div>
      <template v-else-if="query.trim()">{{ t('search.empty') }}</template>
    </template>
    <template v-if="(loading || searchError) && items.length" #footer>
      <div class="search-footer" role="status" aria-live="polite">
        <template v-if="loading">
          <DouSpinner size="sm" />
          <DouText size="sm" variant="tertiary">{{ t('search.loading') }}</DouText>
        </template>
        <template v-else-if="searchError">
          <DouText size="sm" variant="secondary">{{ t('search.error') }}</DouText>
          <DouButton size="sm" variant="ghost" @click="retrySearch">
            <template #leading><RotateCwIcon /></template>
            {{ t('conversation.retry') }}
          </DouButton>
        </template>
      </div>
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

.hit-meta {
  flex: none;
}

.hit-project {
  font-weight: var(--dou-font-weight-medium);
}

.hit-detail {
  padding-left: 24px;
}

.search-status {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.search-footer {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}
</style>
