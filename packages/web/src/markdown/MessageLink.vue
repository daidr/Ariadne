<script setup lang="ts">
import { ImageIcon } from '@dou-ui/icons'
import { DouImagePreview, DouLink } from 'dou-ui'
import { computed, ref, watch, type VNode } from 'vue'
import { kindIcon } from '../artifacts/kinds'
import StageIcon from '../components/StageIcon.vue'
import { openThreadFrom, state } from '../store'
import { askLinkSummary, linkSummary } from './link-summaries'
import { fileSrc, innerLink } from './links'
import PullRequestLink from './PullRequestLink.vue'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

/**
 * A link in an agent's message. A thread shows its state and opens beside the coordinator,
 * an artifact opens in the main view, a kept image opens large; one not in this project is
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
const image = computed(() => (inner.value?.kind === 'file' ? fileSrc(inner.value.id) : null))
const viewing = ref(false)

watch(
  () => (props.external && props.href?.startsWith('https://') ? props.href : null),
  (url) => url && askLinkSummary(url),
  { immediate: true },
)
const summary = computed(() => (props.href ? linkSummary(props.href) : null))

function open(event?: MouseEvent) {
  if (thread.value) openThreadFrom(event, thread.value.id)
  else if (artifact.value) openView({ kind: 'artifact', id: artifact.value.id })
  else if (image.value) viewing.value = true
}
</script>

<template>
  <DouLink v-if="thread || artifact || image" variant="soft" class="inline-ref" @click="open">
    <template #leading>
      <StageIcon v-if="thread" :stage="thread.stage" />
      <component :is="kindIcon[artifact.kind]" v-else-if="artifact" />
      <ImageIcon v-else />
    </template>
    <component :is="children" />
    <DouImagePreview v-if="image" v-model:open="viewing" :images="[image]" />
  </DouLink>
  <PullRequestLink
    v-else-if="href && summary?.kind === 'pull-request'"
    :href="href"
    :label="summary.label"
    :children="bare ? null : children"
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
