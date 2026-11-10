<script setup lang="ts">
import type { ConnectorBlock } from '@ariadne/core'
import { DouButton, DouChatArtifact, DouText } from 'dou-ui'
import { McpServerIcon } from '@dou-ui/icons'
import { computed, inject, ref, watch } from 'vue'
import { api, type McpServerView } from '../api'
import { t } from '../i18n'
import { openSettingsKey, sameServer, suggestedServer } from '../open-settings'
import { state } from '../store'

/** An external service an agent suggested: one press opens the settings with it filled in. */
const props = defineProps<{ block: ConnectorBlock }>()

const openSettings = inject(openSettingsKey, () => {})
const servers = ref<McpServerView[] | null>(null)
// Looked up again when the project's servers change, as saving a suggested one does.
watch(
  () => state.detail?.project.mcp.join(),
  async () => {
    servers.value = (await api.getMcpSettings().catch(() => null))?.servers ?? null
  },
  { immediate: true },
)

// Added once a saved server is this one and the project has it on.
const added = computed(() => {
  const project = state.detail?.project
  const server = servers.value?.find((item) => sameServer(props.block, item))
  return !!server && !!project?.mcp.includes(server.id)
})

function add() {
  const project = state.detail?.project
  if (!project) return
  suggestedServer.value = { block: props.block, projectId: project.id }
  openSettings('mcp')
}
</script>

<template>
  <DouChatArtifact
    :title="block.name"
    :description="block.why ?? undefined"
    status="done"
    @open="!added && add()"
  >
    <template #icon><McpServerIcon /></template>
    <template #actions>
      <DouText v-if="added" size="sm" variant="tertiary">{{ t('connector.added') }}</DouText>
      <DouButton v-else size="sm" variant="soft" tone="primary" @click.stop="add">
        {{ t('connector.add') }}
      </DouButton>
    </template>
  </DouChatArtifact>
</template>
