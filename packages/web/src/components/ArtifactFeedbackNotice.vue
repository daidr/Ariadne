<script setup lang="ts">
import type { ArtifactFeedbackItem, NoticeView } from '@ariadne/core'
import { DouLink, DouShimmer, DouText } from 'dou-ui'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/**
 * A round of what the user did in an artifact, sent to this conversation on its own: a
 * shimmer while the agent works on it, then what it said it did. The items open below.
 */
const props = defineProps<{
  notice: Extract<NoticeView, { kind: 'artifact-feedback' }>
}>()

const MARK = '\u0000'
const open = ref(false)

const count = computed(() =>
  props.notice.items.reduce((n, item) => n + (item.kind === 'board' ? item.changes.length : 1), 0),
)

const parts = computed(() => {
  const key = props.notice.done ? 'notice.feedbackDone' : 'notice.feedbackSent'
  const [before = '', after = ''] = t(key, { title: MARK, count: count.value }).split(MARK)
  return { before, after }
})

function line(item: ArtifactFeedbackItem): string[] {
  switch (item.kind) {
    case 'edit':
      return [t('notice.feedback.edit')]
    case 'choice':
      return [
        item.chosen.length
          ? t('notice.feedback.choice', { titles: item.chosen.map((c) => c.title).join('、') })
          : t('notice.feedback.noChoice'),
      ]
    case 'comment':
      return [t('notice.feedback.comment', { body: item.body })]
    case 'mark':
      return [t('notice.feedback.mark')]
    case 'board':
      return item.changes.map(() => t('notice.feedback.board'))
  }
}
const lines = computed(() => {
  // The same kind of thing said many times reads as one line with a count.
  const counted = new Map<string, number>()
  for (const text of props.notice.items.flatMap(line))
    counted.set(text, (counted.get(text) ?? 0) + 1)
  return [...counted].map(([text, n]) => (n > 1 ? `${text} ×${n}` : text))
})
</script>

<template>
  <div class="feedback">
    <component
      :is="notice.done ? DouText : DouShimmer"
      variant="tertiary"
      size="sm"
      class="feedback-text"
    >
      {{ parts.before
      }}<DouLink underline="hover" @click="openView({ kind: 'artifact', id: notice.artifactId })">{{
        notice.title
      }}</DouLink
      >{{ parts.after
      }}<DouLink underline="hover" class="feedback-toggle" @click="open = !open">{{
        open ? t('notice.feedback.hide') : t('notice.feedback.show')
      }}</DouLink>
    </component>
    <DouText
      v-if="notice.done && notice.summary"
      variant="tertiary"
      size="sm"
      class="feedback-summary"
    >
      {{ notice.summary }}
    </DouText>
    <ul v-if="open" class="feedback-items">
      <li v-for="text in lines" :key="text">
        <DouText variant="tertiary" size="sm">{{ text }}</DouText>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.feedback {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.feedback-toggle {
  margin-left: 8px;
}

.feedback-summary {
  overflow-wrap: anywhere;
}

.feedback-items {
  margin: 0;
  padding: 0 0 0 16px;
}
</style>
