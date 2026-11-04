<script setup lang="ts">
import { DouLink } from 'dou-ui'
import { computed, type VNode } from 'vue'
import { kindIcon } from '../artifacts/kinds'
import StageIcon from '../components/StageIcon.vue'
import { openMain, openThread, state } from '../store'
import { innerLink } from './links'

/**
 * A link in an agent's message. A thread shows its state and opens beside the coordinator,
 * an artifact opens in the main view; one not in this project is left as its text.
 */
const props = defineProps<{ href: string | undefined; external: boolean; children: VNode }>()

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

function open() {
  if (thread.value) openThread(thread.value.id)
  else if (artifact.value) openMain({ kind: 'artifact', id: artifact.value.id })
}
</script>

<template>
  <!-- Without an address a link cannot take focus, so it is made a focusable link here. -->
  <DouLink
    v-if="thread || artifact"
    class="message-link"
    role="link"
    tabindex="0"
    @click="open"
    @keydown.enter.prevent="open"
  >
    <template #leading>
      <StageIcon v-if="thread" :stage="thread.stage" />
      <component :is="kindIcon[artifact!.kind]" v-else />
    </template>
    <component :is="children" />
  </DouLink>
  <DouLink v-else-if="href && !inner" :href="href" :external="external">
    <component :is="children" />
  </DouLink>
  <component :is="children" v-else />
</template>

<style scoped>
.message-link {
  cursor: pointer;
}
</style>
