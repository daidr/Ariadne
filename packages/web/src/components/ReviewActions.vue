<script setup lang="ts">
import { DouButton, DouTooltip } from 'dou-ui'
import { CheckIcon, GitMergeIcon, PencilIcon } from '@dou-ui/icons'
import { computed } from 'vue'
import { t } from '../i18n'
import { writeToThread } from '../shortcuts'
import { reviewThread, state } from '../store'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/**
 * The two ways out of a review: the user is through with it, or wants more done, which
 * opens the thread with the caret in its input. `compact`: icons only, named on hover,
 * where a row's title needs the room.
 */
const props = defineProps<{ threadId: string; compact?: boolean }>()

// Changes on a branch Ariadne merges itself: looking through them ends in merging, so that
// is the way out offered, rather than a "seen it" that reads like a yes to merge.
const mergeable = computed(() => {
  const thread = state.detail?.threads.find((item) => item.id === props.threadId)
  const workspace = thread?.workspace
  return !!workspace?.branch && !!workspace.base && !workspace.removed && !thread?.pullRequest
})
function done() {
  if (mergeable.value) openView({ kind: 'changes', threadId: props.threadId })
  else void reviewThread(props.threadId)
}
</script>

<template>
  <span class="review-actions">
    <template v-if="compact">
      <DouTooltip :content="t(mergeable ? 'thread.goMerge' : 'thread.reviewed')">
        <DouButton
          size="xs"
          variant="ghost"
          :aria-label="t(mergeable ? 'thread.goMerge' : 'thread.reviewed')"
          @click="done"
        >
          <GitMergeIcon v-if="mergeable" />
          <CheckIcon v-else />
        </DouButton>
      </DouTooltip>
      <DouTooltip :content="t('thread.reviseAsk')">
        <DouButton
          size="xs"
          variant="ghost"
          :aria-label="t('thread.reviseAsk')"
          @click="writeToThread(props.threadId)"
        >
          <PencilIcon />
        </DouButton>
      </DouTooltip>
    </template>
    <template v-else>
      <DouButton size="sm" variant="ghost" @click="done">
        <template #leading>
          <GitMergeIcon v-if="mergeable" />
          <CheckIcon v-else />
        </template>
        {{ t(mergeable ? 'thread.goMerge' : 'thread.reviewed') }}
      </DouButton>
      <DouButton size="sm" variant="ghost" @click="writeToThread(props.threadId)">
        <template #leading><PencilIcon /></template>
        {{ t('thread.reviseAsk') }}
      </DouButton>
    </template>
  </span>
</template>

<style scoped>
.review-actions {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 2px;
}
</style>
