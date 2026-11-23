<script setup lang="ts">
import { DouDialog, DouSplitter, DouText } from 'dou-ui'
import { computed, inject, ref } from 'vue'
import ConversationPanel from '../components/ConversationPanel.vue'
import StageIcon from '../components/StageIcon.vue'
import ThreadTools from '../components/ThreadTools.vue'
import { t } from '../i18n'
import { foldLayoutKey } from '../pwa'
import { closeFull, state } from '../store'
import OpenedView from './OpenedView.vue'

const props = defineProps<{ compact?: boolean }>()

/**
 * An artifact or changes over the whole page. Opened from a thread, that thread stays
 * beside it, so the user can read and talk to it at once.
 */
const split = ref(68)
const fold = inject(foldLayoutKey, null)
const isHorizontalFold = computed(
  () =>
    !props.compact &&
    fold?.value.mode === 'horizontal' &&
    fold.value.horizontalSplitPercent !== null,
)
const effectiveSplit = computed({
  get: () => (isHorizontalFold.value ? fold!.value.horizontalSplitPercent! : split.value),
  set: (value) => {
    if (!isHorizontalFold.value) split.value = value
  },
})

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
  const owner = state.detail?.threads.find((th) => th.id === view.threadId)
  return owner ? t('view.changesOf', { title: owner.title }) : ''
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
    <div v-if="full" class="full-shell">
      <DouSplitter
        v-if="threadId && !compact"
        v-model="effectiveSplit"
        :min="40"
        :max="80"
        :data-fold-seam="isHorizontalFold ? 'horizontal' : undefined"
        :disabled="isHorizontalFold"
        class="full-split"
      >
        <template #start>
          <div class="full-pane full-pane-safe-bottom">
            <OpenedView :view="full.view" place="full" />
          </div>
        </template>
        <template #end>
          <div class="full-pane full-thread">
            <div v-if="thread" class="full-thread-header">
              <div class="full-thread-title">
                <StageIcon :stage="thread.stage" :pull-request-state="thread.pullRequest?.state" />
                <DouText size="sm" weight="medium" truncate>{{ thread.title }}</DouText>
              </div>
              <ThreadTools :thread="thread" :label="t('thread.more')" />
            </div>
            <ConversationPanel :key="threadId" :thread-id="threadId" class="full-conversation" />
          </div>
        </template>
      </DouSplitter>
      <div v-else class="full-pane full-pane-safe-bottom">
        <OpenedView :view="full.view" place="full" />
      </div>
    </div>
  </DouDialog>
</template>

<style>
/* The dialog's panel is drawn outside this component: its look is set unscoped. */
.full-view {
  --dou-dialog-padding: 0;
}
</style>

<style scoped>
.full-shell {
  box-sizing: border-box;
  height: 100%;
  min-height: 0;
  padding-top: var(--ariadne-safe-top);
  padding-right: var(--ariadne-safe-right);
  padding-left: var(--ariadne-safe-left);
}

.full-split,
.full-pane {
  box-sizing: border-box;
  height: 100%;
  min-height: 0;
  min-width: 0;
}

.full-pane-safe-bottom {
  padding-bottom: var(--ariadne-safe-bottom);
}

.full-split[data-fold-seam='horizontal'] > :deep(.dou-splitter__pane.is-start) {
  box-sizing: border-box;
  padding-right: calc(var(--ariadne-fold-gap, 0px) / 2);
}

.full-split[data-fold-seam='horizontal'] > :deep(.dou-splitter__pane.is-end) {
  box-sizing: border-box;
  padding-left: calc(var(--ariadne-fold-gap, 0px) / 2);
}

.full-thread {
  display: flex;
  flex-direction: column;
  border-left: 1px solid var(--dou-color-border);
}

.full-thread-header {
  display: flex;
  flex: none;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
  padding: 4px 8px 4px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.full-thread-title {
  display: flex;
  flex: 1;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.full-conversation {
  flex: 1;
  min-height: 0;
}
</style>
