<script setup lang="ts">
import type { TraceRequestDetail } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouCodeBlock,
  DouDescriptions,
  DouDescriptionsItem,
  DouSpinner,
  DouTabPanel,
  DouTabs,
  DouText,
} from 'dou-ui'
import { CloseIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import { duration, locale, t, tm } from '../i18n'
import type { TraceRow } from './rows'
import TraceMessages from './TraceMessages.vue'

const props = defineProps<{ row: TraceRow; conversationId: string }>()
const emit = defineEmits<{ close: [] }>()

const tab = ref('overview')
const detail = ref<TraceRequestDetail | null>(null)
const problem = ref<string | null>(null)

// The whole request is fetched for a request and for a tool call of one.
onMounted(async () => {
  const request = props.row.request
  if (!request) return
  try {
    detail.value = (await api.getTraceRequest(props.conversationId, request.seq)).request
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  }
})

const json = (value: unknown) =>
  typeof value === 'string' ? value : JSON.stringify(value, null, 2)

/** A tool's arguments as indented JSON when they parse, else the text the model wrote. */
function pretty(text: string) {
  try {
    return JSON.stringify(JSON.parse(text || '{}'), null, 2)
  } catch {
    return text
  }
}

const clock = (at: number) =>
  new Date(at).toLocaleTimeString(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    fractionalSecondDigits: 3,
    hourCycle: 'h23',
  })
const tokens = (value: number | undefined | null) =>
  typeof value === 'number' ? value.toLocaleString(locale.value) : '—'

/** What the provider was sent: its body, or the request handed to its plugin. */
const body = computed(() => {
  const d = detail.value
  if (!d) return ''
  if (d.body !== null) return json(d.body)
  return json({
    model: d.call?.model,
    effort: d.effort,
    messages: d.messages,
    tools: d.tools,
  })
})

const response = computed(() => {
  const request = props.row.request
  if (!request) return ''
  return json({
    finish: request.finish,
    reasoning: request.reasoning,
    text: request.text,
    toolCalls: request.toolCalls.map(({ id, name, arguments: args }) => ({
      id,
      name,
      arguments: args,
    })),
    usage: request.usage,
  })
})

const schema = computed(() => {
  const name = props.row.tool?.name
  const spec = detail.value?.tools.find((tool) => tool.name === name)
  return spec ? json(spec) : ''
})

const title = computed(() =>
  props.row.kind === 'tool' && props.row.tool
    ? `${props.row.event} · ${props.row.tool.name}`
    : props.row.event,
)
</script>

<template>
  <div class="inspector">
    <div class="inspector-header">
      <DouText weight="medium" truncate class="inspector-title">{{ title }}</DouText>
      <DouButton variant="ghost" size="sm" :aria-label="t('thread.close')" @click="emit('close')">
        <CloseIcon />
      </DouButton>
    </div>
    <DouAlert v-if="problem" tone="danger" class="inspector-problem">{{ problem }}</DouAlert>

    <DouTabs v-model="tab" size="sm" class="inspector-tabs">
      <DouTabPanel value="overview" :label="t('trace.tab.overview')">
        <div class="inspector-pad">
          <DouDescriptions :columns="1" layout="horizontal" label-width="96px">
            <template v-if="row.kind === 'turn'">
              <DouDescriptionsItem :label="t('trace.field.model')">
                {{ row.turn.model }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.outcome')">
                {{ row.turn.outcome ? t(`trace.outcome.${row.turn.outcome}`) : t('trace.running') }}
              </DouDescriptionsItem>
              <DouDescriptionsItem v-if="row.turn.error" :label="t('trace.field.error')">
                {{ tm(row.turn.error) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.startedAt')">
                {{ clock(row.turn.startedAt) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem v-if="row.turn.finishedAt" :label="t('trace.field.finishedAt')">
                {{ clock(row.turn.finishedAt) }}
              </DouDescriptionsItem>
            </template>
            <template v-else-if="row.request && row.kind === 'request'">
              <DouDescriptionsItem :label="t('trace.field.model')">
                {{ [row.request.provider, row.request.model].filter(Boolean).join(' / ') }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.effort')">
                {{
                  row.request.effort
                    ? t(`settings.effort.${row.request.effort}`)
                    : t('settings.effort.default')
                }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.finish')">
                {{ t(`trace.finish.${row.request.finish}`) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.sentAt')">
                {{ clock(row.request.startedAt) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.firstOutput')">
                {{
                  row.request.firstOutputAt
                    ? `${clock(row.request.firstOutputAt)} · ${duration(row.request.firstOutputAt - row.request.startedAt)}`
                    : '—'
                }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.finishedAt')">
                {{ clock(row.request.finishedAt) }}
              </DouDescriptionsItem>
            </template>
            <template v-else-if="row.tool">
              <DouDescriptionsItem :label="t('trace.field.callId')">
                <span class="inspector-mono">{{ row.tool.id }}</span>
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.startedAt')">
                {{ clock(row.tool.startedAt) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.finishedAt')">
                {{ row.tool.result ? clock(row.tool.result.finishedAt) : '—' }}
              </DouDescriptionsItem>
            </template>
            <template v-else-if="row.retry">
              <DouDescriptionsItem :label="t('trace.field.attempt')">
                {{ row.retry.attempt }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.wait')">
                {{ duration(row.retry.delayMs) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.error')">
                <span class="inspector-mono">{{ row.retry.detail }}</span>
              </DouDescriptionsItem>
            </template>
            <template v-else>
              <DouDescriptionsItem :label="t('trace.field.startedAt')">
                {{ clock(row.start) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.col.content')">{{
                row.content
              }}</DouDescriptionsItem>
            </template>
            <DouDescriptionsItem v-if="row.end !== null" :label="t('trace.field.duration')">
              {{ duration(row.end - row.start) }}
            </DouDescriptionsItem>
            <template v-if="row.input !== null">
              <DouDescriptionsItem :label="t('trace.field.input')">
                {{ tokens(row.input) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.cached')">
                {{ tokens(row.cached) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.output')">
                {{ tokens(row.output) }}
              </DouDescriptionsItem>
              <DouDescriptionsItem :label="t('trace.field.reasoning')">
                {{ tokens(row.reasoning) }}
              </DouDescriptionsItem>
            </template>
          </DouDescriptions>
        </div>
      </DouTabPanel>

      <template v-if="row.kind === 'request'">
        <DouTabPanel value="body" :label="t('trace.tab.body')">
          <div class="inspector-pad">
            <DouSpinner v-if="!detail && !problem" size="sm" />
            <template v-else-if="detail">
              <DouText v-if="detail.body === null" variant="tertiary" size="sm">
                {{ t('trace.noBody') }}
              </DouText>
              <DouCodeBlock :code="body" lang="json" copyable wrap />
            </template>
          </div>
        </DouTabPanel>
        <DouTabPanel value="messages" :label="t('trace.tab.messages')">
          <div class="inspector-pad">
            <DouSpinner v-if="!detail && !problem" size="sm" />
            <TraceMessages v-else-if="detail" :detail="detail" />
          </div>
        </DouTabPanel>
        <DouTabPanel value="response" :label="t('trace.tab.response')">
          <div class="inspector-pad">
            <DouCodeBlock :code="response" lang="json" copyable wrap />
          </div>
        </DouTabPanel>
      </template>

      <template v-else-if="row.tool">
        <DouTabPanel value="arguments" :label="t('trace.tab.arguments')">
          <div class="inspector-pad">
            <DouCodeBlock :code="pretty(row.tool.arguments)" lang="json" copyable wrap />
          </div>
        </DouTabPanel>
        <DouTabPanel value="result" :label="t('trace.tab.result')">
          <div class="inspector-pad">
            <DouCodeBlock
              v-if="row.tool.result"
              :code="row.tool.result.content"
              :lang="row.tool.result.isError ? 'error' : 'text'"
              copyable
              wrap
            />
            <DouText v-else variant="tertiary" size="sm">{{ t('trace.noResult') }}</DouText>
          </div>
        </DouTabPanel>
        <DouTabPanel value="schema" :label="t('trace.tab.schema')">
          <div class="inspector-pad">
            <DouSpinner v-if="!detail && !problem" size="sm" />
            <DouCodeBlock v-else-if="schema" :code="schema" lang="json" copyable wrap />
          </div>
        </DouTabPanel>
      </template>
    </DouTabs>
  </div>
</template>

<style scoped>
.inspector {
  display: flex;
  flex-direction: column;
  min-height: 0;
  min-width: 0;
}

.inspector-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px 4px 16px;
}

.inspector-title {
  flex: 1;
  min-width: 0;
}

.inspector-problem {
  margin: 0 16px 8px;
}

.inspector-tabs {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

.inspector-pad {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px 16px;
}

.inspector-mono {
  font-family: var(--dou-font-family-mono);
  overflow-wrap: anywhere;
}
</style>
