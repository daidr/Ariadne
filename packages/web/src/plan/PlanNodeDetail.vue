<script setup lang="ts">
import type { PlanNodeView, PlanOp } from '@ariadne/core'
import { DouButton, DouInput, DouPopconfirm, DouTag, DouText, DouTextarea } from 'dou-ui'
import { TrashIcon } from '@dou-ui/icons'
import { computed, reactive, ref, watch } from 'vue'
import { t, tm } from '../i18n'
import { stageTone } from '../stage'
import { editPlan, openThread } from '../store'
import { reasonOf } from './edit'
import PullRequestLink from './PlanPullRequest.vue'

const props = defineProps<{ node: PlanNodeView; editable?: boolean }>()
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
let base: Fields = { title: '', description: '', acceptance: '' }
const differs = (a: Fields, b: Fields) =>
  a.title.trim() !== b.title || a.description !== b.description || a.acceptance !== b.acceptance
watch(
  () => [props.node.id, props.node.title, props.node.description, props.node.acceptance],
  ([id], previous) => {
    if (previous && id === previous[0] && differs(form, base)) return
    base = fieldsOf(props.node)
    Object.assign(form, base)
  },
  { immediate: true },
)
const dirty = computed(() => differs(form, fieldsOf(props.node)))
const saving = ref(false)

async function save() {
  const { node } = props
  // `expect` holds what the user started from, so a save refuses to overwrite what the
  // coordinator changed in the meantime.
  const expect: Partial<Fields> = {}
  const op: Extract<PlanOp, { op: 'update' }> = { op: 'update', id: node.id, expect }
  for (const field of FIELDS) {
    const value = field === 'title' ? form.title.trim() : form[field]
    if (value === base[field]) continue
    op[field] = value
    expect[field] = base[field]
  }
  if (!Object.keys(expect).length) return
  saving.value = true
  const ok = await editPlan(tm(reasonOf([op], () => node.title)), [op])
  saving.value = false
  if (ok) {
    // The plan now holds the form; the watch above takes the node over from here.
    base = { ...form, title: form.title.trim() }
  } else {
    // Refused, e.g. the coordinator changed the field meanwhile: start over from the plan.
    base = fieldsOf(props.node)
    Object.assign(form, base)
  }
}

async function remove() {
  const op: PlanOp = { op: 'remove', id: props.node.id }
  if (await editPlan(tm(reasonOf([op], () => props.node.title)), [op])) emit('removed')
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
          :disabled="!dirty || !form.title.trim()"
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
            <DouTag size="sm" :tone="stageTone[thread.stage]">
              {{ t(`thread.stage.${thread.stage}`) }}
            </DouTag>
          </template>
        </DouButton>
        <PullRequestLink v-if="thread.pullRequest" :pull-request="thread.pullRequest" />
      </li>
    </ul>
    <DouText v-else variant="tertiary" size="sm">{{ t('plan.noThread') }}</DouText>
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

.node-detail-actions {
  display: flex;
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

.node-detail-thread {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}
</style>
