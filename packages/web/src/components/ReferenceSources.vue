<script setup lang="ts">
import { DouButton, DouForm, DouFormItem, DouSelect, DouText } from 'dou-ui'
import {
  AlertTriangleIcon,
  FolderGitIcon,
  FolderOpenIcon,
  PlusIcon,
  ServerIcon,
  TrashIcon,
} from '@dou-ui/icons'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api, type SourceKind } from '../api'
import { lt, t } from '../i18n'
import { addReference, removeReference, state } from '../store'
import SettingsFields from './SettingsFields.vue'

/**
 * The sources the open project only reads, under its own source in the project settings.
 * Adding and removing apply at once: each is connected before it is kept.
 */

const kinds = ref<SourceKind[]>([])
const adding = ref(false)
const saving = ref(false)
const problem = ref<string | null>(null)
const kind = ref<string | null>(null)
const drafts = reactive<Record<string, Record<string, unknown>>>({})
/** Why a reference cannot be read right now, by its id. */
const failed = ref<Record<string, string>>({})

const project = computed(() => state.detail?.project ?? null)
const references = computed(() => project.value?.references ?? [])
const current = computed(() => kinds.value.find((k) => k.kind === kind.value) ?? null)
const kindOptions = computed(() =>
  kinds.value.map((k) => ({ value: k.kind, label: lt(k.title) ?? k.kind })),
)

onMounted(() => {
  api.sourceKinds().then(
    (result) => {
      kinds.value = result.kinds
      kind.value = result.kinds[0]?.kind ?? null
    },
    () => {},
  )
})

// Which of them cannot be read is asked again whenever the list changes.
watch(
  () => references.value.map((reference) => reference.id).join(),
  (ids) => {
    const id = project.value?.id
    if (!id || !ids) return void (failed.value = {})
    api.referenceStatus(id).then(
      (result) => {
        failed.value = Object.fromEntries(result.failed.map((item) => [item.id, item.error]))
      },
      () => {},
    )
  },
  { immediate: true },
)

function values(): Record<string, unknown> {
  const key = kind.value ?? ''
  drafts[key] ??= {}
  return drafts[key]
}

/** One line for a reference, as the header writes the project's own source. */
function summaryOf(reference: { kind: string; settings: Record<string, unknown> }): string {
  const of = kinds.value.find((k) => k.kind === reference.kind)
  if (of?.summary) {
    return of.summary.replace(/\{(\w+)\}/g, (_, key: string) =>
      String(reference.settings[key] ?? ''),
    )
  }
  const value = Object.values(reference.settings).find((v) => typeof v === 'string' && v)
  return typeof value === 'string' ? value : reference.kind
}

function kindOf(reference: { kind: string }): SourceKind | null {
  return kinds.value.find((k) => k.kind === reference.kind) ?? null
}

function browse(_key: string, typed: string) {
  const of = current.value
  if (!of) return Promise.reject(new Error('no kind'))
  return api
    .browseSource(of.kind, {
      projectId: project.value?.id ?? null,
      settings: { ...values() },
      path: typed,
    })
    .then((result) => result.listing)
}

function start() {
  problem.value = null
  adding.value = true
}

async function add() {
  if (!kind.value) return
  saving.value = true
  problem.value = await addReference({ kind: kind.value, settings: { ...values() } })
  saving.value = false
  if (problem.value) return
  delete drafts[kind.value]
  adding.value = false
}
</script>

<template>
  <div class="references">
    <DouText weight="medium" size="sm">{{ t('project.references.title') }}</DouText>
    <div v-for="reference in references" :key="reference.id" class="reference">
      <AlertTriangleIcon
        v-if="failed[reference.id]"
        class="reference-icon reference-failed"
        :title="failed[reference.id]"
      />
      <ServerIcon v-else-if="kindOf(reference)?.remote" class="reference-icon" />
      <FolderGitIcon
        v-else-if="kindOf(reference)?.provides.includes('repo')"
        class="reference-icon"
      />
      <FolderOpenIcon v-else class="reference-icon" />
      <DouText size="sm" class="reference-name">{{ reference.name }}</DouText>
      <DouText
        size="sm"
        variant="secondary"
        class="reference-where"
        :title="failed[reference.id] ?? summaryOf(reference)"
      >
        {{ summaryOf(reference) }}
      </DouText>
      <DouButton
        variant="ghost"
        size="sm"
        class="reference-remove"
        :aria-label="t('project.references.remove')"
        :title="t('project.references.remove')"
        @click="removeReference(reference.id)"
      >
        <TrashIcon />
      </DouButton>
    </div>
    <DouForm v-if="adding" layout="vertical" size="sm" class="reference-form">
      <DouFormItem :label="t('project.source.kind')">
        <DouSelect
          :model-value="kind"
          :options="kindOptions"
          @update:model-value="(value) => (kind = String(value))"
        />
      </DouFormItem>
      <SettingsFields
        v-if="current"
        :key="current.kind"
        :schema="current.settings"
        :values="values()"
        :secrets-set="[]"
        bare
        :browse="current.browse ? browse : undefined"
      />
      <DouText v-if="problem" tone="danger" size="sm" role="alert" class="reference-problem">
        {{ problem }}
      </DouText>
      <div class="reference-actions">
        <DouButton size="sm" tone="primary" :loading="saving" @click="add">
          {{ t('project.references.confirm') }}
        </DouButton>
        <DouButton size="sm" variant="ghost" @click="adding = false">
          {{ t('settings.cancel') }}
        </DouButton>
      </div>
    </DouForm>
    <DouButton v-else variant="ghost" size="sm" class="reference-add" @click="start">
      <template #leading><PlusIcon /></template>
      {{ t('project.references.add') }}
    </DouButton>
  </div>
</template>

<style scoped>
.references {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 24px;
}

.reference {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 28px;
}

.reference-icon {
  flex: none;
  width: 16px;
  height: 16px;
  color: var(--dou-color-text-secondary);
}

.reference-failed {
  color: var(--dou-color-warning);
}

.reference-name {
  flex: none;
}

.reference-where {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Removing shows on the row under the pointer or the keyboard, not on every row. */
.reference-remove {
  opacity: 0;
}

.reference:hover .reference-remove,
.reference-remove:focus-visible {
  opacity: 1;
}

.reference-form {
  margin-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.reference-problem {
  overflow-wrap: anywhere;
}

.reference-actions {
  display: flex;
  gap: 8px;
}

.reference-add {
  align-self: flex-start;
}
</style>
