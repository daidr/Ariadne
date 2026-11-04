<script setup lang="ts">
import {
  DouBadge,
  DouButton,
  DouContextMenu,
  DouEmpty,
  DouInput,
  DouMenu,
  DouText,
  useDialog,
} from 'dou-ui'
import {
  DownloadIcon,
  FolderIcon,
  ListTreeIcon,
  PlusIcon,
  SettingsIcon,
  TrashIcon,
} from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { createProject, deleteProject, exportProject, openMain, openProject, state } from '../store'

const name = ref('')
const dialog = useDialog()

/** The project the context menu was opened on; away from a project the page's own menu stays. */
const target = ref<string>()
const actions = [
  { key: 'settings', label: t('projectSettings.open'), icon: SettingsIcon },
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
  state.projectSettingsOpen = true
}

function act(key: unknown, id: string) {
  if (key === 'settings') void openSettings(id)
  else if (key === 'trace') void openTrace(id)
  else if (key === 'export') void exportProject(id)
  else if (key === 'delete') void confirmDelete(id)
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

async function submit() {
  if (!name.value.trim()) return
  await createProject(name.value)
  name.value = ''
  state.creatingProject = false
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
        @click="state.creatingProject = !state.creatingProject"
      >
        <PlusIcon />
      </DouButton>
    </div>
    <form v-if="state.creatingProject" class="projects-new" @submit.prevent="submit">
      <DouInput v-model="name" size="sm" :placeholder="t('project.create.name')" autofocus />
      <DouButton size="sm" tone="primary" native-type="submit">
        {{ t('project.create.submit') }}
      </DouButton>
    </form>
    <DouContextMenu
      v-if="items.length"
      v-model:target="target"
      :items="actions"
      :before-open="() => !!target"
      @select="(key) => target && act(key, target)"
    >
      <div>
        <DouMenu
          :model-value="state.currentId ?? undefined"
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

.projects-new {
  display: flex;
  gap: 8px;
}

.projects-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  gap: 8px;
}
</style>
