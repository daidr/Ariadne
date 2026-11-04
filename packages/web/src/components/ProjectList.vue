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
import { FolderIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { api } from '../api'
import { t } from '../i18n'
import { createProject, deleteProject, openProject, state } from '../store'

const name = ref('')
const dialog = useDialog()

/** The project the context menu was opened on. */
const target = ref<string | null>(null)
const actions = computed(() =>
  target.value
    ? [{ key: 'delete', label: t('project.delete'), icon: TrashIcon, tone: 'danger' as const }]
    : [],
)

function pick(event: MouseEvent) {
  const item = (event.target as Element | null)?.closest('[data-dou-menu-item]')
  target.value = item?.querySelector<HTMLElement>('[data-project]')?.dataset.project ?? null
  // Away from a project the page's own menu stays.
  if (!target.value) event.stopPropagation()
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
      :items="actions"
      @select="() => target && confirmDelete(target)"
    >
      <div>
        <DouMenu
          :model-value="state.currentId ?? undefined"
          :items="items"
          @select="(key) => openProject(String(key))"
          @contextmenu="pick"
        >
          <template #label="{ item }">
            <span class="projects-label" :data-project="item.key">
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
