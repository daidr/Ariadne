<script setup lang="ts">
import {
  ChevronDownIcon,
  ChevronLeftIcon,
  FolderCogIcon,
  PanelLeftIcon,
  PanelRightIcon,
  SettingsIcon,
} from '@dou-ui/icons'
import { DouBadge, DouButton, DouDivider, DouLayoutHeader, DouPopover, DouText } from 'dou-ui'
import { computed, watch } from 'vue'
import { t } from '../i18n'
import { useSourceStatus } from '../source-status'
import { openProjectSettings, showWorkspace, state } from '../store'
import { hasUpdateIndicator, updateIndicatorLabel, updateIndicatorTone } from '../update-restart'
import type { WorkspaceLayoutMode } from '../workspace-layout'
import AriadneLogo from './AriadneLogo.vue'
import AttentionBar from './AttentionBar.vue'
import AutopilotSwitch from './AutopilotSwitch.vue'
import type { SettingsTab } from './ModelSettings.vue'
import ProjectList from './ProjectList.vue'
import ProjectSearch from './ProjectSearch.vue'
import PulseActivityPill from './PulseActivityPill.vue'
import UpdateStatusIndicator from './UpdateStatusIndicator.vue'

const props = defineProps<{
  layoutMode: WorkspaceLayoutMode
  compactActions?: boolean
  projectsCollapsed: boolean
  sideCollapsed?: boolean
  unauthorized?: boolean
}>()
const emit = defineEmits<{
  toggleProjects: []
  toggleSide: []
  openSettings: [tab?: SettingsTab]
}>()
const projectsPopoverOpen = defineModel<boolean>('projectsPopoverOpen', { default: false })

const { summary } = useSourceStatus()
const isNarrow = computed(() => props.layoutMode === 'narrow')
const isMedium = computed(() => props.layoutMode === 'medium')
const isWide = computed(() => props.layoutMode === 'wide')
const actionsCompact = computed(() => props.compactActions ?? isNarrow.value)

const desktopSidebarLabel = computed(() =>
  t(props.projectsCollapsed ? 'nav.expandProjects' : 'nav.collapseProjects'),
)
const mediumSideLabel = computed(() =>
  t(props.sideCollapsed ? 'nav.expandProjectPanel' : 'nav.collapseProjectPanel'),
)
const atProjects = computed(() => state.workspaceLevel === 'projects')
const atSide = computed(() => state.workspaceLevel === 'side')
const backLabel = computed(() => t(atSide.value ? 'nav.backToCoordinator' : 'nav.backToProjects'))
const currentProjectName = computed(
  () => state.projects.find((project) => project.id === state.currentId)?.name ?? null,
)
const title = computed(() => {
  if (props.unauthorized || !isNarrow.value || atProjects.value) return t('app.name')
  if (state.starting) return t('nav.newProject')
  return currentProjectName.value ?? t('app.name')
})
const mediumProjectTitle = computed(() => {
  if (state.starting) return t('nav.newProject')
  return currentProjectName.value ?? t('nav.projects')
})
const projectTriggerLabel = computed(
  () => `${t('nav.switchProject')} · ${mediumProjectTitle.value}`,
)
const otherNeedsMe = computed(() =>
  state.projects.reduce(
    (sum, project) => sum + (project.id === state.currentId ? 0 : project.needsMe),
    0,
  ),
)
const showMediumProjectPopover = computed(
  () => !props.unauthorized && isMedium.value && state.main.kind !== 'trace',
)
const hasSidePane = computed(
  () =>
    !!state.currentId &&
    !state.starting &&
    state.main.kind !== 'trace' &&
    state.main.kind !== 'pulse' &&
    !(state.main.kind === 'plan' && state.planWide),
)
const mergeUpdateIntoSettings = computed(() => isNarrow.value && !atProjects.value)
const showSettingsUpdateBadge = computed(
  () => mergeUpdateIntoSettings.value && hasUpdateIndicator(state.updateStatus),
)
const settingsUpdateTone = computed(() => updateIndicatorTone(state.updateStatus))
const settingsButtonLabel = computed(() =>
  showSettingsUpdateBadge.value
    ? `${t('settings.open')} · ${updateIndicatorLabel(state.updateStatus)}`
    : t('settings.open'),
)
const updateIndicatorCompact = computed(
  () => actionsCompact.value && !(isNarrow.value && atProjects.value),
)

watch(showMediumProjectPopover, (allowed) => {
  if (!allowed) projectsPopoverOpen.value = false
})
</script>

<template>
  <DouLayoutHeader :height="48" :class="{ 'workspace-header-compact': isNarrow }">
    <div class="workspace-header">
      <div class="header-brand">
        <DouButton
          v-if="!unauthorized && isNarrow && !atProjects"
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
          v-else-if="!unauthorized && isWide && state.main.kind !== 'trace'"
          variant="ghost"
          size="sm"
          :aria-label="desktopSidebarLabel"
          :title="desktopSidebarLabel"
          :aria-expanded="!projectsCollapsed"
          @click="emit('toggleProjects')"
        >
          <PanelLeftIcon aria-hidden="true" />
        </DouButton>
        <AriadneLogo v-if="unauthorized || !isNarrow || atProjects" class="header-logo" />
        <DouPopover
          v-if="showMediumProjectPopover"
          v-model:open="projectsPopoverOpen"
          trigger="click"
          placement="bottom-start"
          :width="280"
          :aria-label="t('nav.projects')"
        >
          <DouButton
            variant="ghost"
            size="sm"
            class="header-project-trigger"
            :aria-label="projectTriggerLabel"
            :title="projectTriggerLabel"
            :aria-expanded="projectsPopoverOpen"
          >
            <template #leading>
              <PanelLeftIcon aria-hidden="true" />
            </template>
            <span class="header-project-content">
              <span class="header-project-name">{{ mediumProjectTitle }}</span>
              <DouBadge
                v-if="otherNeedsMe"
                class="header-project-badge"
                :value="otherNeedsMe"
                tone="warning"
              />
            </span>
            <template #trailing>
              <ChevronDownIcon class="header-project-chevron" aria-hidden="true" />
            </template>
          </DouButton>
          <template #content>
            <ProjectList
              surface="popover"
              :active="projectsPopoverOpen"
              @select="projectsPopoverOpen = false"
            />
          </template>
        </DouPopover>
        <DouText v-else weight="medium" truncate>{{ title }}</DouText>
      </div>
      <div v-if="!unauthorized" class="header-actions">
        <ProjectSearch />
        <AttentionBar :compact="actionsCompact" />
        <template v-if="state.detail && (!isNarrow || !atProjects)">
          <PulseActivityPill :compact="actionsCompact" />
          <AutopilotSwitch :compact="actionsCompact" />
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
          <DouDivider v-if="!isNarrow" direction="vertical" />
        </template>
        <UpdateStatusIndicator
          v-if="!mergeUpdateIntoSettings"
          :compact="updateIndicatorCompact"
          @open-settings="emit('openSettings', 'updates')"
        />
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="settingsButtonLabel"
          :title="settingsButtonLabel"
          @click="emit('openSettings')"
        >
          <DouBadge dot :tone="settingsUpdateTone" :hidden="!showSettingsUpdateBadge">
            <SettingsIcon aria-hidden="true" />
          </DouBadge>
        </DouButton>
        <DouButton
          v-if="
            isNarrow &&
            !atProjects &&
            !atSide &&
            state.currentId &&
            !state.starting &&
            state.main.kind !== 'trace' &&
            state.main.kind !== 'pulse'
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
        <DouButton
          v-else-if="isMedium && hasSidePane"
          variant="ghost"
          size="sm"
          :aria-label="mediumSideLabel"
          :title="mediumSideLabel"
          :aria-expanded="!sideCollapsed"
          @click="emit('toggleSide')"
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

.header-logo {
  flex: none;
}

.header-project-trigger {
  max-width: min(280px, 44vw);
  min-width: 0;
}

.header-project-content {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 100%;
}

.header-project-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.header-project-badge,
.header-project-chevron {
  flex: none;
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
