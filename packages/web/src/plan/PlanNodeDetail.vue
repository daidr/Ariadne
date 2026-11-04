<script setup lang="ts">
import type { PlanNodeView, PlanOp } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouInput,
  DouInputNumber,
  DouLink,
  DouPopconfirm,
  DouTag,
  DouText,
  DouTextarea,
} from 'dou-ui'
import { CircleDotIcon, FileDiffIcon, LinkPlusIcon, TrashIcon, UnlinkIcon } from '@dou-ui/icons'
import { computed, reactive, ref, watch } from 'vue'
import { t } from '../i18n'
import { editPlan, openMain, openThread, setNodeIssue, state } from '../store'
import { mergeIntoForm, reasonOf } from './edit'
import PullRequestLink from './PlanPullRequest.vue'
import StageLabel from '../components/StageLabel.vue'
import ThreadHeadline from '../components/ThreadHeadline.vue'
import ReportArtifacts from '../components/ReportArtifacts.vue'
import { showsHeadline } from '../stage'

const props = defineProps<{ node: PlanNodeView; editable?: boolean }>()

/** The artifacts a thread waiting on the user pointed to in its last report. */
const reportArtifacts = (threadId: string) => {
  const thread = state.detail?.threads.find((item) => item.id === threadId)
  return thread && showsHeadline(thread.stage) ? thread.lastArtifacts : []
}

/** Whether the thread has a branch whose changes can be shown. */
const hasBranch = (threadId: string) => {
  const workspace = state.detail?.threads.find((thread) => thread.id === threadId)?.workspace
  return !!workspace?.branch && !!workspace.base
}
const emit = defineEmits<{ removed: [] }>()

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

const removeQuestion = computed(() =>
  props.node.threads.length
    ? t('plan.removeConfirmThreads', { title: props.node.title, count: props.node.threads.length })
    : t('plan.removeConfirm', { title: props.node.title }),
)
</script>

<template>
  <section class="node-detail" :aria-label="node.title">
    <template v-if="editable">
      <DouText variant="secondary" size="sm">{{ t('plan.field.title') }}</DouText>
      <DouInput v-model="form.title" size="sm" :invalid="!form.title.trim()" />
      <DouText variant="secondary" size="sm">{{ t('plan.field.description') }}</DouText>
      <DouTextarea v-model="form.description" size="sm" autosize />
      <DouText variant="secondary" size="sm">{{ t('plan.acceptance') }}</DouText>
      <DouTextarea v-model="form.acceptance" size="sm" autosize />
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
    </template>
    <template v-else>
      <div class="node-detail-head">
        <DouText weight="medium">{{ node.title }}</DouText>
        <DouText variant="tertiary" size="sm">{{ t(`plan.kind.${node.kind}`) }}</DouText>
      </div>
      <DouText v-if="node.description" size="sm" class="node-detail-text">
        {{ node.description }}
      </DouText>
      <template v-if="node.acceptance">
        <DouText variant="secondary" size="sm">{{ t('plan.acceptance') }}</DouText>
        <DouText size="sm" class="node-detail-text">{{ node.acceptance }}</DouText>
      </template>
    </template>
    <DouText variant="secondary" size="sm">{{ t('plan.threads') }}</DouText>
    <ul v-if="node.threads.length" class="node-detail-threads">
      <li v-for="thread in node.threads" :key="thread.id" class="node-detail-thread">
        <DouButton size="sm" variant="ghost" @click="openThread(thread.id)">
          {{ thread.title }}
          <template #trailing>
            <StageLabel :stage="thread.stage" />
          </template>
        </DouButton>
        <PullRequestLink v-if="thread.pullRequest" :pull-request="thread.pullRequest" />
        <DouButton
          v-if="hasBranch(thread.id)"
          size="sm"
          variant="ghost"
          :aria-label="t('changes.open')"
          :title="t('changes.open')"
          @click="openMain({ kind: 'changes', threadId: thread.id })"
        >
          <FileDiffIcon />
        </DouButton>
        <ThreadHeadline
          class="node-detail-headline"
          :stage="thread.stage"
          :headline="thread.lastHeadline"
        />
        <ReportArtifacts class="node-detail-headline" :artifacts="reportArtifacts(thread.id)" />
      </li>
    </ul>
    <DouText v-else variant="tertiary" size="sm">{{ t('plan.noThread') }}</DouText>
    <template v-if="node.issues.length || (editable && state.detail?.hasTracker)">
      <DouText variant="secondary" size="sm">{{ t('plan.issues') }}</DouText>
      <ul v-if="node.issues.length" class="node-detail-threads">
        <li v-for="issue in node.issues" :key="issue.number" class="node-detail-thread">
          <DouLink :href="issue.url" external class="node-detail-issue">
            <CircleDotIcon class="node-detail-issue-icon" />
            <span>#{{ issue.number }} {{ issue.title }}</span>
          </DouLink>
          <DouText variant="tertiary" size="sm">
            {{ t('plan.issueLinkedBy', { who: linkedBy(issue.linkedBy) }) }}
          </DouText>
          <DouButton
            v-if="editable"
            size="sm"
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
    </template>
  </section>
</template>

<style scoped>
.node-detail {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.node-detail-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.node-detail-text {
  white-space: pre-wrap;
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

.node-detail-threads {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.node-detail-issue {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  overflow-wrap: anywhere;
}

.node-detail-issue-icon {
  flex: none;
}

.node-detail-link {
  display: flex;
  gap: 6px;
}

.node-detail-thread {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}

.node-detail-headline {
  flex-basis: 100%;
  /* In line with the thread's title, which sits inside a small text button. */
  padding-inline-start: var(--dou-control-padding-x-sm);
}
</style>
