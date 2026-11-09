<script setup lang="ts">
import { DouBadge, DouButton, DouContextMenu, DouEmpty, DouMenu, DouText, useDialog } from 'dou-ui'
import {
  DownloadIcon,
  FolderIcon,
  ListTreeIcon,
  PencilIcon,
  PlusIcon,
  SettingsIcon,
  TrashIcon,
} from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import {
  deleteProject,
  exportProject,
  openMain,
  openProject,
  renameProject,
  report,
  state,
  openProjectSettings,
} from '../store'

const dialog = useDialog()

/** The project the context menu was opened on; away from a project the page's own menu stays. */
const target = ref<string>()
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

const items = computed(() =>
  state.projects.map((project) => ({ key: project.id, label: project.name, icon: FolderIcon })),
)
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
      v-if="items.length"
      v-model:target="target"
      :items="actions"
      :before-open="() => !!target"
      @select="(key) => target && act(key, target)"
    >
      <div>
        <DouMenu
          :model-value="state.starting ? undefined : (state.currentId ?? undefined)"
          :items="items"
          @select="(key) => openProject(String(key))"
          @item-contextmenu="(item) => (target = String(item.key))"
        >
          <template #label="{ item }">
            <span class="projects-label">
              <span>{{ item.label }}</span>
              <DouBadge
                :value="state.projects.find((p) => p.id === item.key)?.needsMe ?? 0"
                tone="warning"
              />
            </span>
          </template>
        </DouMenu>
      </div>
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

.projects-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  gap: 8px;
}
</style>
