<script setup lang="ts">
import {
  DouAlert,
  DouConfigProvider,
  DouLayout,
  DouLayoutAside,
  DouLayoutHeader,
  DouLayoutMain,
  DouText,
} from 'dou-ui'
import { onMounted } from 'vue'
import AttentionBar from './components/AttentionBar.vue'
import CoordinatorChat from './components/CoordinatorChat.vue'
import ProjectList from './components/ProjectList.vue'
import SidePanel from './components/SidePanel.vue'
import { douLocale, t } from './i18n'
import { start, state } from './store'

onMounted(start)
</script>

<template>
  <DouConfigProvider :locale="douLocale">
    <DouLayout class="app">
      <DouLayoutHeader :height="48">
        <div class="header">
          <DouText weight="medium">{{ t('app.name') }}</DouText>
          <AttentionBar />
        </div>
      </DouLayoutHeader>
      <DouAlert v-if="!state.connected" tone="warning" class="banner">
        {{ t('error.connection') }}
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
          <CoordinatorChat />
        </DouLayoutMain>
        <DouLayoutAside :width="420">
          <SidePanel />
        </DouLayoutAside>
      </DouLayout>
    </DouLayout>
  </DouConfigProvider>
</template>

<style>
html,
body,
#app {
  height: 100%;
  margin: 0;
}

body {
  font-family: var(--dou-font-family);
  font-size: var(--dou-font-size-md);
  line-height: var(--dou-font-line-height);
  color: var(--dou-color-text);
  background: var(--dou-color-bg-canvas);
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
