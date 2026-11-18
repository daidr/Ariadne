<script setup lang="ts">
import {
  DouBadge,
  DouButton,
  DouContextMenu,
  DouEmpty,
  DouSortable,
  DouSortableHandle,
  DouText,
  useDialog,
} from 'dou-ui'
import {
  DownloadIcon,
  FolderIcon,
  ListTreeIcon,
  PencilIcon,
  PlusIcon,
  SettingsIcon,
  TrashIcon,
} from '@dou-ui/icons'
import { ref } from 'vue'
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
  state,
  openProjectSettings,
} from '../store'

const dialog = useDialog()

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
  { key: 'trace', label: t('trace.coordinator'), icon: ListTreeIcon },
  { key: 'export', label: t('project.export'), icon: DownloadIcon },
  { key: 'delete', label: t('project.delete'), icon: TrashIcon, tone: 'danger' as const },
]

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
  if (key === 'settings') void openSettings(id)
  else if (key === 'rename') void rename(id)
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
  <div class="projects">
    <div class="projects-head">
      <DouText variant="secondary" size="sm">{{ t('nav.projects') }}</DouText>
      <DouButton
        variant="ghost"
        size="sm"
        :aria-label="t('nav.newProject')"
        :title="t('nav.newProject')"
        @click="state.starting = true"
      >
        <PlusIcon />
      </DouButton>
    </div>
    <DouContextMenu
      v-if="state.projects.length"
      v-model:target="target"
      :items="actions"
      :before-open="targetProject"
      @select="(key) => target && act(key, target)"
    >
      <nav :aria-label="t('nav.projects')">
        <DouSortable
          :model-value="state.projects"
          :item-label="(project) => project.name"
          :disabled="state.projects.length < 2"
          handle
          @update:model-value="(projects) => reorderProjects(projects.map((project) => project.id))"
        >
          <template #default="{ item }">
            <div class="projects-row" :data-project-id="item.id">
              <button
                type="button"
                class="projects-open"
                :aria-current="state.currentId === item.id && !state.starting ? 'page' : undefined"
                @click="openProject(item.id)"
              >
                <FolderIcon class="projects-folder" aria-hidden="true" />
                <span class="projects-name" :title="item.name">{{ item.name }}</span>
                <DouBadge class="projects-count" :value="item.needsMe" tone="warning" />
              </button>
              <DouSortableHandle
                v-if="state.projects.length > 1"
                class="projects-handle"
                :label="t('project.reorder', { name: item.name })"
              />
            </div>
          </template>
        </DouSortable>
      </nav>
    </DouContextMenu>
    <DouEmpty v-else size="sm" :description="t('nav.noProjects')" />
  </div>
</template>

<style scoped>
.projects {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.projects-head {
  display: flex;
  padding-inline-start: 8px;
  align-items: center;
  justify-content: space-between;
}

.projects-row {
  position: relative;
}

.projects-open {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 32px;
  padding: 0 10px;
  border: 0;
  border-radius: var(--dou-radius-control);
  background: transparent;
  color: var(--dou-color-text-secondary);
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.projects-row:hover .projects-open {
  background: color-mix(in oklab, var(--dou-color-text) 6%, transparent);
  color: var(--dou-color-text);
}

.projects-open[aria-current] {
  background: color-mix(in oklab, var(--dou-color-text) 8%, transparent);
  color: var(--dou-color-text);
  font-weight: var(--dou-font-weight-medium);
}

.projects-open:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: -2px;
}

.projects-folder {
  flex: none;
  width: 16px;
  height: 16px;
}

.projects-handle {
  position: absolute;
  inset-inline-start: 4px;
  top: 50%;
  translate: 0 -50%;
  width: 28px;
  height: 28px;
  opacity: 0;
}

.projects-row:hover .projects-handle,
.projects-row:focus-within .projects-handle {
  opacity: 1;
}

.projects-row:has(.projects-handle):is(:hover, :focus-within) .projects-folder {
  visibility: hidden;
}

@media (hover: none) {
  .projects-handle {
    opacity: 1;
  }

  .projects-row:has(.projects-handle) .projects-folder {
    visibility: hidden;
  }
}

.projects-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.projects-count {
  flex: none;
}
</style>
