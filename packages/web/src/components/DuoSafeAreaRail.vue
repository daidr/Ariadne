<script setup lang="ts">
import {
  ChevronLeftIcon,
  FolderCogIcon,
  FolderIcon,
  MenuIcon,
  PanelLeftIcon,
  PanelRightIcon,
  PlusIcon,
  SearchIcon,
  SettingsIcon,
} from '@dou-ui/icons'
import { DouBadge, DouButton, DouDivider, DouPopover } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import { t } from '../i18n'
import { resolveDuoBackAction } from '../pwa'
import { backMain, openProjectSettings, showWorkspace, startNewProject, state } from '../store'
import type { WorkspaceLayoutMode } from '../workspace-layout'

const props = defineProps<{
  layoutMode: WorkspaceLayoutMode
  projectsCollapsed: boolean
  sideCollapsed: boolean
  compactHeight?: boolean
  unfoldedLandscape?: boolean
}>()

const emit = defineEmits<{
  openProjects: []
  toggleProjects: []
  toggleSide: []
  openSettings: []
}>()

const menuOpen = ref(false)

const isNarrow = computed(() => props.layoutMode === 'narrow')
const isMedium = computed(() => props.layoutMode === 'medium')
const isWide = computed(() => props.layoutMode === 'wide')
const atProjects = computed(() => state.workspaceLevel === 'projects')
const atSide = computed(() => state.workspaceLevel === 'side')

const hasSidePane = computed(
  () =>
    !!state.currentId &&
    !state.starting &&
    state.main.kind !== 'trace' &&
    !(state.main.kind === 'plan' && state.planWide),
)

const canToggleSide = computed(
  () => hasSidePane.value && ((isNarrow.value && !atProjects.value) || isMedium.value),
)

const sideActive = computed(() =>
  isNarrow.value ? atSide.value : isMedium.value && !props.sideCollapsed,
)

const sideLabel = computed(() => {
  if (isNarrow.value) return t(atSide.value ? 'nav.backToCoordinator' : 'nav.openProjectPanel')
  return t(props.sideCollapsed ? 'nav.expandProjectPanel' : 'nav.collapseProjectPanel')
})

const backAction = computed(() =>
  resolveDuoBackAction({
    mainKind: state.main.kind,
    starting: state.starting,
    hasCurrentProject: !!state.currentId,
    layoutMode: props.layoutMode,
    workspaceLevel: state.workspaceLevel,
  }),
)

const backLabel = computed(() => {
  if (backAction.value === 'to-coordinator') return t('nav.backToCoordinator')
  if (backAction.value === 'to-projects') return t('nav.backToProjects')
  return t('view.back')
})

const totalNeedsMe = computed(() =>
  state.projects.reduce((sum, project) => sum + project.needsMe, 0),
)

const canActOnProjects = computed(() => isNarrow.value || state.main.kind !== 'trace')

const projectsActionLabel = computed(() => {
  if (isNarrow.value) return t(atProjects.value ? 'nav.newProject' : 'nav.projects')
  if (isMedium.value) return t('nav.switchProject')
  return t(props.projectsCollapsed ? 'nav.expandProjects' : 'nav.collapseProjects')
})

function handleBack() {
  menuOpen.value = false
  if (backAction.value === 'main') backMain()
  else if (backAction.value === 'cancel-new') state.starting = false
  else if (backAction.value === 'to-coordinator') showWorkspace('coordinator')
  else if (backAction.value === 'to-projects') showWorkspace('projects')
}

function handleToggleSide() {
  menuOpen.value = false
  if (isNarrow.value) showWorkspace(atSide.value ? 'coordinator' : 'side')
  else if (isMedium.value) emit('toggleSide')
}

function handleProjectsAction() {
  menuOpen.value = false
  if (isNarrow.value) {
    if (atProjects.value) startNewProject()
    else showWorkspace('projects')
    return
  }
  if (isMedium.value) emit('openProjects')
  else emit('toggleProjects')
}

function handleSearch() {
  menuOpen.value = false
  state.searchOpen = true
}

function handleProjectSettings() {
  menuOpen.value = false
  openProjectSettings()
}

function handleSettings() {
  menuOpen.value = false
  emit('openSettings')
}

watch(
  () => [props.layoutMode, state.currentId, state.workspaceLevel, state.main.kind],
  () => {
    menuOpen.value = false
  },
)
</script>

<template>
  <aside
    class="duo-safe-rail"
    :class="{
      'is-compact-height': compactHeight,
      'is-unfolded-landscape': unfoldedLandscape,
    }"
    :aria-label="t('nav.duoRail')"
    data-duo-safe-rail
  >
    <nav class="duo-rail-actions" :aria-label="t('nav.duoRail')">
      <DouPopover
        v-model:open="menuOpen"
        trigger="click"
        placement="left-end"
        :width="216"
        :aria-label="t('nav.menu')"
      >
        <div class="duo-rail-slot">
          <DouButton
            variant="ghost"
            class="duo-rail-button"
            :class="{ 'is-active': menuOpen }"
            :aria-label="t('nav.menu')"
            :title="t('nav.menu')"
            :aria-expanded="menuOpen"
            data-duo-rail-menu
          >
            <MenuIcon aria-hidden="true" />
          </DouButton>
          <DouBadge
            v-if="totalNeedsMe > 0"
            class="duo-rail-badge"
            :value="totalNeedsMe"
            tone="warning"
          />
        </div>
        <template #content>
          <div class="duo-rail-menu" role="menu">
            <DouButton
              v-if="canActOnProjects"
              variant="ghost"
              size="sm"
              class="duo-rail-menu-item"
              @click="handleProjectsAction"
            >
              <template #leading>
                <PlusIcon v-if="isNarrow && atProjects" aria-hidden="true" />
                <PanelLeftIcon v-else-if="isWide" aria-hidden="true" />
                <FolderIcon v-else aria-hidden="true" />
              </template>
              <span class="duo-rail-menu-label">{{ projectsActionLabel }}</span>
              <DouBadge
                v-if="totalNeedsMe > 0 && !(isNarrow && atProjects)"
                class="duo-rail-menu-badge"
                :value="totalNeedsMe"
                tone="warning"
              />
            </DouButton>
            <DouButton
              v-if="canToggleSide"
              variant="ghost"
              size="sm"
              class="duo-rail-menu-item"
              @click="handleToggleSide"
            >
              <template #leading>
                <PanelRightIcon aria-hidden="true" />
              </template>
              <span class="duo-rail-menu-label">{{ sideLabel }}</span>
            </DouButton>
            <DouButton variant="ghost" size="sm" class="duo-rail-menu-item" @click="handleSearch">
              <template #leading>
                <SearchIcon aria-hidden="true" />
              </template>
              <span class="duo-rail-menu-label">{{ t('search.open') }}</span>
            </DouButton>
            <DouDivider />
            <DouButton
              v-if="state.detail"
              variant="ghost"
              size="sm"
              class="duo-rail-menu-item"
              @click="handleProjectSettings"
            >
              <template #leading>
                <FolderCogIcon aria-hidden="true" />
              </template>
              <span class="duo-rail-menu-label">{{ t('projectSettings.open') }}</span>
            </DouButton>
            <DouButton variant="ghost" size="sm" class="duo-rail-menu-item" @click="handleSettings">
              <template #leading>
                <SettingsIcon aria-hidden="true" />
              </template>
              <span class="duo-rail-menu-label">{{ t('settings.open') }}</span>
            </DouButton>
          </div>
        </template>
      </DouPopover>

      <DouButton
        v-if="canToggleSide"
        variant="ghost"
        class="duo-rail-button"
        :class="{ 'is-active': sideActive }"
        :aria-label="sideLabel"
        :title="sideLabel"
        :aria-pressed="sideActive"
        data-duo-rail-side
        @click="handleToggleSide"
      >
        <PanelRightIcon aria-hidden="true" />
      </DouButton>

      <DouButton
        variant="ghost"
        class="duo-rail-button"
        :disabled="!backAction"
        :aria-label="backLabel"
        :title="backLabel"
        data-duo-rail-back
        @click="handleBack"
      >
        <ChevronLeftIcon aria-hidden="true" />
      </DouButton>
    </nav>
  </aside>
</template>

<style scoped>
.duo-safe-rail {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 30;
  box-sizing: border-box;
  width: var(--ariadne-safe-right);
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: flex-end;
  /* Clear iPhone Duo's top-right 84x170 status + camera strip and bottom home indicator. */
  padding: max(180px, calc(16px + var(--ariadne-safe-top))) 8px
    calc(20px + var(--ariadne-safe-bottom));
  border-left: 1px solid var(--dou-color-border-subtle);
  background-color: var(--dou-color-bg-canvas);
  user-select: none;
  -webkit-user-select: none;
}

/* Unfolded landscape (951x669): top-right status strip is 84x120 without the outer camera. */
.duo-safe-rail.is-unfolded-landscape:not(.is-compact-height) {
  padding-top: max(128px, calc(16px + var(--ariadne-safe-top)));
}

/* Folded landscape-left (678x466): status + Dynamic Island sit in the bottom-right corner and may grow upward. */
.duo-safe-rail.is-compact-height {
  justify-content: flex-start;
  padding-top: max(20px, var(--ariadne-safe-top));
  padding-bottom: max(120px, calc(16px + var(--ariadne-safe-bottom)));
}

.duo-rail-actions {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}

.duo-rail-slot {
  position: relative;
  display: inline-flex;
}

.duo-rail-button {
  width: 48px;
  height: 48px;
  padding: 0;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: 16px;
  background-color: var(--dou-color-bg-surface);
  color: var(--dou-color-text-secondary);
  box-shadow: 0 1px 2px rgb(0 0 0 / 4%);
}

.duo-rail-button:hover:not(:disabled) {
  border-color: var(--dou-color-border);
  color: var(--dou-color-text);
}

.duo-rail-button.is-active {
  border-color: var(--dou-color-border);
  background-color: var(--dou-color-bg-muted);
  color: var(--dou-color-text);
}

.duo-rail-button:disabled {
  opacity: 0.36;
  box-shadow: none;
}

.duo-rail-button :deep(svg) {
  width: 20px;
  height: 20px;
}

.duo-rail-badge {
  position: absolute;
  top: -4px;
  right: -4px;
  pointer-events: none;
}

.duo-rail-menu {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.duo-rail-menu-item {
  justify-content: flex-start;
  width: 100%;
  min-height: 36px;
}

.duo-rail-menu-label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  text-align: start;
  white-space: nowrap;
}

.duo-rail-menu-badge {
  flex: none;
  margin-inline-start: auto;
}
</style>
