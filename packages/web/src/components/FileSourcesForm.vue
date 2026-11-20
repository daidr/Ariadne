<script setup lang="ts">
import type { ProjectReference } from '@ariadne/core'
import { DouButton, DouText } from 'dou-ui'
import { FolderOpenIcon, PencilIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref, useId, watch } from 'vue'
import { api, ApiError, type SourceInput, type SourceKind } from '../api'
import { t, tm } from '../i18n'
import { pluginRevision } from '../plugin-config'
import { nameFrom, summaryOf } from '../source-status'
import { removeReference, saveFileSources, state } from '../store'
import FileSourceDialog from './FileSourceDialog.vue'

const props = defineProps<{ projectId?: string }>()
type Draft = SourceInput & { id?: string; key: string }
const kinds = ref<SourceKind[]>([])
const drafts = ref<Draft[]>([])
const primary = ref<string | null>(null)
const editing = ref<Draft | null>(null)
const labelId = useId()
const secretsSet = ref<Record<string, string[]>>({})
const problem = ref<string | null>(null)
const loaded = ref(false)
let opened = ''

const defaultKind = computed(() =>
  kinds.value.find((kind) => kind.provides.includes('workspace') && kind.plugin?.ready !== false),
)
const kindOf = (kind: string) => kinds.value.find((item) => item.kind === kind) ?? null
const visibleReferences = computed(() =>
  (props.projectId ? (state.detail?.project.references ?? []) : []).filter(
    (reference) => !drafts.value.some((draft) => draft.id === reference.id),
  ),
)
const editingExisting = computed(
  () => drafts.value.some((draft) => draft.key === editing.value?.key) || !!editing.value?.id,
)
const active = computed(() => drafts.value.find((draft) => draft.key === primary.value))
const primaryName = computed(() =>
  active.value ? nameFrom(kindOf(active.value.kind), active.value.settings) : '',
)
const ready = computed(() => loaded.value && editing.value === null)

function sourceLabel(source: Pick<SourceInput, 'kind' | 'settings'>) {
  const kind = kindOf(source.kind)
  return nameFrom(kind, source.settings) || summaryOf(kind, source.settings) || source.kind
}

function sourceTitle(source: Pick<SourceInput, 'kind' | 'settings'>) {
  return summaryOf(kindOf(source.kind), source.settings) ?? undefined
}

function input() {
  const sources = drafts.value.map(({ key: _key, ...draft }) => draft)
  return sources.length
    ? {
        sources,
        primaryIndex: Math.max(
          0,
          drafts.value.findIndex((draft) => draft.key === primary.value),
        ),
      }
    : { sources }
}

onMounted(async () => {
  try {
    const [available, stored] = await Promise.all([
      api.sourceKinds(),
      props.projectId ? api.getFileSources(props.projectId) : null,
    ])
    kinds.value = available.kinds
    if (stored) {
      drafts.value = stored.sources.map(({ name: _name, ...item }) => ({ ...item, key: item.id }))
      primary.value = stored.primaryId || null
      secretsSet.value = stored.secretsSet
    }
    opened = JSON.stringify(input())
    loaded.value = true
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
    // Creating a project without any files is still possible.
    loaded.value = !props.projectId
  }
})

watch(pluginRevision, async (_revision, _before, onCleanup) => {
  let current = true
  onCleanup(() => {
    current = false
  })
  try {
    const refreshed = await api.sourceKinds()
    if (current) kinds.value = refreshed.kinds
  } catch (error) {
    if (current) problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
  }
})

function add() {
  if (!defaultKind.value || drafts.value.length >= 9) return
  editing.value = {
    key: crypto.randomUUID(),
    kind: defaultKind.value.kind,
    settings: { ...defaultKind.value.defaults },
  }
}

function writable(reference: ProjectReference) {
  editing.value = {
    id: reference.id,
    key: reference.id,
    kind: reference.kind,
    settings: reference.settings,
  }
}

function apply(source: SourceInput) {
  if (!editing.value) return
  const draft = { ...editing.value, ...source }
  const index = drafts.value.findIndex((entry) => entry.key === draft.key)
  if (index === -1) drafts.value.push(draft)
  else drafts.value[index] = draft
  if (!primary.value) primary.value = draft.key
  editing.value = null
}

function remove(key: string) {
  drafts.value = drafts.value.filter((draft) => draft.key !== key)
  if (primary.value === key) primary.value = drafts.value[0]?.key ?? null
}

async function save() {
  if (!props.projectId || !ready.value) return false
  const values = input()
  const key = JSON.stringify(values)
  if (key === opened) return true
  problem.value = await saveFileSources(props.projectId, values)
  if (!problem.value) {
    const saved = state.detail?.project
    if (saved?.id === props.projectId && saved.fileSources) {
      const { sources, primaryId } = saved.fileSources
      drafts.value = sources.map(({ name: _name, ...item }) => ({ ...item, key: item.id }))
      primary.value = primaryId
    }
    opened = JSON.stringify(input())
  }
  return !problem.value
}

defineExpose({ ready, primaryName, input, save })
</script>

<template>
  <div class="file-sources" role="group" :aria-labelledby="labelId">
    <div class="sources-heading">
      <DouText :id="labelId" size="sm" weight="medium">{{ t('newProject.source') }}</DouText>
      <DouButton
        v-if="defaultKind && drafts.length < 9"
        variant="ghost"
        size="sm"
        :aria-label="t('project.sources.add')"
        :title="t('project.sources.add')"
        @click="add"
      >
        <PlusIcon />
      </DouButton>
    </div>
    <ul v-if="drafts.length || visibleReferences.length" class="source-list">
      <li v-for="draft in drafts" :key="draft.key" class="file-source">
        <FolderOpenIcon class="source-icon" />
        <DouText size="sm" truncate class="source-label" :title="sourceTitle(draft)">
          {{ sourceLabel(draft) }}
        </DouText>
        <template v-if="drafts.length > 1">
          <DouText v-if="primary === draft.key" size="sm" variant="secondary">
            {{ t('project.sources.primary') }}
          </DouText>
          <DouButton v-else variant="ghost" size="sm" @click="primary = draft.key">
            {{ t('project.sources.makePrimary') }}
          </DouButton>
        </template>
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="t('project.sources.edit')"
          :title="t('project.sources.edit')"
          @click="editing = draft"
        >
          <PencilIcon />
        </DouButton>
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="t('project.references.remove')"
          :title="t('project.references.remove')"
          @click="remove(draft.key)"
        >
          <TrashIcon />
        </DouButton>
      </li>
      <li v-for="reference in visibleReferences" :key="reference.id" class="file-source">
        <FolderOpenIcon class="source-icon" />
        <DouText size="sm" truncate class="source-label" :title="sourceTitle(reference)">
          {{ sourceLabel(reference) }}
        </DouText>
        <DouText size="sm" variant="secondary">{{ t('project.sources.readOnly') }}</DouText>
        <DouButton variant="ghost" size="sm" @click="writable(reference)">
          {{ t('project.sources.enableWrites') }}
        </DouButton>
        <DouButton
          variant="ghost"
          size="sm"
          :aria-label="t('project.references.remove')"
          :title="t('project.references.remove')"
          @click="removeReference(reference.id)"
        >
          <TrashIcon />
        </DouButton>
      </li>
    </ul>
    <FileSourceDialog
      v-if="editing"
      :key="editing.key"
      :source="editing"
      :existing="editingExisting"
      :kinds="kinds"
      :project-id="projectId"
      :secrets-set="editing.id ? (secretsSet[editing.id] ?? []) : []"
      @apply="apply"
      @cancel="editing = null"
    />
    <DouText v-if="problem" tone="danger" size="sm" role="alert">{{ problem }}</DouText>
  </div>
</template>

<style scoped>
.file-sources,
.source-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.source-list {
  padding: 0;
  margin: 0;
  list-style: none;
}

.sources-heading,
.file-source {
  display: flex;
  align-items: center;
  gap: 8px;
}

.source-icon {
  flex: none;
  color: var(--dou-color-text-secondary);
}

.source-label {
  flex: 1;
  min-width: 0;
}

.sources-heading {
  justify-content: space-between;
}
</style>
