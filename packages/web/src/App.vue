<script setup lang="ts">
import {
  DouAlert,
  DouConfigProvider,
  DouLayout,
  DouLayoutAside,
  DouLayoutMain,
  DouSplitter,
  DouToastViewport,
  useBreakpoints,
  useColorMode,
} from 'dou-ui'
import {
  computed,
  defineAsyncComponent,
  onScopeDispose,
  provide,
  ref,
  useTemplateRef,
  watch,
  watchEffect,
} from 'vue'
import ConversationPanel from './components/ConversationPanel.vue'
import ModelSettings, { type SettingsTab } from './components/ModelSettings.vue'
import ProjectList from './components/ProjectList.vue'
import ShortcutSheet from './components/ShortcutSheet.vue'
import StartScreen from './components/StartScreen.vue'
import ProjectSettings from './components/ProjectSettings.vue'
import PluginConfigDialog from './components/PluginConfigDialog.vue'
import WorkspaceHeader from './components/WorkspaceHeader.vue'
import SideLayout from './side/SideLayout.vue'
import FullView from './full/FullView.vue'
import FilePreview from './files/FilePreview.vue'
import { douLocale, t } from './i18n'
import { onLiveEvent, state } from './store'
import { notifyAbout } from './notify'
import { COLOR_MODE_KEY, watchThemeColor } from './color-mode'
import { useShortcuts } from './shortcuts'
import { openSettingsKey } from './open-settings'
import { useSidebarLayout } from './sidebar-layout'
import { useWorkspaceRouting } from './workspace-routing'

// Puts data-dou-theme on <html>, so the theme's background, text colour and font cover the page.
useColorMode({ storageKey: COLOR_MODE_KEY })
onScopeDispose(watchThemeColor())
onLiveEvent(notifyAbout)
useWorkspaceRouting()
useShortcuts()

const settingsOpen = ref(false)
const settingsTab = ref<SettingsTab>('models')
function openSettings(tab: SettingsTab = 'models') {
  settingsTab.value = tab
  settingsOpen.value = true
}
provide(openSettingsKey, openSettings)

const breakpoints = useBreakpoints()
const compact = computed(() => !breakpoints.lg)
const coordinatorVisible = computed(() => !compact.value || state.workspaceLevel === 'coordinator')
const sideVisible = computed(() => !compact.value || state.workspaceLevel === 'side')

const projectBody = useTemplateRef<HTMLElement>('projectBody')
const {
  size: projectSize,
  min: projectMin,
  max: projectMax,
  collapsed: projectsCollapsed,
  save: saveProjectSidebar,
} = useSidebarLayout('projects', projectBody)
function toggleProjectSidebar(collapsed: boolean) {
  if (!compact.value) projectsCollapsed.value = collapsed
}

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

// The flow and document editors are large; load them when first opened.
const PlanMain = defineAsyncComponent(() => import('./plan/PlanMain.vue'))
const ArtifactsOverview = defineAsyncComponent(() => import('./artifacts/ArtifactsOverview.vue'))
const TraceView = defineAsyncComponent(() => import('./trace/TraceView.vue'))

// The tab says how many things wait on the user, across projects.
watchEffect(() => {
  const waiting = state.projects.reduce((sum, project) => sum + project.needsMe, 0)
  document.title = waiting ? `(${waiting}) ${t('app.name')}` : t('app.name')
})
</script>

<template>
  <DouConfigProvider :locale="douLocale">
    <DouLayout class="app">
      <WorkspaceHeader
        :compact="compact"
        :projects-collapsed="projectsCollapsed"
        @toggle-projects="toggleProjectSidebar(!projectsCollapsed)"
        @open-settings="openSettings()"
      />
      <DouAlert v-if="state.stream === 'lost'" tone="warning" class="banner">
        {{ t('error.connection') }}
      </DouAlert>
      <DouAlert v-else-if="state.stream === 'unauthorized'" tone="danger" class="banner">
        {{ t('error.unauthorized') }}
      </DouAlert>
      <!-- The trace is a wide table with its details beside it: it takes the whole page. -->
      <TraceView
        v-if="state.main.kind === 'trace' && coordinatorVisible"
        :conversation-id="state.main.conversationId"
        class="body"
      />
      <div v-else ref="projectBody" class="body">
        <DouSplitter
          v-model="projectSize"
          :collapsed="!compact && projectsCollapsed"
          :min="projectMin"
          :max="projectMax"
          :aria-label="t('nav.resizeProjects')"
          :data-single-pane="
            compact ? (state.workspaceLevel === 'projects' ? 'start' : 'end') : undefined
          "
          class="project-split workspace-split"
          :disabled="compact"
          collapsible
          @update:collapsed="toggleProjectSidebar"
          @change="saveProjectSidebar"
        >
          <template #start>
            <DouLayoutAside width="100%" :bordered="false" class="project-sidebar">
              <ProjectList :compact="compact" />
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
                v-model="split"
                :min="30"
                :max="80"
                :data-single-pane="compact ? (sideVisible ? 'end' : 'start') : undefined"
                :disabled="compact"
                class="split workspace-split"
              >
                <template #start>
                  <div class="pane">
                    <PlanMain v-if="state.main.kind === 'plan'" />
                    <ArtifactsOverview v-else-if="state.main.kind === 'artifacts'" />
                    <ConversationPanel v-else :active="coordinatorVisible" />
                  </div>
                </template>
                <template #end>
                  <div class="pane">
                    <SideLayout
                      :compact="compact"
                      :active="sideVisible && (!compact || !state.full)"
                    />
                  </div>
                </template>
              </DouSplitter>
            </DouLayoutMain>
          </template>
        </DouSplitter>
      </div>
    </DouLayout>
    <ModelSettings v-model:open="settingsOpen" v-model:tab="settingsTab" />
    <PluginConfigDialog />
    <ShortcutSheet />
    <ProjectSettings />
    <FullView :compact="compact" />
    <FilePreview />
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

/* alpha.18's copy-button base rule otherwise overrides the code block's placement. */
.dou-code-block > .dou-code-block__copy {
  position: absolute;
}

.app {
  height: 100%;
}

.banner {
  margin: 8px 16px 0;
}

.body {
  flex: 1;
  min-height: 0;
}

.project-split {
  --dou-splitter-line: var(--dou-color-border-subtle);
}

/* A collapsed outer splitter also matches the start panes of nested splitters. */
.project-split.is-collapsed
  > .dou-splitter__pane.is-end
  .dou-splitter:not(.is-collapsed)
  > .dou-splitter__pane.is-start {
  visibility: inherit;
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

.pane {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
}

/* Keep both slots mounted so switching levels keeps the chat draft and scroll position. */
.workspace-split[data-single-pane] {
  display: block;
}

.workspace-split[data-single-pane] > .dou-splitter__pane {
  height: 100%;
}

.workspace-split[data-single-pane] > .dou-splitter__bar,
.workspace-split[data-single-pane='start'] > .dou-splitter__pane.is-end,
.workspace-split[data-single-pane='end'] > .dou-splitter__pane.is-start {
  display: none;
}

.workspace-split[data-single-pane] .start-panel {
  overflow: auto;
  align-items: safe center;
  padding-block: 24px;
}
</style>
