<script setup lang="ts">
import type { NoticeView } from '@ariadne/core'
import type { MessageKey } from '@ariadne/i18n'
import { DouLink, DouText } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { openMain } from '../store'

/** A user's edit or a comment on an artifact, with the artifact's title to open it. */
const props = defineProps<{
  notice: Extract<NoticeView, { kind: 'artifact-edited' | 'artifact-comment' }>
}>()

const MARK = '\u0000'

const parts = computed(() => {
  const notice = props.notice
  let key: MessageKey
  if (notice.kind === 'artifact-edited') key = 'notice.artifactEdited'
  else if (notice.by === 'user') key = 'notice.artifactCommentUser'
  else key = notice.action === 'create' ? 'notice.artifactComment' : 'notice.artifactReply'
  const text = t(key, {
    title: MARK,
    ...(notice.kind === 'artifact-edited' ? { version: notice.version } : { body: notice.body }),
  })
  const [before = '', after = ''] = text.split(MARK)
  return { before, after }
})
</script>

<template>
  <DouText variant="tertiary" size="sm" class="artifact-notice">
    {{ parts.before
    }}<DouLink underline="hover" @click="openMain({ kind: 'artifact', id: notice.artifactId })">{{
      notice.title
    }}</DouLink
    >{{ parts.after }}
  </DouText>
</template>

<style scoped>
.artifact-notice {
  display: -webkit-box;
  overflow: hidden;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}
</style>
