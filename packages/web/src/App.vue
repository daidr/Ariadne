<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouColorModeButton,
  DouConfigProvider,
  DouDivider,
  DouLayout,
  DouLayoutAside,
  DouLayoutHeader,
  DouLayoutMain,
  DouSplitter,
  DouText,
  useColorMode,
} from 'dou-ui'
import { FolderCogIcon, SettingsIcon } from '@dou-ui/icons'
import { defineAsyncComponent, onMounted, provide, ref, watch, watchEffect } from 'vue'
import AttentionBar from './components/AttentionBar.vue'
import AutopilotSwitch from './components/AutopilotSwitch.vue'
import ConversationPanel from './components/ConversationPanel.vue'
import ModelSettings, { type SettingsTab } from './components/ModelSettings.vue'
import ProjectList from './components/ProjectList.vue'
import ProjectSearch from './components/ProjectSearch.vue'
import ProjectSource from './components/ProjectSource.vue'
import ProjectUsage from './components/ProjectUsage.vue'
import ShortcutSheet from './components/ShortcutSheet.vue'
import ProjectSettings from './components/ProjectSettings.vue'
import SideLayout from './side/SideLayout.vue'
import FullView from './full/FullView.vue'
import { douLocale, t } from './i18n'
import { onLiveEvent, openProjectSettings, start, state } from './store'
import { notifyAbout } from './notify'
import { COLOR_MODE_KEY } from './color-mode'
import { useShortcuts } from './shortcuts'
import { openSettingsKey } from './open-settings'

// Puts data-dou-theme on <html>, so the theme's background, text colour and font cover the page.
useColorMode({ storageKey: COLOR_MODE_KEY })
onLiveEvent(notifyAbout)
onMounted(start)
useShortcuts()

const settingsOpen = ref(false)
const settingsTab = ref<SettingsTab>('models')
function openSettings(tab: SettingsTab = 'models') {
  settingsTab.value = tab
  settingsOpen.value = true
}
provide(openSettingsKey, openSettings)

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
      <DouLayoutHeader :height="48">
        <div class="header">
          <DouText weight="medium">{{ t('app.name') }}</DouText>
          <div class="header-actions">
            <!-- What belongs to the open project, set apart from the app's own buttons. -->
            <template v-if="state.detail">
              <ProjectSearch />
              <AutopilotSwitch />
              <AttentionBar />
              <ProjectSource />
              <ProjectUsage />
              <DouButton
                variant="ghost"
                size="sm"
                :aria-label="t('projectSettings.open')"
                :title="t('projectSettings.open')"
                @click="openProjectSettings()"
              >
                <FolderCogIcon />
              </DouButton>
              <DouDivider direction="vertical" />
            </template>
            <DouButton
              variant="ghost"
              size="sm"
              :aria-label="t('settings.open')"
              :title="t('settings.open')"
              @click="openSettings()"
            >
              <SettingsIcon />
            </DouButton>
            <DouColorModeButton />
          </div>
        </div>
      </DouLayoutHeader>
      <DouAlert v-if="state.stream === 'lost'" tone="warning" class="banner">
        {{ t('error.connection') }}
      </DouAlert>
      <DouAlert v-else-if="state.stream === 'unauthorized'" tone="danger" class="banner">
        {{ t('error.unauthorized') }}
      </DouAlert>
      <DouAlert
        v-if="state.error"
        tone="danger"
        closable
        class="banner"
        @close="state.error = null"
      >
        {{ state.error }}
      </DouAlert>
      <!-- The trace is a wide table with its details beside it: it takes the whole page. -->
      <TraceView
        v-if="state.main.kind === 'trace'"
        :conversation-id="state.main.conversationId"
        class="body"
      />
      <DouLayout v-else class="body">
        <DouLayoutAside :width="220">
          <ProjectList />
        </DouLayoutAside>
        <DouLayoutMain class="main">
          <!-- Threads open on the right, so the coordinator stays in view beside them. -->
          <!-- The plan shown large takes the side too, so it is not drawn twice. -->
          <div v-if="state.main.kind === 'plan' && state.planWide" class="pane">
            <PlanMain />
          </div>
          <!-- Before there is a project, the side has nothing to show. -->
          <div v-else-if="!state.currentId" class="pane">
            <ConversationPanel />
          </div>
          <DouSplitter v-else v-model="split" :min="30" :max="80" class="split">
            <template #start>
              <div class="pane">
                <PlanMain v-if="state.main.kind === 'plan'" />
                <ArtifactsOverview v-else-if="state.main.kind === 'artifacts'" />
                <ConversationPanel v-else />
              </div>
            </template>
            <template #end>
              <div class="pane">
                <SideLayout />
              </div>
            </template>
          </DouSplitter>
        </DouLayoutMain>
      </DouLayout>
    </DouLayout>
    <ModelSettings v-model:open="settingsOpen" v-model:tab="settingsTab" />
    <ShortcutSheet />
    <ProjectSettings />
    <FullView />
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
}

.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 4px;
}

.banner {
  margin: 8px 16px 0;
}

.body {
  flex: 1;
  min-height: 0;
}

.main {
  --dou-layout-main-padding: 0;
  display: flex;
  flex-direction: column;
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
</style>
