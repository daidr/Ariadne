<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouCard,
  DouEmpty,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputTag,
  DouSegmentedControl,
  DouSelect,
  DouShimmer,
  DouText,
} from 'dou-ui'
import { CloseIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import {
  api,
  ApiError,
  type McpServerInput,
  type McpServerStatus,
  type McpServerView,
  type McpSettings,
  type McpToolMark,
} from '../api'
import { t, tm } from '../i18n'
import DirectoryInput from './DirectoryInput.vue'

const browseLocal = (typed: string) => api.browseLocal(typed).then((result) => result.listing)

interface DraftPair {
  name: string
  /** Empty keeps the stored value, when `stored`. */
  value: string
  stored: boolean
}

interface DraftServer {
  /** Stable for the list while the id is being typed. */
  key: number
  id: string
  name: string
  transport: 'stdio' | 'http'
  command: string
  args: string[]
  cwd: string
  url: string
  env: DraftPair[]
  headers: DraftPair[]
  tools: Record<string, McpToolMark>
}

const servers = ref<DraftServer[]>([])
/** The servers as last saved, to tell a draft with unsaved changes. */
const saved = ref<string>('[]')
const status = ref<Record<string, McpServerStatus>>({})
const checking = ref<Set<string>>(new Set())
const error = ref<string | null>(null)
let nextKey = 1
// Bound rather than written in the template: a static "//" there trips the type check.
const URL_HINT = 'https://'

const transportOptions = computed(() => [
  { value: 'stdio', label: t('mcp.transport.stdio') },
  { value: 'http', label: t('mcp.transport.http') },
])
const markOptions = computed(() =>
  (['read', 'ask', 'off'] as const).map((mark) => ({
    value: mark,
    label: t(`mcp.mark.${mark}`),
  })),
)

const pairsOf = (names: string[]): DraftPair[] =>
  names.map((name) => ({ name, value: '', stored: true }))

function draftOf(server: McpServerView): DraftServer {
  return {
    key: nextKey++,
    id: server.id,
    name: server.name,
    transport: server.transport,
    command: server.command ?? '',
    args: [...(server.args ?? [])],
    cwd: server.cwd ?? '',
    url: server.url ?? '',
    env: pairsOf(server.env),
    headers: pairsOf(server.headers),
    tools: { ...server.tools },
  }
}

/** Pairs with a name; an empty value keeps the stored one. */
const pairs = (list: DraftPair[]) =>
  list
    .filter((pair) => pair.name.trim())
    .map((pair) => ({ name: pair.name.trim(), value: pair.value }))

function inputOf(server: DraftServer): McpServerInput {
  const base = {
    id: server.id.trim(),
    name: server.name.trim(),
    tools: server.tools,
  }
  return server.transport === 'stdio'
    ? {
        ...base,
        transport: 'stdio',
        command: server.command.trim(),
        args: server.args,
        ...(server.cwd.trim() ? { cwd: server.cwd.trim() } : {}),
        env: pairs(server.env),
        headers: [],
      }
    : {
        ...base,
        transport: 'http',
        url: server.url.trim(),
        env: [],
        headers: pairs(server.headers),
      }
}

const snapshot = () => JSON.stringify(servers.value.map(inputOf))
const dirty = computed(() => snapshot() !== saved.value)

function load(settings: McpSettings) {
  servers.value = settings.servers.map(draftOf)
  saved.value = snapshot()
  status.value = Object.fromEntries(settings.status.map((s) => [s.id, s]))
}

const message = (cause: unknown) =>
  cause instanceof ApiError ? tm(cause.localized) : String(cause)

onMounted(async () => {
  try {
    load(await api.getMcpSettings())
  } catch (cause) {
    error.value = message(cause)
  }
})

function add() {
  const taken = new Set(servers.value.map((s) => s.id))
  let n = servers.value.length + 1
  while (taken.has(`server${n}`)) n++
  servers.value.push({
    key: nextKey++,
    id: `server${n}`,
    name: '',
    transport: 'stdio',
    command: '',
    args: [],
    cwd: '',
    url: '',
    env: [],
    headers: [],
    tools: {},
  })
}

async function check(id: string) {
  checking.value = new Set([...checking.value, id])
  try {
    status.value = {
      ...status.value,
      [id]: (await api.checkMcpServer(id)).status,
    }
  } catch (cause) {
    error.value = message(cause)
  } finally {
    checking.value = new Set([...checking.value].filter((other) => other !== id))
  }
}

/** Save the servers, then connect those not connected yet. `false` when it was refused. */
async function save(): Promise<boolean> {
  error.value = null
  try {
    load(await api.saveMcpSettings(servers.value.map(inputOf)))
  } catch (cause) {
    error.value = message(cause)
    return false
  }
  for (const server of servers.value) {
    if (status.value[server.id]?.state !== 'ready') void check(server.id)
  }
  return true
}

/** Connect now; unsaved changes are saved first, which connects. */
async function connect(server: DraftServer) {
  if (dirty.value) await save()
  else await check(server.id)
}

defineExpose({ save })

const savedIds = computed(
  () => new Set((JSON.parse(saved.value) as McpServerInput[]).map((s) => s.id)),
)
/** The status of what is saved under this id; a server not saved yet has none. */
const statusOf = (server: DraftServer) =>
  savedIds.value.has(server.id) ? status.value[server.id] : undefined
const stateOf = (server: DraftServer) =>
  checking.value.has(server.id) ? 'connecting' : (statusOf(server)?.state ?? 'idle')

const isStdio = (server: DraftServer) => server.transport === 'stdio'

/** A local command takes environment variables, an HTTP server headers. */
const pairList = (server: DraftServer) => (isStdio(server) ? server.env : server.headers)

function addPair(server: DraftServer) {
  pairList(server).push({ name: '', value: '', stored: false })
}

function setMark(server: DraftServer, tool: string, mark: unknown) {
  server.tools[tool] = mark as McpToolMark
}
</script>

<template>
  <div class="mcp">
    <div class="mcp-head">
      <DouButton size="sm" variant="ghost" @click="add">
        <template #leading><PlusIcon /></template>
        {{ t('mcp.add') }}
      </DouButton>
    </div>
    <DouEmpty v-if="!servers.length" size="sm" :description="t('mcp.empty')" />
    <DouCard v-for="(server, index) in servers" :key="server.key" padding="sm">
      <DouForm layout="vertical" size="sm" class="mcp-form">
        <div class="mcp-row">
          <DouFormItem :label="t('mcp.name')" class="mcp-grow">
            <DouInput v-model="server.name" :placeholder="server.id" />
          </DouFormItem>
          <DouFormItem :label="t('mcp.id')" class="mcp-narrow">
            <DouInput v-model="server.id" :maxlength="20" />
          </DouFormItem>
          <DouFormItem :label="t('mcp.transport')" class="mcp-narrow">
            <DouSelect v-model="server.transport" :options="transportOptions" />
          </DouFormItem>
          <DouButton
            size="sm"
            variant="ghost"
            tone="danger"
            class="mcp-remove"
            :aria-label="t('settings.remove')"
            :title="t('settings.remove')"
            @click="servers.splice(index, 1)"
          >
            <TrashIcon />
          </DouButton>
        </div>
        <template v-if="isStdio(server)">
          <div class="mcp-row">
            <DouFormItem :label="t('mcp.command')" class="mcp-grow">
              <DouInput v-model="server.command" placeholder="npx" />
            </DouFormItem>
            <DouFormItem :label="t('mcp.cwd')" class="mcp-grow">
              <DirectoryInput
                v-model="server.cwd"
                :browse="browseLocal"
                :placeholder="t('mcp.cwdPlaceholder')"
              />
            </DouFormItem>
          </div>
          <DouFormItem :label="t('mcp.args')">
            <DouInputTag
              v-model="server.args"
              allow-duplicates
              add-on-blur
              :separators="['Enter']"
              :placeholder="t('mcp.argsPlaceholder')"
            />
          </DouFormItem>
        </template>
        <DouFormItem v-if="!isStdio(server)" :label="t('mcp.url')">
          <DouInput v-model="server.url" :placeholder="URL_HINT" />
        </DouFormItem>
        <DouFormItem :label="t(isStdio(server) ? 'mcp.env' : 'mcp.headers')">
          <div class="mcp-pairs">
            <div v-for="(pair, at) in pairList(server)" :key="at" class="mcp-row">
              <DouInput
                v-model="pair.name"
                class="mcp-pair-name"
                :readonly="pair.stored"
                :aria-label="t('mcp.pairName')"
                :placeholder="t('mcp.pairName')"
              />
              <DouInput
                v-model="pair.value"
                type="password"
                show-password
                class="mcp-grow"
                :aria-label="t('mcp.pairValue')"
                :placeholder="pair.stored ? t('settings.secretSet') : t('mcp.pairValue')"
              />
              <DouButton
                size="sm"
                variant="ghost"
                :aria-label="t('settings.remove')"
                :title="t('settings.remove')"
                @click="pairList(server).splice(at, 1)"
              >
                <CloseIcon />
              </DouButton>
            </div>
            <DouButton size="sm" variant="ghost" class="mcp-pair-add" @click="addPair(server)">
              <template #leading><PlusIcon /></template>
              {{ t(isStdio(server) ? 'mcp.addEnv' : 'mcp.addHeader') }}
            </DouButton>
          </div>
        </DouFormItem>
      </DouForm>
      <div class="mcp-status">
        <DouText
          size="sm"
          :tone="stateOf(server) === 'failed' ? 'danger' : 'neutral'"
          :variant="stateOf(server) === 'failed' ? 'default' : 'tertiary'"
          class="mcp-status-text"
        >
          <DouShimmer v-if="stateOf(server) === 'connecting'">
            {{ t('mcp.state.connecting') }}
          </DouShimmer>
          <template v-else-if="stateOf(server) === 'ready'">
            {{
              t('mcp.state.ready', {
                count: statusOf(server)?.tools?.length ?? 0,
              })
            }}
          </template>
          <template v-else-if="stateOf(server) === 'failed'">
            {{ t('mcp.state.failed', { error: statusOf(server)?.error ?? '' }) }}
          </template>
          <template v-else>{{ t('mcp.state.idle') }}</template>
        </DouText>
        <DouButton size="sm" :disabled="stateOf(server) === 'connecting'" @click="connect(server)">
          {{ t('mcp.connect') }}
        </DouButton>
      </div>
      <DouText
        v-if="stateOf(server) === 'failed' && statusOf(server)?.log"
        size="sm"
        variant="tertiary"
        class="mcp-log"
      >
        {{ statusOf(server)?.log }}
      </DouText>
      <ul v-if="statusOf(server)?.tools?.length" class="mcp-tools">
        <li v-for="tool in statusOf(server)?.tools ?? []" :key="tool.name" class="mcp-tool">
          <div class="mcp-tool-text">
            <DouText size="sm" truncate :title="tool.title">
              <span class="mcp-tool-name">{{ tool.name }}</span>
              <DouText v-if="tool.title" size="sm" variant="tertiary" class="mcp-tool-title">{{
                tool.title
              }}</DouText>
            </DouText>
            <DouText
              v-if="tool.description"
              size="sm"
              variant="tertiary"
              truncate
              :title="tool.description"
            >
              {{ tool.description }}
            </DouText>
          </div>
          <DouSegmentedControl
            size="sm"
            :options="markOptions"
            :model-value="server.tools[tool.name] ?? 'ask'"
            @update:model-value="(mark) => setMark(server, tool.name, mark)"
          />
        </li>
      </ul>
    </DouCard>
    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
  </div>
</template>

<style scoped>
.mcp {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.mcp-head {
  display: flex;
  justify-content: flex-end;
}

.mcp-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.mcp-row {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}

.mcp-grow {
  flex: 1;
  min-width: 0;
}

.mcp-narrow {
  flex: none;
  width: 140px;
}

.mcp-remove {
  flex: none;
}

.mcp-pairs {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}

.mcp-pair-name {
  flex: none;
  width: 180px;
}

.mcp-pair-add {
  align-self: flex-start;
}

.mcp-status {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 8px;
}

.mcp-status-text {
  min-width: 0;
  overflow-wrap: anywhere;
}

.mcp-log {
  display: block;
  max-height: 120px;
  margin-top: 4px;
  overflow-y: auto;
  font-family: var(--dou-font-family-mono);
  white-space: pre-wrap;
}

.mcp-tools {
  display: flex;
  flex-direction: column;
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}

.mcp-tool {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 0;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.mcp-tool-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.mcp-tool-name {
  font-family: var(--dou-font-family-mono);
}

.mcp-tool-title {
  margin-left: 8px;
}
</style>
