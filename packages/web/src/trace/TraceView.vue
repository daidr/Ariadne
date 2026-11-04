<script setup lang="ts">
import type { TraceTurn } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouChatToolCall,
  DouCollapse,
  DouCollapseItem,
  DouEmpty,
  DouSpinner,
  DouText,
} from 'dou-ui'
import { ChevronLeftIcon, RotateCwIcon } from '@dou-ui/icons'
import { computed, onUnmounted, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import { count, duration, locale, t, tm } from '../i18n'
import { onLiveEvent, openMain, state } from '../store'
import TraceInput from './TraceInput.vue'

const props = defineProps<{ conversationId: string }>()

const name = computed(() => {
  if (state.detail?.project.coordinatorId === props.conversationId) return t('coordinator.title')
  return state.detail?.threads.find((thread) => thread.id === props.conversationId)?.title ?? ''
})

const turns = ref<TraceTurn[]>([])
const more = ref(false)
const loading = ref(true)
const problem = ref<string | null>(null)
// Open turns and steps, by key, so a reload keeps what the user expanded.
const openTurns = ref<string[]>([])
// Steps whose full input is shown.
const inputs = ref(new Set<number>())

/** The newest turns again, keeping older ones already paged in. */
async function load() {
  try {
    const page = await api.getTrace(props.conversationId)
    const oldest = page.turns[0]?.seq ?? Number.POSITIVE_INFINITY
    const older = turns.value.filter((turn) => turn.seq < oldest)
    if (!turns.value.length) {
      more.value = page.more
      // The newest turn is what the user came to look at.
      const last = page.turns.at(-1)
      if (last) openTurns.value = [last.turnId]
    }
    turns.value = [...older, ...page.turns]
    problem.value = null
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  } finally {
    loading.value = false
  }
}

async function loadEarlier() {
  const first = turns.value[0]
  if (!first) return
  try {
    const page = await api.getTrace(props.conversationId, first.seq)
    turns.value = [...page.turns, ...turns.value]
    more.value = page.more
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  }
}

watch(
  () => props.conversationId,
  () => {
    turns.value = []
    inputs.value = new Set()
    loading.value = true
    void load()
  },
  { immediate: true },
)
// The running turn of this conversation moved on.
onUnmounted(
  onLiveEvent((event) => {
    const data = event.data as { conversationId?: unknown }
    if (data.conversationId === props.conversationId) void load()
  }),
)

const clock = (at: number) =>
  new Date(at).toLocaleTimeString(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  })
const day = (at: number) =>
  new Date(at).toLocaleDateString(locale.value, { month: '2-digit', day: '2-digit' })

function turnTitle(turn: TraceTurn) {
  const parts = [`${day(turn.startedAt)} ${clock(turn.startedAt)}`]
  if (turn.model) parts.push(turn.model)
  parts.push(turn.outcome ? t(`trace.outcome.${turn.outcome}`) : t('trace.running'))
  if (turn.finishedAt) parts.push(duration(turn.finishedAt - turn.startedAt))
  if (turn.usage.input || turn.usage.output) parts.push(usageText(turn.usage))
  return parts.join(' · ')
}

const usageText = (usage: { input: number; output: number }) =>
  t('trace.usage', { input: count(usage.input), output: count(usage.output) })

/** A tool's arguments as an object when they parse, else the text the model wrote. */
function argsOf(text: string): unknown {
  try {
    return JSON.parse(text || '{}')
  } catch {
    return text
  }
}

function toggleInput(seq: number) {
  const next = new Set(inputs.value)
  if (next.has(seq)) next.delete(seq)
  else next.add(seq)
  inputs.value = next
}

/** Steps are numbered within their turn. */
function stepNumber(turn: TraceTurn, index: number) {
  return turn.items.slice(0, index + 1).filter((item) => item.kind === 'step').length
}
</script>

<template>
  <div class="trace">
    <div class="trace-header">
      <DouButton variant="ghost" size="sm" @click="openMain({ kind: 'conversation' })">
        <template #leading><ChevronLeftIcon /></template>
        {{ t('changes.back') }}
      </DouButton>
      <DouText weight="medium" truncate class="trace-title">
        {{ t('trace.title', { name }) }}
      </DouText>
      <DouButton
        variant="ghost"
        size="sm"
        :aria-label="t('changes.refresh')"
        :title="t('changes.refresh')"
        @click="load"
      >
        <RotateCwIcon />
      </DouButton>
    </div>

    <div class="trace-body">
      <DouAlert v-if="problem" tone="danger">{{ problem }}</DouAlert>
      <div v-if="loading" class="trace-empty"><DouSpinner /></div>
      <div v-else-if="!turns.length" class="trace-empty">
        <DouEmpty size="sm" :description="t('trace.empty')" />
      </div>
      <template v-else>
        <DouButton v-if="more" variant="ghost" size="sm" class="trace-more" @click="loadEarlier">
          {{ t('trace.earlier') }}
        </DouButton>
        <DouCollapse v-model="openTurns">
          <DouCollapseItem
            v-for="turn in turns"
            :key="turn.turnId"
            :value="turn.turnId"
            :title="turnTitle(turn)"
          >
            <div class="trace-items">
              <template v-for="(item, index) in turn.items" :key="index">
                <div v-if="item.kind === 'step'" class="trace-step">
                  <div class="trace-step-head">
                    <DouText size="sm" weight="medium">
                      {{ t('trace.step', { n: stepNumber(turn, index) }) }}
                    </DouText>
                    <DouText variant="tertiary" size="sm">
                      {{ clock(item.at) }} · {{ duration(item.durationMs) }}
                      <template v-if="item.usage"> · {{ usageText(item.usage) }}</template>
                    </DouText>
                    <DouButton
                      size="sm"
                      variant="ghost"
                      class="trace-input-toggle"
                      @click="toggleInput(item.seq)"
                    >
                      {{ t(inputs.has(item.seq) ? 'trace.hideInput' : 'trace.showInput') }}
                    </DouButton>
                  </div>
                  <TraceInput
                    v-if="inputs.has(item.seq)"
                    :conversation-id="conversationId"
                    :seq="item.seq"
                  />
                  <DouText v-if="item.text" size="sm" class="trace-text">{{ item.text }}</DouText>
                  <DouChatToolCall
                    v-for="call in item.toolCalls"
                    :key="call.id"
                    :name="call.name"
                    :title="
                      call.result ? `${call.name} · ${duration(call.result.durationMs)}` : call.name
                    "
                    :status="!call.result ? 'running' : call.result.isError ? 'error' : 'success'"
                    :input="argsOf(call.arguments)"
                    :output="call.result?.content"
                  />
                </div>
                <div v-else-if="item.kind === 'compacted'" class="trace-step">
                  <div class="trace-step-head">
                    <DouText size="sm" weight="medium">{{ t('trace.compacted') }}</DouText>
                    <DouText variant="tertiary" size="sm">
                      {{ clock(item.at) }} · {{ duration(item.durationMs) }}
                      <template v-if="item.usage"> · {{ usageText(item.usage) }}</template>
                    </DouText>
                    <DouButton
                      size="sm"
                      variant="ghost"
                      class="trace-input-toggle"
                      @click="toggleInput(item.seq)"
                    >
                      {{ t(inputs.has(item.seq) ? 'trace.hideInput' : 'trace.showInput') }}
                    </DouButton>
                  </div>
                  <TraceInput
                    v-if="inputs.has(item.seq)"
                    :conversation-id="conversationId"
                    :seq="item.seq"
                  />
                  <DouText size="sm" class="trace-text">{{ item.summary }}</DouText>
                </div>
                <DouText
                  v-else-if="item.kind === 'retry'"
                  tone="warning"
                  size="sm"
                  class="trace-line"
                >
                  {{ clock(item.at) }} ·
                  {{
                    t('notice.modelRetry', {
                      attempt: item.attempt,
                      seconds: Math.max(1, Math.round(item.delayMs / 1000)),
                      detail: item.detail,
                    })
                  }}
                </DouText>
                <DouText
                  v-else-if="item.kind === 'compaction-failed'"
                  tone="warning"
                  size="sm"
                  class="trace-line"
                >
                  {{ clock(item.at) }} · {{ t('notice.compactionFailed', { detail: item.detail }) }}
                </DouText>
                <DouText v-else variant="tertiary" size="sm" class="trace-line">
                  {{ clock(item.at) }} ·
                  {{
                    t(
                      item.removed
                        ? 'notice.instructionsRemoved'
                        : item.changed
                          ? 'notice.instructionsChanged'
                          : 'notice.instructions',
                      { path: item.path },
                    )
                  }}
                </DouText>
              </template>
              <DouText v-if="turn.error" tone="danger" size="sm" class="trace-line">
                {{ tm(turn.error) }}
              </DouText>
            </div>
          </DouCollapseItem>
        </DouCollapse>
      </template>
    </div>
  </div>
</template>

<style scoped>
.trace {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.trace-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--dou-color-border);
}

.trace-title {
  flex: 1;
  min-width: 0;
}

.trace-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 8px 16px 16px;
}

.trace-empty {
  display: grid;
  place-items: center;
  padding: 48px 0;
}

.trace-more {
  margin-bottom: 4px;
}

.trace-items {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.trace-step {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.trace-step-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.trace-input-toggle {
  margin-left: auto;
}

.trace-text,
.trace-line {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
