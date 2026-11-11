<script setup lang="ts">
import { DouButton, DouForm, DouFormItem, DouInput, DouSelect, DouText } from 'dou-ui'
import { FolderGitIcon, FolderOpenIcon, PlusIcon, ServerIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api, type SourceKind } from '../api'
import { lt, t } from '../i18n'
import { nameFrom, summaryOf } from '../source-status'
import { offerNotifications } from '../notify'
import { focusCoordinator } from '../shortcuts'
import { startProject } from '../store'
import SettingsFields from './SettingsFields.vue'

/**
 * A new project: what it is called, where its files are and the sources it only reads.
 * Nothing is connected until it is created; then all are, and the project is made only
 * when every one of them can be reached.
 */

const kinds = ref<SourceKind[]>([])
/** The kind of the project's own source; `''` for none. */
const kind = ref('')
/** Draft settings per kind, so switching kinds back and forth keeps what was typed. */
const drafts = reactive<Record<string, Record<string, unknown>>>({})
const name = ref('')
/** The name was typed, and no longer follows the source. */
const named = ref(false)
const references = ref<{ kind: string; settings: Record<string, unknown> }[]>([])
/** The kind of the reference being added, while one is. */
const adding = ref<string | null>(null)
const referenceDrafts = reactive<Record<string, Record<string, unknown>>>({})
const creating = ref(false)
const problem = ref<string | null>(null)

onMounted(async () => {
  try {
    kinds.value = (await api.sourceKinds()).kinds
    kind.value = kinds.value[0]?.kind ?? ''
  } catch {
    // Without them the project starts with no source; its settings take one later.
  }
})

const kindOf = (of: string) => kinds.value.find((k) => k.kind === of) ?? null
const current = computed(() => kindOf(kind.value))
const kindOptions = computed(() => [
  ...kinds.value.map((k) => ({ value: k.kind, label: lt(k.title) ?? k.kind })),
  { value: '', label: t('newProject.noSource') },
])
const referenceOptions = computed(() =>
  kinds.value.map((k) => ({ value: k.kind, label: lt(k.title) ?? k.kind })),
)

function draftOf(store: Record<string, Record<string, unknown>>, of: string) {
  store[of] ??= {}
  return store[of]
}

/** Every required setting of the kind is filled in. */
function filled(of: SourceKind | null, settings: Record<string, unknown>) {
  return (of?.settings.required ?? []).every((key) => {
    const value = settings[key]
    return typeof value === 'string' ? !!value.trim() : value !== undefined && value !== null
  })
}

const source = computed(() =>
  current.value ? { kind: current.value.kind, settings: { ...draftOf(drafts, kind.value) } } : null,
)

// Until it is typed, the name follows the source: its directory or repository.
watch(
  () => (source.value ? nameFrom(current.value, source.value.settings) : ''),
  (suggested) => {
    if (!named.value) name.value = suggested
  },
)

function rename(value: string) {
  name.value = value
  named.value = !!value.trim()
}

const ready = computed(
  () =>
    !!name.value.trim() &&
    (!current.value || filled(current.value, source.value?.settings ?? {})) &&
    adding.value === null,
)

function browse(of: string, settings: Record<string, unknown>, typed: string) {
  return api
    .browseSource(of, { projectId: null, settings: { ...settings }, path: typed })
    .then((result) => result.listing)
}

function startAdding() {
  adding.value = kinds.value[0]?.kind ?? null
}

const addingKind = computed(() => (adding.value ? kindOf(adding.value) : null))
const referenceReady = computed(
  () => !!adding.value && filled(addingKind.value, draftOf(referenceDrafts, adding.value)),
)

function addReference() {
  if (!adding.value) return
  references.value.push({
    kind: adding.value,
    settings: { ...draftOf(referenceDrafts, adding.value) },
  })
  delete referenceDrafts[adding.value]
  adding.value = null
}

function iconOf(of: string) {
  const known = kindOf(of)
  if (known?.remote) return ServerIcon
  if (known?.provides.includes('repo')) return FolderGitIcon
  return FolderOpenIcon
}

async function create() {
  if (!ready.value || creating.value) return
  creating.value = true
  problem.value = null
  problem.value = await startProject({
    name: name.value.trim(),
    ...(source.value ? { source: source.value } : {}),
    ...(references.value.length ? { references: references.value } : {}),
  })
  creating.value = false
  if (problem.value) return
  // This form is gone with the project open; what comes next is said to its coordinator.
  offerNotifications()
  void focusCoordinator()
}

defineExpose({ create, ready, creating })
</script>

<template>
  <DouForm layout="vertical" size="sm" class="new-project" @submit.prevent="create">
    <DouFormItem :label="t('newProject.source')">
      <DouSelect
        :model-value="kind"
        :options="kindOptions"
        @update:model-value="(value) => (kind = String(value ?? ''))"
      />
    </DouFormItem>
    <SettingsFields
      v-if="current"
      :key="current.kind"
      :schema="current.settings"
      :values="draftOf(drafts, current.kind)"
      :secrets-set="[]"
      bare
      :browse="
        current.browse
          ? (_key, typed) => browse(current!.kind, draftOf(drafts, current!.kind), typed)
          : undefined
      "
    />
    <DouFormItem :label="t('newProject.name')" required>
      <DouInput :model-value="name" @update:model-value="rename" />
    </DouFormItem>
    <div class="new-project-references">
      <DouText weight="medium" size="sm">{{ t('project.references.title') }}</DouText>
      <div v-for="(reference, index) in references" :key="index" class="new-project-reference">
        <component :is="iconOf(reference.kind)" class="new-project-icon" />
        <DouText size="sm" class="new-project-where">
          {{ summaryOf(kindOf(reference.kind), reference.settings) ?? reference.kind }}
        </DouText>
        <DouButton
          variant="ghost"
          size="sm"
          class="new-project-remove"
          :aria-label="t('project.references.remove')"
          :title="t('project.references.remove')"
          @click="references.splice(index, 1)"
        >
          <TrashIcon />
        </DouButton>
      </div>
      <div v-if="adding" class="new-project-adding">
        <DouFormItem :label="t('project.source.kind')">
          <DouSelect
            :model-value="adding"
            :options="referenceOptions"
            @update:model-value="(value) => (adding = String(value))"
          />
        </DouFormItem>
        <SettingsFields
          v-if="addingKind"
          :key="addingKind.kind"
          :schema="addingKind.settings"
          :values="draftOf(referenceDrafts, addingKind.kind)"
          :secrets-set="[]"
          bare
          :browse="
            addingKind.browse
              ? (_key, typed) =>
                  browse(addingKind!.kind, draftOf(referenceDrafts, addingKind!.kind), typed)
              : undefined
          "
        />
        <div class="new-project-actions">
          <DouButton size="sm" tone="primary" :disabled="!referenceReady" @click="addReference">
            {{ t('project.references.confirm') }}
          </DouButton>
          <DouButton size="sm" variant="ghost" @click="adding = null">
            {{ t('settings.cancel') }}
          </DouButton>
        </div>
      </div>
      <DouButton v-else variant="ghost" size="sm" class="new-project-add" @click="startAdding">
        <template #leading><PlusIcon /></template>
        {{ t('project.references.add') }}
      </DouButton>
    </div>
    <DouText v-if="problem" tone="danger" size="sm" role="alert" class="new-project-problem">
      {{ problem }}
    </DouText>
  </DouForm>
</template>

<style scoped>
.new-project {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.new-project-references {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.new-project-reference {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 28px;
}

.new-project-icon {
  flex: none;
  width: 16px;
  height: 16px;
  color: var(--dou-color-text-secondary);
}

.new-project-where {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.new-project-adding {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.new-project-actions {
  display: flex;
  gap: 8px;
}

.new-project-add {
  align-self: flex-start;
}

.new-project-problem {
  overflow-wrap: anywhere;
}
</style>
