<script setup lang="ts">
import { DouDialog, DouSplitter, DouText } from 'dou-ui'
import { computed, ref } from 'vue'
import ConversationPanel from '../components/ConversationPanel.vue'
import StageIcon from '../components/StageIcon.vue'
import { t } from '../i18n'
import { closeFull, state } from '../store'
import OpenedView from './OpenedView.vue'

/**
 * An artifact or changes over the whole page. Opened from a thread, that thread stays
 * beside it, so the user can read and talk to it at once.
 */
const split = ref(68)

const full = computed(() => state.full)
const threadId = computed(() =>
  full.value?.threadId && state.detail?.threads.some((th) => th.id === full.value!.threadId)
    ? full.value.threadId
    : null,
)
const thread = computed(() => state.detail?.threads.find((th) => th.id === threadId.value) ?? null)
const label = computed(() => {
  const view = full.value?.view
  if (!view) return ''
  if (view.kind === 'artifact') return state.artifacts.find((a) => a.id === view.id)?.title ?? ''
  const thread = state.detail?.threads.find((th) => th.id === view.threadId)
  return thread ? t('view.changesOf', { title: thread.title }) : ''
})
</script>

<template>
  <DouDialog
    :open="!!full"
    fullscreen
    :closable="false"
    destroy-on-close
    class="full-view"
    :aria-label="label"
    @update:open="(open) => !open && closeFull()"
  >
    <template v-if="full">
      <DouSplitter v-if="threadId" v-model="split" :min="40" :max="80" class="full-split">
        <template #start>
          <div class="full-pane">
            <OpenedView :view="full.view" place="full" />
          </div>
        </template>
        <template #end>
          <div class="full-pane full-thread">
            <div v-if="thread" class="full-thread-title">
              <StageIcon :stage="thread.stage" />
              <DouText size="sm" weight="medium" truncate>{{ thread.title }}</DouText>
            </div>
            <ConversationPanel :thread-id="threadId" class="full-conversation" />
          </div>
        </template>
      </DouSplitter>
      <div v-else class="full-pane">
        <OpenedView :view="full.view" place="full" />
      </div>
    </template>
  </DouDialog>
</template>

<style>
/* The dialog's panel is drawn outside this component: its look is set unscoped. */
.full-view {
  --dou-dialog-padding: 0;
}
</style>

<style scoped>
.full-split,
.full-pane {
  height: 100%;
  min-height: 0;
  min-width: 0;
}

.full-thread {
  display: flex;
  flex-direction: column;
  border-left: 1px solid var(--dou-color-border);
}

.full-thread-title {
  display: flex;
  flex: none;
  align-items: center;
  gap: 6px;
  min-width: 0;
  padding: 12px 16px 0;
}

.full-conversation {
  flex: 1;
  min-height: 0;
}
</style>
