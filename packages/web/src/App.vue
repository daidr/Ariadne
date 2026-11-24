<script setup lang="ts">
import {
  DouConfigProvider,
  DouLayout,
  DouLayoutAside,
  DouLayoutMain,
  DouSplitter,
  DouToastViewport,
  useBreakpoints,
  useColorMode,
  type ComponentDefaults,
} from 'dou-ui'
import { computed, onScopeDispose, provide, ref, watch, watchEffect } from 'vue'
import { defineAsyncView } from './components/async-view'
import ConversationPanel from './components/ConversationPanel.vue'
import DuoSafeAreaRail from './components/DuoSafeAreaRail.vue'
import ModelSettings, { type SettingsTab } from './components/ModelSettings.vue'
import ProjectList from './components/ProjectList.vue'
import ShortcutSheet from './components/ShortcutSheet.vue'
import StartScreen from './components/StartScreen.vue'
import ProjectSettings from './components/ProjectSettings.vue'
import PluginConfigDialog from './components/PluginConfigDialog.vue'
import UnauthorizedScreen from './components/UnauthorizedScreen.vue'
import WorkspaceHeader from './components/WorkspaceHeader.vue'
import SideLayout from './side/SideLayout.vue'
import FullView from './full/FullView.vue'
import FilePreview from './files/FilePreview.vue'
import { douLocale, t } from './i18n'
import { highlightCodeBlock } from './highlighting/code-block'
import { onLiveEvent, state } from './store'
import { notifyAbout } from './notify'
import { COLOR_MODE_KEY, watchThemeColor } from './color-mode'
import { useShortcuts } from './shortcuts'
import { openSettingsKey } from './open-settings'
import { useSidebarLayout } from './sidebar-layout'
import { foldLayoutKey, usePwaEnvironment } from './pwa'
import { useStreamStatusToast } from './stream-toast'
import { resolveWorkspaceLayout, resolveWorkspacePanes } from './workspace-layout'
import { useWorkspaceRouting } from './workspace-routing'

// Puts data-dou-theme on <html>, so the theme's background, text colour and font cover the page.
useColorMode({ storageKey: COLOR_MODE_KEY })
onScopeDispose(watchThemeColor())
onLiveEvent(notifyAbout)
useWorkspaceRouting()
useShortcuts()
useStreamStatusToast()
const { duoRail, fold } = usePwaEnvironment()
provide(foldLayoutKey, fold)

const douDefaults: ComponentDefaults = {
  DouCodeBlock: {
    highlight: highlightCodeBlock,
  },
}

const settingsOpen = ref(false)
const settingsTab = ref<SettingsTab>('models')
function openSettings(tab: SettingsTab = 'models') {
  settingsTab.value = tab
  settingsOpen.value = true
}
provide(openSettingsKey, openSettings)

const breakpoints = useBreakpoints()
const layoutMode = computed(() => resolveWorkspaceLayout(breakpoints))
const isNarrow = computed(() => layoutMode.value === 'narrow')
const isMedium = computed(() => layoutMode.value === 'medium')
const isWide = computed(() => layoutMode.value === 'wide')
const compactActions = computed(() => !breakpoints.lg)
const unauthorized = computed(() => state.stream === 'unauthorized')

const {
  size: projectSize,
  min: projectMin,
  max: projectMax,
  collapsed: projectsCollapsed,
  save: saveProjectSidebar,
} = useSidebarLayout('projects')

const projectsPopoverOpen = ref(false)
const mediumSideCollapsed = ref(false)
const panes = computed(() =>
  resolveWorkspacePanes({
    mode: layoutMode.value,
    workspaceLevel: state.workspaceLevel,
    projectsCollapsed: projectsCollapsed.value,
    mediumSideCollapsed: mediumSideCollapsed.value,
  }),
)

function toggleProjectSidebar(collapsed: boolean) {
  if (isWide.value) projectsCollapsed.value = collapsed
}

function toggleMediumSide() {
  if (isMedium.value) mediumSideCollapsed.value = !mediumSideCollapsed.value
}

watch(
  () => state.sideRevealNonce,
  () => {
    if (isMedium.value) mediumSideCollapsed.value = false
  },
)

// How much of the room the left pane takes; the right one holds the project or a thread.
const SPLIT_KEY = 'ariadne.split'
const split = ref(readSplit())
function readSplit() {
  try {
    const value = Number(localStorage.getItem(SPLIT_KEY))
    return value >= 30 && value <= 80 ? value : 62
  } catch {
    return 62
  }
}
watch(split, (value) => {
  try {
    localStorage.setItem(SPLIT_KEY, String(Math.round(value)))
  } catch {
    // Not remembered; the default comes back next time.
  }
})

// Align the coordinator/side divider with the physical hinge when half-folded like a book
// and the inner splitter spans the full safe width (no wide project sidebar beside it),
// without overwriting the user's saved flat-screen split preference.
const isHorizontalFold = computed(
  () =>
    !isNarrow.value &&
    !(isMedium.value && mediumSideCollapsed.value) &&
    !(isWide.value && !projectsCollapsed.value) &&
    fold.value.mode === 'horizontal' &&
    fold.value.horizontalSplitPercent !== null,
)
const contentSplit = computed({
  get: () => (isHorizontalFold.value ? fold.value.horizontalSplitPercent! : split.value),
  set: (value) => {
    if (!isHorizontalFold.value) split.value = value
  },
})

// The flow and document editors are large; load them when first opened.
const PlanMain = defineAsyncView(() => import('./plan/PlanMain.vue'))
const ArtifactsOverview = defineAsyncView(() => import('./artifacts/ArtifactsOverview.vue'))
const TraceView = defineAsyncView(() => import('./trace/TraceView.vue'))

// The tab says how many things wait on the user, across projects.
watchEffect(() => {
  const waiting = state.projects.reduce((sum, project) => sum + project.needsMe, 0)
  document.title = waiting ? `(${waiting}) ${t('app.name')}` : t('app.name')
})
</script>

<template>
  <DouConfigProvider :locale="douLocale" :defaults="douDefaults">
    <DouLayout class="app">
      <WorkspaceHeader
        v-model:projects-popover-open="projectsPopoverOpen"
        :layout-mode="layoutMode"
        :compact-actions="compactActions"
        :projects-collapsed="projectsCollapsed"
        :side-collapsed="mediumSideCollapsed"
        :unauthorized="unauthorized"
        @toggle-projects="toggleProjectSidebar(!projectsCollapsed)"
        @toggle-side="toggleMediumSide()"
        @open-settings="openSettings()"
      />
      <DouLayoutMain v-if="unauthorized" class="body main">
        <UnauthorizedScreen />
      </DouLayoutMain>
      <!-- The trace is a wide table with its details beside it: it takes the whole page. -->
      <TraceView
        v-else-if="state.main.kind === 'trace' && panes.coordinatorVisible"
        :conversation-id="state.main.conversationId"
        class="body"
      />
      <div v-else class="body">
        <DouSplitter
          v-model="projectSize"
          unit="px"
          :collapsed="isWide && projectsCollapsed"
          :min="projectMin"
          :max="projectMax"
          :single-pane="panes.projectSplitSinglePane"
          :aria-label="t('nav.resizeProjects')"
          class="project-split workspace-split"
          :disabled="!isWide"
          collapsible
          @update:collapsed="toggleProjectSidebar"
          @change="saveProjectSidebar"
        >
          <template #start>
            <DouLayoutAside width="100%" :bordered="false" class="project-sidebar">
              <ProjectList :compact="isNarrow" :active="panes.projectsSidebarVisible" />
            </DouLayoutAside>
          </template>
          <template #end>
            <DouLayoutMain class="main">
              <div v-if="!state.currentId || state.starting" class="pane">
                <StartScreen class="start-panel" />
              </div>
              <div v-else-if="state.main.kind === 'plan' && state.planWide" class="pane">
                <PlanMain />
              </div>
              <DouSplitter
                v-else
                v-model="contentSplit"
                :min="isMedium ? '320px' : 30"
                :max="80"
                :min-end="isMedium ? '280px' : undefined"
                :single-pane="panes.contentSplitSinglePane"
                :data-fold-seam="isHorizontalFold ? 'horizontal' : undefined"
                :disabled="isNarrow || (isMedium && mediumSideCollapsed) || isHorizontalFold"
                class="split workspace-split"
              >
                <template #start>
                  <div class="pane">
                    <PlanMain v-if="state.main.kind === 'plan'" />
                    <ArtifactsOverview v-else-if="state.main.kind === 'artifacts'" />
                    <ConversationPanel
                      v-else
                      :key="state.currentId ?? undefined"
                      :active="panes.coordinatorVisible"
                    />
                  </div>
                </template>
                <template #end>
                  <div class="pane">
                    <SideLayout
                      :compact="isNarrow"
                      :active="panes.sideVisible && (!isNarrow || !state.full)"
                    />
                  </div>
                </template>
              </DouSplitter>
            </DouLayoutMain>
          </template>
        </DouSplitter>
      </div>
    </DouLayout>
    <DuoSafeAreaRail
      v-if="duoRail.active && !unauthorized"
      :layout-mode="layoutMode"
      :projects-collapsed="projectsCollapsed"
      :side-collapsed="mediumSideCollapsed"
      :compact-height="duoRail.compactHeight"
      :unfolded-landscape="fold.mode === 'horizontal'"
      @open-projects="projectsPopoverOpen = true"
      @toggle-projects="toggleProjectSidebar(!projectsCollapsed)"
      @toggle-side="toggleMediumSide()"
      @open-settings="openSettings()"
    />
    <template v-if="!unauthorized">
      <ModelSettings
        v-model:open="settingsOpen"
        v-model:tab="settingsTab"
        :layout-mode="layoutMode"
      />
      <PluginConfigDialog />
      <ShortcutSheet />
      <ProjectSettings :layout-mode="layoutMode" />
      <FullView :compact="isNarrow" />
      <FilePreview />
    </template>
    <DouToastViewport />
  </DouConfigProvider>
</template>

<style>
html,
body,
#app {
  height: 100%;
  margin: 0;
}

.app {
  height: 100%;
  box-sizing: border-box;
  padding-top: var(--ariadne-safe-top);
  padding-left: var(--ariadne-safe-left);
  padding-right: var(--ariadne-safe-right);
  background-color: var(--dou-color-bg-canvas);
}

.body {
  flex: 1;
  min-height: 0;
}

.project-split {
  --dou-splitter-line: var(--dou-color-border-subtle);
}

.project-sidebar {
  height: 100%;
}

.main {
  --dou-layout-main-padding: 0;
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
}

.split {
  flex: 1;
  min-height: 0;
}

/* Keep both panes clear of the physical fold crease in book posture. */
.split[data-fold-seam='horizontal'] > .dou-splitter__pane.is-start {
  box-sizing: border-box;
  padding-right: calc(var(--ariadne-fold-gap, 0px) / 2);
}

.split[data-fold-seam='horizontal'] > .dou-splitter__pane.is-end {
  box-sizing: border-box;
  padding-left: calc(var(--ariadne-fold-gap, 0px) / 2);
}

.pane {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
}

.workspace-split.is-single-pane .start-panel {
  overflow: auto;
  align-items: safe center;
  padding-top: 24px;
  padding-bottom: max(24px, var(--ariadne-safe-bottom));
}
</style>
