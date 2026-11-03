<script setup lang="ts">
import { DouBadge, DouButton, DouEmpty, DouInput, DouMenu, DouText } from 'dou-ui'
import { FolderIcon, PlusIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { createProject, openProject, state } from '../store'

const name = ref('')
const creating = ref(false)

const items = computed(() =>
  state.projects.map((project) => ({ key: project.id, label: project.name, icon: FolderIcon })),
)

async function submit() {
  if (!name.value.trim()) return
  await createProject(name.value)
  name.value = ''
  creating.value = false
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
        @click="creating = !creating"
      >
        <PlusIcon />
      </DouButton>
    </div>
    <form v-if="creating" class="projects-new" @submit.prevent="submit">
      <DouInput v-model="name" size="sm" :placeholder="t('project.create.name')" autofocus />
      <DouButton size="sm" tone="primary" native-type="submit">
        {{ t('project.create.submit') }}
      </DouButton>
    </form>
    <DouMenu
      v-if="items.length"
      :model-value="state.currentId ?? undefined"
      :items="items"
      @select="(key) => openProject(String(key))"
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
    <DouEmpty v-else size="sm" :description="t('nav.noProjects')" />
  </div>
</template>

<style scoped>
.projects {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
}

.projects-head {
  display: flex;
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
