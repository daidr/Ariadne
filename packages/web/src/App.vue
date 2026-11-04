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
import { SettingsIcon } from '@dou-ui/icons'
import { defineAsyncComponent, onMounted, ref, watch, watchEffect } from 'vue'
import AttentionBar from './components/AttentionBar.vue'
import ConversationPanel from './components/ConversationPanel.vue'
import ModelSettings from './components/ModelSettings.vue'
import ProjectList from './components/ProjectList.vue'
import ProjectSource from './components/ProjectSource.vue'
import SidePanel from './components/SidePanel.vue'
import { douLocale, t } from './i18n'
import { onLiveEvent, start, state } from './store'
import { notifyAbout } from './notify'
import { COLOR_MODE_KEY } from './color-mode'

// Puts data-dou-theme on <html>, so the theme's background, text colour and font cover the page.
useColorMode({ storageKey: COLOR_MODE_KEY })
onLiveEvent(notifyAbout)
onMounted(start)

const settingsOpen = ref(false)

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
const ArtifactEditor = defineAsyncComponent(() => import('./artifacts/ArtifactEditor.vue'))
const ArtifactsOverview = defineAsyncComponent(() => import('./artifacts/ArtifactsOverview.vue'))
const ChangesView = defineAsyncComponent(() => import('./changes/ChangesView.vue'))
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
              <AttentionBar />
              <ProjectSource />
              <DouDivider direction="vertical" />
            </template>
            <DouButton
              variant="ghost"
              size="sm"
              :aria-label="t('settings.open')"
              :title="t('settings.open')"
              @click="settingsOpen = true"
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
          <!-- A thread opens on the right, so the coordinator stays in view beside it. -->
          <DouSplitter v-model="split" :min="30" :max="80" class="split">
            <template #start>
              <div class="pane">
                <PlanMain v-if="state.main.kind === 'plan'" />
                <ArtifactEditor v-else-if="state.main.kind === 'artifact'" :id="state.main.id" />
                <ArtifactsOverview v-else-if="state.main.kind === 'artifacts'" />
                <ChangesView
                  v-else-if="state.main.kind === 'changes'"
                  :thread-id="state.main.threadId"
                />
                <ConversationPanel v-else @open-settings="settingsOpen = true" />
              </div>
            </template>
            <template #end>
              <div class="pane">
                <ConversationPanel v-if="state.threadId" :key="state.threadId" thread />
                <SidePanel v-else />
              </div>
            </template>
          </DouSplitter>
        </DouLayoutMain>
      </DouLayout>
    </DouLayout>
    <ModelSettings v-model:open="settingsOpen" />
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
