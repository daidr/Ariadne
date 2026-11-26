<script setup lang="ts">
import type { ProjectView } from '@ariadne/core'
import {
  DouButton,
  DouContextMenu,
  DouEmpty,
  DouSortable,
  DouText,
  DouVirtualList,
  useDialog,
  useLocale,
  useSortable,
  type VirtualListExpose,
} from 'dou-ui'
import {
  ActivityIcon,
  DownloadIcon,
  ListTreeIcon,
  PencilIcon,
  PlusIcon,
  SettingsIcon,
  TrashIcon,
} from '@dou-ui/icons'
import { computed, nextTick, ref, useTemplateRef, watch } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import {
  deleteProject,
  exportProject,
  openMain,
  openProject,
  renameProject,
  reorderProjects,
  report,
  startNewProject,
  state,
  openProjectSettings,
} from '../store'
import ProjectRow from './ProjectRow.vue'

/** Switch from plain sortable list to virtualized sortable list above this threshold. */
const PROJECT_VIRTUALIZE_THRESHOLD = 40
const ROW_GAP = 8

const props = withDefaults(
  defineProps<{ compact?: boolean; active?: boolean; surface?: 'sidebar' | 'popover' }>(),
  {
    active: true,
    surface: 'sidebar',
  },
)
const emit = defineEmits<{ select: [] }>()

const dialog = useDialog()
const { t: douT } = useLocale()
const canReorder = computed(() => state.projects.length > 1)
const menuOpen = ref(false)
const inspecting = ref<string>()
const inPopover = computed(() => props.surface === 'popover')
const virtualized = computed(() => state.projects.length > PROJECT_VIRTUALIZE_THRESHOLD)
const rowStride = computed(() => (props.compact ? 44 : inPopover.value ? 36 : 32) + ROW_GAP)

const virtualList = useTemplateRef<VirtualListExpose>('virtualList')
const sortableItems = computed<ProjectView[]>({
  get: () => state.projects,
  set: (projects) => reorderProjects(projects.map((project) => project.id)),
})
const virtualSortable = useSortable({
  items: sortableItems,
  key: (project) => project.id,
  handle: () => true,
  disabled: () => !virtualized.value || !canReorder.value,
  direction: () => 'vertical',
  label: (project) => project.name,
  messages: () => ({
    item: douT('sortable.item'),
    lifted: (label, position, total) => douT('sortable.lifted', { label, position, total }),
    moved: (label, position, total) => douT('sortable.moved', { label, position, total }),
    dropped: (label, position, total) => douT('sortable.dropped', { label, position, total }),
    canceled: (label) => douT('sortable.canceled', { label }),
  }),
})
const activityDisabled = computed(
  () =>
    !!props.compact ||
    inPopover.value ||
    !props.active ||
    menuOpen.value ||
    virtualSortable.dragging.value !== null,
)

watch(
  [virtualized, virtualList],
  ([isVirtual, list]) => {
    virtualSortable.container.value = isVirtual ? (list?.el() ?? null) : null
  },
  { flush: 'post', immediate: true },
)

function inspectProject(id: string, open: boolean) {
  if (open && !activityDisabled.value) inspecting.value = id
  else if (inspecting.value === id) inspecting.value = undefined
}

function selectProject(id: string) {
  inspecting.value = undefined
  if (state.detail?.project.id === id) openMain({ kind: 'conversation' })
  else void openProject(id)
  emit('select')
}

function createProject() {
  startNewProject()
  emit('select')
}

watch(activityDisabled, (disabled) => {
  if (disabled) inspecting.value = undefined
})

watch(
  () => props.active,
  (active) => {
    if (!active) menuOpen.value = false
  },
)

async function moveProject(event: KeyboardEvent, id: string, offset: number) {
  if (!canReorder.value) return
  const ids = state.projects.map((project) => project.id)
  const index = ids.indexOf(id)
  const to = index + offset
  if (index < 0 || to < 0 || to >= ids.length) return

  const row = event.currentTarget as HTMLButtonElement
  ids.splice(index, 1)
  ids.splice(to, 0, id)
  reorderProjects(ids)
  await nextTick()
  if (virtualized.value) {
    virtualList.value?.scrollToIndex(to, 'nearest')
    await nextTick()
    const targetButton = virtualList.value
      ?.el()
      ?.querySelector<HTMLButtonElement>(`button[data-project-id="${CSS.escape(id)}"]`)
    targetButton?.focus({ preventScroll: true })
  } else {
    row.focus({ preventScroll: true })
  }
}

/** The project the context menu was opened on; away from a project the page's own menu stays. */
const target = ref<string>()
function targetProject(event: MouseEvent) {
  target.value =
    event.target instanceof Element
      ? event.target.closest<HTMLElement>('[data-project-id]')?.dataset.projectId
      : undefined
  return !!target.value
}

const actions = [
  { key: 'settings', label: t('projectSettings.open'), icon: SettingsIcon },
  { key: 'rename', label: t('project.rename'), icon: PencilIcon },
  { key: 'pulse', label: t('pulse.title'), icon: ActivityIcon },
  { key: 'trace', label: t('trace.coordinator'), icon: ListTreeIcon },
  { key: 'export', label: t('project.export'), icon: DownloadIcon },
  { key: 'delete', label: t('project.delete'), icon: TrashIcon, tone: 'danger' as const },
]

/** The project's real-time collaboration timeline. */
async function openPulse(id: string) {
  if (state.currentId !== id) await openProject(id)
  openMain({ kind: 'pulse' })
}

/** The coordinator's trace, in its project. */
async function openTrace(id: string) {
  const project = state.projects.find((p) => p.id === id)
  if (!project) return
  if (state.currentId !== id) await openProject(id)
  openMain({ kind: 'trace', conversationId: project.coordinatorId })
}

/** The project's own settings, in its project. */
async function openSettings(id: string) {
  if (state.currentId !== id) await openProject(id)
  openProjectSettings()
}

function act(key: unknown, id: string) {
  emit('select')
  if (key === 'settings') void openSettings(id)
  else if (key === 'rename') void rename(id)
  else if (key === 'pulse') void openPulse(id)
  else if (key === 'trace') void openTrace(id)
  else if (key === 'export') void exportProject(id)
  else if (key === 'delete') void confirmDelete(id)
}

async function rename(id: string) {
  const project = state.projects.find((p) => p.id === id)
  if (!project) return
  const name = await dialog.prompt({
    title: t('project.rename'),
    defaultValue: project.name,
    validate: (value) => !!value.trim(),
  })
  if (name?.trim() && name.trim() !== project.name) await renameProject(id, name).catch(report)
}

async function confirmDelete(id: string) {
  const project = state.projects.find((p) => p.id === id)
  if (!project) return
  const threads =
    state.detail?.project.id === id
      ? state.detail.threads
      : ((await api.getProject(id).catch(() => null))?.threads ?? [])
  const working = threads.filter((thread) => thread.stage === 'working').length
  const body = t('project.delete.body')
  await dialog.confirm({
    title: t('project.delete.title', { name: project.name }),
    content: working ? `${t('project.delete.working', { count: working })}${body}` : body,
    tone: 'danger',
    confirmText: t('project.delete.confirm'),
    onConfirm: () => deleteProject(id),
  })
}
</script>

<template>
  <div
    class="projects"
    :class="{
      'projects-compact': compact,
      'projects-popover': inPopover,
      'projects-virtualized': virtualized,
    }"
  >
    <div class="projects-head">
      <DouText variant="secondary" size="sm">{{ t('nav.projects') }}</DouText>
      <DouButton
        variant="ghost"
        size="sm"
        :aria-label="t('nav.newProject')"
        :title="t('nav.newProject')"
        @click="createProject"
      >
        <PlusIcon />
      </DouButton>
    </div>
    <DouContextMenu
      v-if="state.projects.length"
      v-model:open="menuOpen"
      v-model:target="target"
      :items="actions"
      :before-open="targetProject"
      @select="(key) => target && act(key, target)"
    >
      <nav class="projects-nav" :aria-label="t('nav.projects')">
        <DouVirtualList
          v-if="virtualized"
          ref="virtualList"
          class="projects-virtual"
          :items="virtualSortable.list.value"
          item-key="id"
          :item-size="rowStride"
          :overscan="6"
          v-bind="virtualSortable.listAttrs.value"
          @scroll="inspecting = undefined"
        >
          <template #default="{ item, index }">
            <div class="projects-virtual-slot">
              <div
                class="dou-sortable__item is-handle"
                v-bind="virtualSortable.itemAttrs(item, index)"
              >
                <ProjectRow
                  :project="item"
                  :current="state.currentId === item.id && !state.starting"
                  :can-reorder="canReorder"
                  :inspecting="inspecting === item.id"
                  :activity-disabled="activityDisabled"
                  @select="selectProject"
                  @inspect="inspectProject"
                  @move="moveProject"
                />
              </div>
            </div>
          </template>
        </DouVirtualList>
        <DouSortable
          v-else
          :model-value="state.projects"
          :item-label="(project) => project.name"
          :disabled="!canReorder"
          handle
          @update:model-value="(projects) => reorderProjects(projects.map((project) => project.id))"
        >
          <template #default="{ item }">
            <ProjectRow
              :project="item"
              :current="state.currentId === item.id && !state.starting"
              :can-reorder="canReorder"
              :inspecting="inspecting === item.id"
              :activity-disabled="activityDisabled"
              @select="selectProject"
              @inspect="inspectProject"
              @move="moveProject"
            />
          </template>
        </DouSortable>
      </nav>
    </DouContextMenu>
    <DouEmpty v-else size="sm" :description="t('nav.noProjects')" />
  </div>
</template>

<style scoped>
.projects {
  --project-row-base: var(--dou-color-bg-canvas);
  --project-row-min-height: 32px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.projects:not(.projects-popover) {
  padding-bottom: var(--ariadne-safe-bottom);
}

.projects-virtualized:not(.projects-popover) {
  flex: 1;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.projects-popover {
  --project-row-base: var(--dou-popover-bg, var(--dou-color-bg-surface));
  --project-row-min-height: 36px;
}

.projects-compact {
  --project-row-min-height: 44px;
}

.projects-popover .projects-nav {
  max-height: min(60vh, 420px);
  overflow-y: auto;
}

.projects-popover.projects-virtualized .projects-nav {
  display: flex;
  flex-direction: column;
  height: min(60vh, 420px);
  overflow: hidden;
}

.projects-virtualized .projects-nav {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
}

.projects-virtual {
  flex: 1;
  min-height: 0;
}

.projects-virtual-slot {
  box-sizing: border-box;
  padding-bottom: 8px;
}

.projects-head {
  display: flex;
  flex: none;
  padding-inline-start: 8px;
  align-items: center;
  justify-content: space-between;
}
</style>
