<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouForm,
  DouFormItem,
  DouInputNumber,
  DouSelect,
  DouText,
} from 'dou-ui'
import { FolderGitIcon, FolderOpenIcon, PencilIcon } from '@dou-ui/icons'
import { computed, onMounted, reactive, ref } from 'vue'
import { api, type SandboxStatus, type SourceKind } from '../api'
import { lt, t } from '../i18n'
import { setProjectSlots, setSource, state } from '../store'
import SettingsFields from './SettingsFields.vue'

const editing = ref(false)
const saving = ref(false)
const problem = ref<string | null>(null)
const kinds = ref<SourceKind[]>([])
const kind = ref<string | null>(null)
/** Draft settings per kind, so switching kinds back and forth keeps what was typed. */
const drafts = reactive<Record<string, Record<string, unknown>>>({})
const secretsSet = ref<string[]>([])
/** This project's share of the work slots; empty leaves it to the shared total. */
const slots = ref<number | null>(null)
/** The secrets the stored source has, restored when its kind is chosen again. */
const storedSecrets = ref<string[]>([])
/** The source as it was when the form opened, to tell whether it was changed at all. */
const opened = ref<string | null>(null)

const source = computed(() => state.detail?.project.source ?? null)

// Without a sandbox, thread commands can read Ariadne's token; the user should know.
const sandbox = ref<SandboxStatus | null>(null)
onMounted(() => {
  api.getSandbox().then(
    (result) => (sandbox.value = result.sandbox),
    () => {},
  )
})
const unsandboxed = computed(() => !!source.value && sandbox.value?.kind === null)
// macOS's sandbox keeps files out of reach but not the server's processes.
const weaker = computed(() => !!source.value && sandbox.value?.kind === 'sandbox-exec')
const current = computed(() => kinds.value.find((k) => k.kind === kind.value) ?? null)
const kindOptions = computed(() =>
  kinds.value.map((k) => ({ value: k.kind, label: lt(k.title) ?? k.kind })),
)

/** One line for the source: its first plain setting, e.g. the path or the repository. */
const summary = computed(() => {
  const settings = source.value?.settings ?? {}
  const value = Object.values(settings).find((v) => typeof v === 'string' && v)
  return typeof value === 'string' ? value : null
})
const isRepo = computed(() => source.value?.kind !== 'local-directory')

async function edit() {
  problem.value = null
  editing.value = true
  const project = state.detail?.project
  if (!project) return
  slots.value = project.slots
  try {
    const [{ kinds: list }, stored] = await Promise.all([
      api.sourceKinds(),
      api.getSource(project.id),
    ])
    kinds.value = list
    kind.value = stored.source?.kind ?? list[0]?.kind ?? null
    secretsSet.value = stored.secretsSet
    storedSecrets.value = stored.secretsSet
    if (stored.source) drafts[stored.source.kind] = { ...stored.source.settings }
    // With no source yet, an untouched form saves none.
    opened.value = sourceKey()
  } catch (error) {
    problem.value = String(error)
  }
}

function values(): Record<string, unknown> {
  const key = kind.value ?? ''
  drafts[key] ??= {}
  return drafts[key]
}

function chooseKind(next: string) {
  kind.value = next
  // Secrets belong to the stored kind; another kind starts without them.
  secretsSet.value = next === source.value?.kind ? storedSecrets.value : []
}

function clearSecret(key: string) {
  values()[key] = null
  secretsSet.value = secretsSet.value.filter((name) => name !== key)
  storedSecrets.value = storedSecrets.value.filter((name) => name !== key)
}

/** The chosen kind and what was typed for it, including secrets typed or cleared. */
function sourceKey() {
  return JSON.stringify([kind.value, values()])
}

async function save(clear = false) {
  saving.value = true
  // Saving the source connects to it again, so it is saved only when it changed: changing
  // the slot cap alone leaves the source as it is.
  problem.value =
    clear || !kind.value
      ? source.value
        ? await setSource(null)
        : null
      : sourceKey() !== opened.value
        ? await setSource({ kind: kind.value, settings: { ...values() } })
        : null
  // Clearing the source is about the source alone; the slot cap waits for a save.
  if (!clear && !problem.value && slots.value !== (state.detail?.project.slots ?? null)) {
    problem.value = await setProjectSlots(slots.value || null)
  }
  saving.value = false
  if (!problem.value) editing.value = false
}
</script>

<template>
  <section v-if="state.detail" class="source" :aria-label="t('project.source.title')">
    <DouText variant="secondary" size="sm">{{ t('project.source.title') }}</DouText>
    <DouForm v-if="editing" layout="vertical" size="sm" class="source-form" @submit="save()">
      <DouFormItem :label="t('project.source.kind')">
        <DouSelect
          :model-value="kind"
          :options="kindOptions"
          @update:model-value="(value) => chooseKind(String(value))"
        />
      </DouFormItem>
      <SettingsFields
        v-if="current"
        :key="current.kind"
        :schema="current.settings"
        :values="values()"
        :secrets-set="secretsSet"
        @clear-secret="clearSecret"
      />
      <DouFormItem :label="t('project.slots')" :help="t('project.slotsHelp')">
        <DouInputNumber
          v-model="slots"
          :min="1"
          :max="16"
          :step="1"
          :placeholder="t('project.slotsShared')"
        />
      </DouFormItem>
      <DouText v-if="problem" tone="danger" size="sm" role="alert" class="source-problem">
        {{ problem }}
      </DouText>
      <div class="source-actions">
        <DouButton v-if="source" size="sm" variant="ghost" tone="danger" @click="save(true)">
          {{ t('project.source.clear') }}
        </DouButton>
        <DouButton size="sm" variant="ghost" @click="editing = false">
          {{ t('settings.cancel') }}
        </DouButton>
        <DouButton size="sm" tone="primary" native-type="submit" :loading="saving">
          {{ t('project.source.save') }}
        </DouButton>
      </div>
    </DouForm>
    <div v-else class="source-row">
      <FolderGitIcon v-if="source && isRepo" class="source-icon" />
      <FolderOpenIcon v-else class="source-icon" />
      <DouText v-if="summary" size="sm" truncate :title="summary" class="source-path">
        {{ summary }}
      </DouText>
      <DouText v-else variant="tertiary" size="sm" class="source-path">
        {{ t('project.source.none') }}
      </DouText>
      <DouButton
        size="sm"
        variant="ghost"
        :aria-label="t('project.source.edit')"
        :title="t('project.source.edit')"
        @click="edit"
      >
        <PencilIcon />
      </DouButton>
    </div>
    <DouAlert
      v-if="unsandboxed && sandbox"
      tone="warning"
      role="alert"
      :title="t(sandbox.mode === 'off' ? 'sandbox.off.title' : 'sandbox.missing.title')"
    >
      {{ t(sandbox.mode === 'off' ? 'sandbox.off.body' : 'sandbox.missing.body') }}
    </DouAlert>
    <DouText v-else-if="weaker" variant="tertiary" size="sm">{{ t('sandbox.weaker') }}</DouText>
  </section>
</template>

<style scoped>
.source {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--dou-color-border-subtle);
}

.source-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.source-icon {
  flex: none;
  color: var(--dou-color-text-tertiary);
}

.source-path {
  flex: 1;
  min-width: 0;
}

.source-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.source-problem {
  overflow-wrap: anywhere;
}

.source-actions {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
}
</style>
