<script setup lang="ts">
import { FileIcon } from '@dou-ui/icons'
import { DouLink } from 'dou-ui'
import { computed, ref, watch, type VNode } from 'vue'
import { kindIcon } from '../artifacts/kinds'
import StageIcon from '../components/StageIcon.vue'
import { openThreadFrom, state } from '../store'
import { linkSummary } from './link-summaries'
import type { LinkSummary } from '@ariadne/plugin-sdk'
import { innerLink } from './links'
import { openFilePreview } from '../files/preview'
import PullRequestLink from './PullRequestLink.vue'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/**
 * A link in an agent's message. A thread shows its state and opens beside the coordinator,
 * an artifact opens in the main view, a file opens in a preview; one not in this project is
 * left as its text. A full address a plugin knows, such as a pull request, shows what it
 * names. `bare` is a link written as its address alone.
 */
const props = defineProps<{
  href: string | undefined
  external: boolean
  bare: boolean
  children: VNode
}>()

const inner = computed(() => innerLink(props.href))
const thread = computed(() =>
  inner.value?.kind === 'thread'
    ? (state.detail?.threads.find((item) => item.id === inner.value!.id) ?? null)
    : null,
)
const artifact = computed(() =>
  inner.value?.kind === 'artifact'
    ? (state.artifacts.find((item) => item.id === inner.value!.id) ?? null)
    : null,
)
const file = computed(() => (inner.value?.kind === 'file' ? inner.value.id : null))

const summary = ref<LinkSummary | null>(null)
const trackedPullRequest = computed(() => {
  if (summary.value?.kind !== 'pull-request') return undefined
  return state.detail?.threads.find((item) => item.pullRequest?.url === props.href)
})
watch(
  () => (props.external && props.href?.startsWith('https://') ? props.href : null),
  (url, _before, cleanup) => {
    summary.value = null
    let current = true
    cleanup(() => {
      current = false
    })
    if (url)
      void linkSummary(url).then((value) => {
        if (current) summary.value = value
      })
  },
  { immediate: true },
)

function open(event?: MouseEvent) {
  if (thread.value) openThreadFrom(event, thread.value.id)
  else if (artifact.value) openView({ kind: 'artifact', id: artifact.value.id })
  else if (file.value) {
    const label = props.children.children
    openFilePreview({
      id: file.value,
      name: !props.bare && typeof label === 'string' ? label : undefined,
    })
  }
}
</script>

<template>
  <DouLink v-if="thread || artifact || file" variant="soft" class="inline-ref" @click="open">
    <template #leading>
      <StageIcon
        v-if="thread"
        :stage="thread.stage"
        :pull-request-state="thread.pullRequest?.state"
      />
      <component :is="kindIcon[artifact.kind]" v-else-if="artifact" />
      <FileIcon v-else />
    </template>
    <component :is="children" />
  </DouLink>
  <PullRequestLink
    v-else-if="href && summary?.kind === 'pull-request'"
    :href="href"
    :label="summary.label"
    :children="bare ? null : children"
    :tracked="trackedPullRequest"
  />
  <DouLink v-else-if="href && !inner" :href="href" :external="external">
    <component :is="children" />
  </DouLink>
  <component :is="children" v-else />
</template>

<style scoped>
/* Set off from the words around it: CJK runs have no spaces, so the fill would touch them. */
.inline-ref {
  margin-inline: 0.25em;
}
</style>
