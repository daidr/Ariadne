<script setup lang="ts">
import type { PlanNodeView, PlanOp } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputNumber,
  DouLink,
  DouPopconfirm,
  DouText,
  DouTextarea,
} from 'dou-ui'
import {
  AlertTriangleIcon,
  CircleDotIcon,
  FileDiffIcon,
  LinkPlusIcon,
  PlayIcon,
  SendIcon,
  TrashIcon,
  UnlinkIcon,
} from '@dou-ui/icons'
import { computed, reactive, ref, watch } from 'vue'
import { t } from '../i18n'
import { editPlan, openMain, openThreadFrom, sendMessage, setNodeIssue, state } from '../store'
import { kindIcon } from '../artifacts/kinds'
import ReviewActions from '../components/ReviewActions.vue'
import { startedAhead } from './layout'
import { liveThreads, mergeIntoForm, reasonOf } from './edit'
import PullRequestLink from './PlanPullRequest.vue'
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

const props = defineProps<{ node: PlanNodeView; editable?: boolean }>()

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
const emit = defineEmits<{ removed: [] }>()

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
const canStart = computed(
  () => props.node.kind === 'task' && props.node.status !== 'done' && !liveThreads(props.node),
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

type Fields = { title: string; description: string; acceptance: string }
const FIELDS = ['title', 'description', 'acceptance'] as const
const fieldsOf = (node: PlanNodeView): Fields => ({
  title: node.title,
  description: node.description,
  acceptance: node.acceptance,
})

// The form starts from the node. `base` is what the user started editing from: the plan
// refreshes whenever any thread moves, and that must not wipe what they are typing.
const form = reactive<Fields>({ title: '', description: '', acceptance: '' })
const base = ref<Fields>({ title: '', description: '', acceptance: '' })
const differs = (a: Fields, b: Fields) =>
  a.title.trim() !== b.title || a.description !== b.description || a.acceptance !== b.acceptance
watch(
  () => [props.node.id, props.node.title, props.node.description, props.node.acceptance],
  ([id], previous) => {
    if (previous && id === previous[0]) {
      // Only fields the user changed and someone else changed too are left to them.
      base.value = mergeIntoForm(form, base.value, fieldsOf(props.node))
      return
    }
    base.value = fieldsOf(props.node)
    Object.assign(form, base.value)
  },
  { immediate: true },
)
const dirty = computed(() => differs(form, fieldsOf(props.node)))
// Fields someone else changed while the user was editing, with their value now. Shown next
// to the form, so the user decides; nothing they typed is thrown away.
const changedMeanwhile = computed(() =>
  FIELDS.filter((field) => props.node[field] !== base.value[field]).map((field) => ({
    field,
    value: props.node[field],
  })),
)
const saving = ref(false)
const labels = {
  title: 'plan.field.title',
  description: 'plan.field.description',
  acceptance: 'plan.acceptance',
} as const

async function save() {
  const { node } = props
  // `expect` holds what the user started from, so a save refuses to overwrite what the
  // coordinator changed in the meantime.
  const expect: Partial<Fields> = {}
  const op: Extract<PlanOp, { op: 'update' }> = { op: 'update', id: node.id, expect }
  for (const field of FIELDS) {
    const value = field === 'title' ? form.title.trim() : form[field]
    if (value === base.value[field]) continue
    op[field] = value
    expect[field] = base.value[field]
  }
  if (!Object.keys(expect).length) return
  saving.value = true
  const result = await editPlan(
    reasonOf([op], () => node.title, t),
    [op],
  )
  saving.value = false
  // The plan now holds the form; the watch above takes the node over from here. On a
  // conflict or a failure the form stays as the user left it.
  if (result === 'ok') base.value = { ...form, title: form.title.trim() }
}

/** Keep what the user typed over the newer values, and save it. */
function keepMine() {
  base.value = fieldsOf(props.node)
  void save()
}

/** Take the newer values for the fields changed meanwhile, dropping the user's edits there. */
function takeTheirs() {
  for (const { field, value } of changedMeanwhile.value) form[field] = value
  base.value = fieldsOf(props.node)
}

async function remove() {
  const op: PlanOp = { op: 'remove', id: props.node.id }
  if (
    (await editPlan(
      reasonOf([op], () => props.node.title, t),
      [op],
    )) === 'ok'
  )
    emit('removed')
}

// Issues the node's work is about. The user links them by number; the title comes from
// the tracker.
const issueNumber = ref<number | null>(null)
const issueProblem = ref<string | null>(null)
const linking = ref(false)
async function linkIssue() {
  if (!issueNumber.value) return
  linking.value = true
  issueProblem.value = await setNodeIssue(props.node.id, issueNumber.value, true)
  linking.value = false
  if (!issueProblem.value) issueNumber.value = null
}
/** Who linked an issue: the user, the coordinator, or a thread by its title. */
function linkedBy(by: string): string {
  if (by === 'user') return t('plan.linkedByUser')
  if (by === state.detail?.project.coordinatorId) return t('plan.linkedByCoordinator')
  return state.detail?.threads.find((thread) => thread.id === by)?.title ?? by
}

async function unlinkIssue(number: number) {
  issueProblem.value = await setNodeIssue(props.node.id, number, false)
}

const removeQuestion = computed(() => {
  const count = liveThreads(props.node)
  return count
    ? t('plan.removeConfirmThreads', { title: props.node.title, count })
    : t('plan.removeConfirm', { title: props.node.title })
})
</script>

<template>
  <section class="node-detail" :aria-label="node.title">
    <DouForm v-if="editable" layout="vertical" size="sm" class="node-detail-form">
      <DouFormItem :label="t('plan.field.title')">
        <DouInput v-model="form.title" :invalid="!form.title.trim()" />
      </DouFormItem>
      <DouFormItem :label="t('plan.field.description')">
        <DouTextarea v-model="form.description" autosize />
      </DouFormItem>
      <DouFormItem :label="t('plan.acceptance')">
        <DouTextarea v-model="form.acceptance" autosize />
      </DouFormItem>
      <DouAlert v-if="changedMeanwhile.length" tone="warning" :title="t('plan.conflict')">
        <dl class="node-detail-conflict">
          <template v-for="change in changedMeanwhile" :key="change.field">
            <dt>{{ t(labels[change.field]) }}</dt>
            <dd>{{ change.value || t('plan.conflict.empty') }}</dd>
          </template>
        </dl>
        <div class="node-detail-actions">
          <DouButton size="sm" variant="outline" @click="takeTheirs">
            {{ t('plan.conflict.theirs') }}
          </DouButton>
          <DouButton size="sm" tone="primary" :loading="saving" @click="keepMine">
            {{ t('plan.conflict.mine') }}
          </DouButton>
        </div>
      </DouAlert>
      <div class="node-detail-actions">
        <DouPopconfirm
          :title="removeQuestion"
          tone="danger"
          :confirm-text="t('plan.remove')"
          @confirm="remove"
        >
          <DouButton size="sm" variant="ghost" tone="danger">
            <template #leading><TrashIcon /></template>
            {{ t('plan.remove') }}
          </DouButton>
        </DouPopconfirm>
        <DouButton
          size="sm"
          tone="primary"
          :disabled="!dirty || !form.title.trim() || changedMeanwhile.length > 0"
          :loading="saving"
          @click="save"
        >
          {{ t('plan.save') }}
        </DouButton>
      </div>
    </DouForm>
    <template v-else>
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
    </template>
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
            <span class="node-thread-lead"><StageIcon :stage="thread.stage" /></span>
            <span class="node-thread-text">
              <DouText size="sm" :truncate="2" :title="thread.title">{{ thread.title }}</DouText>
              <ThreadHeadline :stage="thread.stage" :headline="thread.lastHeadline" />
            </span>
          </button>
          <span
            v-if="thread.pullRequest || hasBranch(thread.id) || thread.stage === 'review'"
            class="node-thread-tools"
          >
            <ReviewActions v-if="thread.stage === 'review'" :thread-id="thread.id" compact />
            <PullRequestLink v-if="thread.pullRequest" :pull-request="thread.pullRequest" />
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
    <PlanSection
      v-if="node.issues.length || (editable && state.detail?.hasTracker)"
      :title="t('plan.issues')"
      :count="node.issues.length"
    >
      <ul v-if="node.issues.length" class="node-detail-list">
        <li v-for="issue in node.issues" :key="issue.number" class="node-issue">
          <DouLink :href="issue.url" external class="node-issue-link" :title="issue.title">
            <template #leading><CircleDotIcon /></template>
            #{{ issue.number }} {{ issue.title }}
          </DouLink>
          <DouText variant="tertiary" size="sm" class="node-issue-by">
            {{ t('plan.issueLinkedBy', { who: linkedBy(issue.linkedBy) }) }}
          </DouText>
          <DouButton
            v-if="editable"
            size="xs"
            variant="ghost"
            :aria-label="t('plan.issueUnlink', { number: issue.number })"
            :title="t('plan.issueUnlink', { number: issue.number })"
            @click="unlinkIssue(issue.number)"
          >
            <UnlinkIcon />
          </DouButton>
        </li>
      </ul>
      <form
        v-if="editable && state.detail?.hasTracker"
        class="node-detail-link"
        @submit.prevent="linkIssue"
      >
        <DouInputNumber
          v-model="issueNumber"
          size="sm"
          :min="1"
          :step="1"
          :placeholder="t('plan.issueNumber')"
          :aria-label="t('plan.issueNumber')"
        />
        <DouButton size="sm" native-type="submit" :disabled="!issueNumber" :loading="linking">
          <template #leading><LinkPlusIcon /></template>
          {{ t('plan.issueLink') }}
        </DouButton>
      </form>
      <DouText v-if="issueProblem" tone="danger" size="sm" role="alert">{{ issueProblem }}</DouText>
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

.node-detail-conflict {
  margin: 4px 0 0;
}

.node-detail-conflict dd {
  margin: 0 0 4px;
  white-space: pre-wrap;
}

.node-detail-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 8px;
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

.node-issue-link {
  min-width: 0;
  font-size: var(--dou-font-size-sm);
  overflow-wrap: anywhere;
}

.node-detail-link {
  display: flex;
  gap: 6px;
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
