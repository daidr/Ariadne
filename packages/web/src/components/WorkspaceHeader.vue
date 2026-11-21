<script setup lang="ts">
import {
  ChevronLeftIcon,
  FolderCogIcon,
  PanelLeftIcon,
  PanelRightIcon,
  SettingsIcon,
} from '@dou-ui/icons'
import { DouButton, DouDivider, DouLayoutHeader, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { useSourceStatus } from '../source-status'
import { openProjectSettings, showWorkspace, state } from '../store'
import AttentionBar from './AttentionBar.vue'
import AutopilotSwitch from './AutopilotSwitch.vue'
import ProjectSearch from './ProjectSearch.vue'

const props = defineProps<{ compact: boolean; projectsCollapsed: boolean }>()
const emit = defineEmits<{ toggleProjects: []; openSettings: [] }>()

const { summary } = useSourceStatus()
const desktopSidebarLabel = computed(() =>
  t(props.projectsCollapsed ? 'nav.expandProjects' : 'nav.collapseProjects'),
)
const atProjects = computed(() => state.workspaceLevel === 'projects')
const atSide = computed(() => state.workspaceLevel === 'side')
const backLabel = computed(() => t(atSide.value ? 'nav.backToCoordinator' : 'nav.backToProjects'))
const title = computed(() => {
  if (!props.compact || atProjects.value) return t('app.name')
  if (state.starting) return t('nav.newProject')
  return state.projects.find((project) => project.id === state.currentId)?.name ?? t('app.name')
})
</script>

<template>
  <DouLayoutHeader :height="48" :class="{ 'workspace-header-compact': compact }">
    <div class="workspace-header">
      <div class="header-brand">
        <DouButton
          v-if="compact && !atProjects"
          variant="ghost"
          size="sm"
          class="header-navigation"
          :aria-label="backLabel"
          :title="backLabel"
          @click="showWorkspace(atSide ? 'coordinator' : 'projects')"
        >
          <ChevronLeftIcon aria-hidden="true" />
        </DouButton>
        <DouButton
          v-else-if="!compact && state.main.kind !== 'trace'"
          variant="ghost"
          size="sm"
          :aria-label="desktopSidebarLabel"
          :title="desktopSidebarLabel"
          :aria-expanded="!projectsCollapsed"
          @click="emit('toggleProjects')"
        >
          <PanelLeftIcon aria-hidden="true" />
        </DouButton>
        <DouText weight="medium" truncate>{{ title }}</DouText>
      </div>
      <div class="header-actions">
        <ProjectSearch />
        <template v-if="state.detail && (!compact || !atProjects)">
          <AutopilotSwitch :compact="compact" />
          <AttentionBar :compact="compact" />
          <DouButton
            variant="ghost"
            size="sm"
            :aria-label="t('projectSettings.open')"
            :title="
              summary ? `${t('projectSettings.open')} · ${summary}` : t('projectSettings.open')
            "
            @click="openProjectSettings()"
          >
            <FolderCogIcon aria-hidden="true" />
          </DouButton>
          <DouDivider v-if="!compact" direction="vertical" />
        </template>
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="t('settings.open')"
          :title="t('settings.open')"
          @click="emit('openSettings')"
        >
          <SettingsIcon aria-hidden="true" />
        </DouButton>
        <DouButton
          v-if="
            compact &&
            !atProjects &&
            !atSide &&
            state.currentId &&
            !state.starting &&
            state.main.kind !== 'trace'
          "
          variant="ghost"
          size="sm"
          class="header-navigation"
          :aria-label="t('nav.openProjectPanel')"
          :title="t('nav.openProjectPanel')"
          @click="showWorkspace('side')"
        >
          <PanelRightIcon aria-hidden="true" />
        </DouButton>
      </div>
    </div>
  </DouLayoutHeader>
</template>

<style scoped>
.workspace-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  min-width: 0;
}

.header-actions {
  display: flex;
  flex: none;
  align-items: center;
  gap: 4px;
}

.header-brand {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.workspace-header-compact {
  --dou-layout-padding-x: 8px;
}

.workspace-header-compact .workspace-header {
  gap: 4px;
}

.workspace-header-compact .header-brand,
.workspace-header-compact .header-actions {
  gap: 2px;
}

.header-navigation {
  flex: none;
  min-width: 40px;
  min-height: 40px;
}
</style>
