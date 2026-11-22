<script setup lang="ts">
import type { PlanNodeView } from '@ariadne/core'
import { DouButton, DouLink, DouTag, DouText, DouTextarea } from 'dou-ui'
import { AlertTriangleIcon, FileDiffIcon, PlayIcon, SendIcon } from '@dou-ui/icons'
import { computed, ref } from 'vue'
import { t } from '../i18n'
import { openMain, openThreadFrom, sendMessage, state } from '../store'
import { kindIcon } from '../artifacts/kinds'
import { groupNameOf, startedAhead } from './layout'
import PullRequestTag from '../components/PullRequestTag.vue'
import IssueLink from '../markdown/IssueLink.vue'
import ChangeTotals from '../components/ChangeTotals.vue'
import { branchChanges } from '../review/merge-offered'
import StageIcon from '../components/StageIcon.vue'
import ThreadHeadline from '../components/ThreadHeadline.vue'
import ReportArtifacts from '../components/ReportArtifacts.vue'
import { showsHeadline } from '../stage'
import PlanSection from './PlanSection.vue'
import PlanStatusIcon from './PlanStatusIcon.vue'
import { useOpenView } from '../opened-from'

const openView = useOpenView()

const props = defineProps<{ node: PlanNodeView }>()

const groupName = computed(() => groupNameOf(props.node))

/** The artifacts a thread waiting on the user pointed to in its last report. */
const reportArtifacts = (threadId: string) => {
  const thread = state.detail?.threads.find((item) => item.id === threadId)
  return thread && showsHeadline(thread.stage) ? thread.lastArtifacts : []
}

/** Whether the thread has a branch whose changes can be shown. */
const changesOf = (threadId: string) =>
  branchChanges(state.detail?.threads.find((thread) => thread.id === threadId))
const hasBranch = (threadId: string) => {
  const workspace = state.detail?.threads.find((thread) => thread.id === threadId)?.workspace
  return !!workspace?.branch && !!workspace.base
}

/** Dependencies not done while work on this node started anyway. */
const ahead = computed(() =>
  state.plan ? (startedAhead(state.plan.nodes).get(props.node.id) ?? []) : [],
)
/** Artifacts made for this node. */
const artifacts = computed(() =>
  state.artifacts.filter((artifact) => artifact.nodeId === props.node.id),
)

// Starting work and making a decision go through the coordinator, in the user's words in
// its conversation: it briefs the thread, or changes the plan by what was decided.
const sending = ref(false)
const decision = ref('')
const hasLiveThread = computed(() =>
  props.node.threads.some((thread) => thread.stage !== 'done' && thread.stoppedBy !== 'user'),
)
const canStart = computed(
  () => props.node.kind === 'task' && props.node.status !== 'done' && !hasLiveThread.value,
)
async function ask(content: string) {
  const coordinator = state.detail?.project.coordinatorId
  if (!coordinator) return false
  sending.value = true
  const sent = await sendMessage(coordinator, content)
  sending.value = false
  if (sent && state.main.kind !== 'conversation') openMain({ kind: 'conversation' })
  return sent
}
const start = () => ask(t('plan.startAsk', { title: props.node.title }))
async function decide() {
  const text = decision.value.trim()
  if (!text) return
  if (await ask(t('plan.decideSay', { title: props.node.title, text }))) decision.value = ''
}

/** Who linked an issue: the user, the coordinator, or a thread by its title. */
function linkedBy(by: string): string {
  if (by === 'user') return t('plan.linkedByUser')
  if (by === state.detail?.project.coordinatorId) return t('plan.linkedByCoordinator')
  return state.detail?.threads.find((thread) => thread.id === by)?.title ?? by
}
</script>

<template>
  <section class="node-detail" :aria-label="node.title">
    <header class="node-detail-head">
      <div class="node-detail-title">
        <span class="node-detail-lead"><PlanStatusIcon :status="node.status" /></span>
        <DouText as="h2" size="lg" weight="medium" class="node-detail-title-text">
          {{ node.title }}
        </DouText>
        <DouText size="sm" variant="tertiary" class="node-detail-kind">
          {{ t(`plan.kind.${node.kind}`) }}
        </DouText>
      </div>
      <div v-if="groupName" class="node-detail-group">
        <DouTag size="sm">{{ groupName }}</DouTag>
      </div>
      <DouText v-if="node.description" size="sm" class="node-detail-text">
        {{ node.description }}
      </DouText>
    </header>
    <DouText v-if="ahead.length" tone="warning" size="sm" class="node-detail-ahead">
      <AlertTriangleIcon aria-hidden="true" />
      {{ t('plan.ahead', { titles: ahead.join('、') }) }}
    </DouText>
    <PlanSection v-if="node.acceptance" :title="t('plan.acceptance')">
      <DouText size="sm" class="node-detail-text">{{ node.acceptance }}</DouText>
    </PlanSection>
    <!-- A decision is the user's to make: said here, it reaches the coordinator. -->
    <form
      v-if="node.kind === 'decision' && node.status !== 'done'"
      class="node-detail-decide"
      @submit.prevent="decide"
    >
      <DouTextarea
        v-model="decision"
        size="sm"
        :autosize="{ minRows: 2, maxRows: 6 }"
        :placeholder="t('plan.decidePlaceholder')"
        :aria-label="t('plan.decide')"
      />
      <DouButton
        size="sm"
        tone="primary"
        native-type="submit"
        :disabled="!decision.trim()"
        :loading="sending"
      >
        <template #leading><SendIcon /></template>
        {{ t('plan.decide') }}
      </DouButton>
    </form>
    <DouButton
      v-else-if="canStart"
      size="sm"
      variant="outline"
      class="node-detail-start"
      :loading="sending"
      @click="start"
    >
      <template #leading><PlayIcon /></template>
      {{ t('plan.start') }}
    </DouButton>
    <!-- With a start button above, "no thread yet" says the same thing twice. -->
    <PlanSection
      v-if="node.threads.length || !canStart"
      :title="t('plan.threads')"
      :count="node.threads.length"
    >
      <ul v-if="node.threads.length" class="node-detail-list">
        <li v-for="thread in node.threads" :key="thread.id" class="node-thread">
          <button
            type="button"
            class="node-thread-open"
            @click="(event) => openThreadFrom(event, thread.id)"
          >
            <span class="node-thread-lead">
              <StageIcon :stage="thread.stage" :pull-request-state="thread.pullRequest?.state" />
            </span>
            <span class="node-thread-text">
              <DouText size="sm" :truncate="2" :title="thread.title">{{ thread.title }}</DouText>
              <ThreadHeadline :stage="thread.stage" :headline="thread.lastHeadline" />
            </span>
          </button>
          <span v-if="thread.pullRequest || hasBranch(thread.id)" class="node-thread-tools">
            <PullRequestTag :thread="thread" />
            <ChangeTotals
              v-if="hasBranch(thread.id) && changesOf(thread.id)"
              :totals="changesOf(thread.id)!"
              :thread-id="thread.id"
              button
              size="xs"
            />
            <DouButton
              v-else-if="hasBranch(thread.id)"
              size="xs"
              variant="ghost"
              :aria-label="t('changes.open')"
              :title="t('changes.open')"
              @click="openView({ kind: 'changes', threadId: thread.id })"
            >
              <FileDiffIcon />
            </DouButton>
          </span>
          <ReportArtifacts class="node-thread-extra" :artifacts="reportArtifacts(thread.id)" />
        </li>
      </ul>
      <DouText v-else variant="tertiary" size="sm">{{ t('plan.noThread') }}</DouText>
    </PlanSection>
    <PlanSection v-if="artifacts.length" :title="t('plan.artifacts')" :count="artifacts.length">
      <ul class="node-detail-list">
        <li v-for="artifact in artifacts" :key="artifact.id">
          <DouLink
            underline="hover"
            :title="artifact.title"
            class="node-artifact"
            @click="openView({ kind: 'artifact', id: artifact.id })"
          >
            <template #leading><component :is="kindIcon[artifact.kind]" /></template>
            {{ artifact.title }}
          </DouLink>
        </li>
      </ul>
    </PlanSection>
    <PlanSection v-if="node.issues.length" :title="t('plan.issues')" :count="node.issues.length">
      <ul class="node-detail-list">
        <li v-for="issue in node.issues" :key="issue.number" class="node-issue">
          <IssueLink :href="issue.url" :label="`#${issue.number} ${issue.title}`" variant="text" />
          <DouText variant="tertiary" size="sm" class="node-issue-by">
            {{ t('plan.issueLinkedBy', { who: linkedBy(issue.linkedBy) }) }}
          </DouText>
        </li>
      </ul>
    </PlanSection>
  </section>
</template>

<style scoped>
.node-detail-ahead {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}

.node-detail-ahead svg {
  flex: none;
  margin-top: 0.2em;
}

.node-detail-decide {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 8px;
}

.node-detail-start {
  align-self: flex-start;
}

.node-artifact {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-detail {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.node-detail-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.node-detail-group {
  display: flex;
  align-items: center;
  margin-top: 2px;
}

.node-detail-title {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}

.node-detail-title-text {
  flex: 1;
  align-self: first baseline;
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}

.node-detail-kind {
  flex: none;
  align-self: first baseline;
}

.node-detail-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.node-detail-head .node-detail-text {
  margin-top: 4px;
}

.node-detail-list {
  display: flex;
  flex-direction: column;
  margin: 0;
  padding: 0;
  list-style: none;
}

/* A thread: its stage, title and what it asks of the user open it; the tools sit at the end.
   The row reaches past the column by the button's padding, so its icon lines up with the
   heading. */
.node-thread {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0 4px;
  margin-inline-start: -6px;
}

.node-thread-open {
  display: flex;
  flex: 1;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
  margin: 0;
  padding: 4px 6px;
  border: 0;
  border-radius: var(--dou-radius-control);
  background: none;
  transition: background-color var(--dou-motion-duration-fast) var(--dou-motion-easing);
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.node-thread-open:hover {
  background-color: color-mix(in oklab, var(--dou-color-neutral) 6%, transparent);
}

.node-thread-open:focus-visible {
  outline: 2px solid var(--dou-color-focus);
  outline-offset: 2px;
}

.node-thread-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.node-thread-tools {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 4px;
  font-size: var(--dou-font-size-sm);
}

/* Under the title, past the stage icon. */
.node-thread-extra {
  flex-basis: 100%;
  padding-inline-start: 30px;
}

.node-issue {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0 8px;
  padding-block: 2px;
}

/*
 * A status icon in front of a title that may wrap sits on the title's first line, centred
 * on its letters: its box is one cap tall in the title's font and shares the title's first
 * baseline, so it covers the capitals, and the icon centres on that. A fixed offset from the
 * line box's top cannot do this, since the line box is the font's ascent and descent, and
 * Windows' Segoe UI and Microsoft YaHei leave much more room above the letters than other
 * fonts: the icon sat 2 to 3 pixels high there. Layout containment keeps the icon's own
 * box from lending the lead a baseline, so the lead's bottom edge is its baseline.
 */
.node-detail-lead,
.node-thread-lead {
  display: flex;
  flex: none;
  align-self: first baseline;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 1cap;
  contain: layout;
}

.node-detail-lead {
  font-size: var(--dou-font-size-lg);
}

.node-thread-lead {
  font-size: var(--dou-font-size-sm);
}

.node-thread-text {
  align-self: first baseline;
}
</style>
