<script setup lang="ts">
import type { ProjectReference } from '@ariadne/core'
import { DouButton, DouFormItem, DouSelect, DouText } from 'dou-ui'
import { FolderOpenIcon, PencilIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type SourceInput, type SourceKind } from '../api'
import { lt, t, tm } from '../i18n'
import { nameFrom, summaryOf } from '../source-status'
import { removeReference, saveFileSources, state } from '../store'
import SettingsFields from './SettingsFields.vue'

const props = defineProps<{ projectId?: string }>()
type Draft = SourceInput & { id?: string; key: string }
const kinds = ref<SourceKind[]>([])
const drafts = ref<Draft[]>([])
const primary = ref<string | null>(null)
const editing = ref<Draft | null>(null)
const editingKind = ref('')
const secretsSet = ref<Record<string, string[]>>({})
const problem = ref<string | null>(null)
const loaded = ref(false)
let opened = ''

const kindsThatWrite = computed(() =>
  kinds.value.filter((kind) => kind.provides.includes('workspace')),
)
const kindOf = (kind: string) => kinds.value.find((item) => item.kind === kind) ?? null
const current = computed(() => kindOf(editing.value?.kind ?? ''))
const hostingKinds = computed(() => kinds.value.filter((kind) => kind.hosting))
const hosting = computed(() => kindOf(editing.value?.hosting?.kind ?? ''))
const visibleReferences = computed(() =>
  (props.projectId ? (state.detail?.project.references ?? []) : []).filter(
    (reference) => !drafts.value.some((draft) => draft.id === reference.id),
  ),
)
const kindOptions = computed(() =>
  kindsThatWrite.value.map((kind) => ({ value: kind.kind, label: lt(kind.title) ?? kind.kind })),
)
const hostingOptions = computed(() => [
  { value: '', label: t('project.source.hostingNone') },
  ...hostingKinds.value.map((kind) => ({ value: kind.kind, label: lt(kind.title) ?? kind.kind })),
])
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
    if (!props.projectId && kindsThatWrite.value.length) add()
  } catch (error) {
    problem.value = error instanceof ApiError ? tm(error.localized) : String(error)
    // Creating a project without any files is still possible.
    loaded.value = !props.projectId
  }
})

function add() {
  const kind = kindsThatWrite.value[0]?.kind
  if (kind) edit({ key: crypto.randomUUID(), kind, settings: {} })
}

function edit(draft: Draft) {
  editingKind.value = draft.kind
  editing.value = {
    ...draft,
    settings: { ...draft.settings },
    hosting: draft.hosting
      ? { kind: draft.hosting.kind, settings: { ...draft.hosting.settings } }
      : null,
  }
}

function writable(reference: ProjectReference) {
  edit({ id: reference.id, key: reference.id, kind: reference.kind, settings: reference.settings })
}

function changeKind(value: unknown) {
  if (!editing.value) return
  editing.value.kind = String(value)
  editing.value.settings = {}
  editing.value.hosting = null
}

function changeHosting(value: unknown) {
  if (editing.value) editing.value.hosting = value ? { kind: String(value), settings: {} } : null
}

const storedKeys = computed(() => {
  const draft = editing.value
  return draft?.id && draft.kind === editingKind.value ? (secretsSet.value[draft.id] ?? []) : []
})
const mainKeys = computed(() => storedKeys.value.filter((key) => !key.startsWith('hosting.')))
const hostingKeys = computed(() => {
  const original = drafts.value.find((draft) => draft.key === editing.value?.key)
  return original?.hosting?.kind === editing.value?.hosting?.kind
    ? storedKeys.value.filter((key) => key.startsWith('hosting.')).map((key) => key.slice(8))
    : []
})
const canApply = computed(
  () =>
    !!current.value &&
    (current.value.settings.required ?? []).every((key) => {
      const value = editing.value?.settings[key]
      return typeof value === 'string'
        ? !!value.trim()
        : value != null || mainKeys.value.includes(key)
    }),
)

function confirm() {
  if (!editing.value || !canApply.value) return false
  const draft = editing.value
  const i = drafts.value.findIndex((entry) => entry.key === draft.key)
  if (i === -1) drafts.value.push(draft)
  else drafts.value[i] = draft
  if (!primary.value) primary.value = draft.key
  editing.value = null
  return true
}

function remove(key: string) {
  drafts.value = drafts.value.filter((draft) => draft.key !== key)
  if (primary.value === key) primary.value = drafts.value[0]?.key ?? null
}

function browse(_key: string, path: string) {
  const draft = editing.value
  if (!draft) return Promise.reject(new Error('source form is closed'))
  return api
    .browseSource(draft.kind, {
      projectId: props.projectId ?? null,
      sourceId: draft.id ?? '',
      settings: { ...draft.settings },
      path,
    })
    .then((result) => result.listing)
}

async function save() {
  if (!props.projectId || !loaded.value) return false
  if (editing.value && !confirm()) return false
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

defineExpose({ ready, primaryName, editing, confirm, input, save })
</script>

<template>
  <div class="file-sources">
    <div v-for="draft in drafts" :key="draft.key" class="file-source">
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
        @click="edit(draft)"
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
    </div>
    <div v-for="reference in visibleReferences" :key="reference.id" class="file-source">
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
    </div>
    <template v-if="editing">
      <DouFormItem :label="t('project.source.kind')">
        <DouSelect
          :model-value="editing.kind"
          :options="kindOptions"
          @update:model-value="changeKind"
        />
      </DouFormItem>
      <SettingsFields
        v-if="current"
        :key="current.kind"
        :schema="current.settings"
        :values="editing.settings"
        :secrets-set="mainKeys"
        :browse="current.browse ? browse : undefined"
        @clear-secret="
          (key) => {
            if (editing) editing.settings[key] = null
          }
        "
      />
      <template v-if="current?.remote && hostingKinds.length">
        <DouFormItem :label="t('project.source.hosting')">
          <DouSelect
            :model-value="editing.hosting?.kind ?? ''"
            :options="hostingOptions"
            @update:model-value="changeHosting"
          />
        </DouFormItem>
        <SettingsFields
          v-if="hosting && editing.hosting"
          :key="'hosting-' + hosting.kind"
          :schema="hosting.settings"
          :values="editing.hosting.settings"
          :secrets-set="hostingKeys"
          @clear-secret="
            (key) => {
              if (editing?.hosting) editing.hosting.settings[key] = null
            }
          "
        />
      </template>
      <div class="source-actions">
        <DouButton size="sm" tone="primary" :disabled="!canApply" @click="confirm">
          {{ t('project.references.confirm') }}
        </DouButton>
        <DouButton size="sm" variant="ghost" @click="editing = null">{{
          t('settings.cancel')
        }}</DouButton>
      </div>
    </template>
    <DouButton
      v-else-if="kindsThatWrite.length && drafts.length < 9"
      variant="ghost"
      size="sm"
      class="add-source"
      @click="add"
    >
      <template #leading><PlusIcon /></template>
      {{ t('project.sources.add') }}
    </DouButton>
    <DouText v-if="problem" tone="danger" size="sm" role="alert">{{ problem }}</DouText>
  </div>
</template>

<style scoped>
.file-sources {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.file-source,
.source-actions {
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

.add-source {
  align-self: flex-start;
}
</style>
