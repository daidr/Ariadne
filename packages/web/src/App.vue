<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouColorModeButton,
  DouConfigProvider,
  DouLayout,
  DouLayoutAside,
  DouLayoutHeader,
  DouLayoutMain,
  DouText,
  useColorMode,
} from 'dou-ui'
import { SettingsIcon } from '@dou-ui/icons'
import { onMounted, ref } from 'vue'
import AttentionBar from './components/AttentionBar.vue'
import ConversationPanel from './components/ConversationPanel.vue'
import ModelSettings from './components/ModelSettings.vue'
import ProjectList from './components/ProjectList.vue'
import SidePanel from './components/SidePanel.vue'
import { douLocale, t } from './i18n'
import { start, state } from './store'
import { COLOR_MODE_KEY } from './color-mode'

// Puts data-dou-theme on <html>, so the theme's background, text colour and font cover the page.
useColorMode({ storageKey: COLOR_MODE_KEY })
onMounted(start)

const settingsOpen = ref(false)
</script>

<template>
  <DouConfigProvider :locale="douLocale">
    <DouLayout class="app">
      <DouLayoutHeader :height="48">
        <div class="header">
          <DouText weight="medium">{{ t('app.name') }}</DouText>
          <div class="header-actions">
            <AttentionBar />
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
      <DouLayout class="body">
        <DouLayoutAside :width="220">
          <ProjectList />
        </DouLayoutAside>
        <DouLayoutMain class="main">
          <ConversationPanel @open-settings="settingsOpen = true" />
        </DouLayoutMain>
        <DouLayoutAside :width="420">
          <SidePanel />
        </DouLayoutAside>
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
</style>
