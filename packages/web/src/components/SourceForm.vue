<script setup lang="ts">
import { DouButton, DouForm, DouFormItem, DouSelect, DouText } from 'dou-ui'
import { computed, onMounted, reactive, ref } from 'vue'
import { api, type SourceKind } from '../api'
import { lt, t } from '../i18n'
import { setSource, state } from '../store'
import SettingsFields from './SettingsFields.vue'

/**
 * Where the open project's files are, in the project settings; the header shows it. Saved
 * with the rest of the settings, and only when it changed: saving connects to it again.
 */

const problem = ref<string | null>(null)
const clearing = ref(false)
const kinds = ref<SourceKind[]>([])
const kind = ref<string | null>(null)
/** Draft settings per kind, so switching kinds back and forth keeps what was typed. */
const drafts = reactive<Record<string, Record<string, unknown>>>({})
const secretsSet = ref<string[]>([])
/** The hosted repository a remote source pushes to, and its form, like the source's. */
const hostingKind = ref<string | null>(null)
const hostingDrafts = reactive<Record<string, Record<string, unknown>>>({})
const hostingSecretsSet = ref<string[]>([])
const storedHostingSecrets = ref<string[]>([])
/** How the server keeps a hosting's secrets apart from the source's. */
const HOSTING = 'hosting.'
/** The secrets the stored source has, restored when its kind is chosen again. */
const storedSecrets = ref<string[]>([])
/** The source as it was when the form opened, to tell whether it was changed at all. */
const opened = ref<string | null>(null)

const source = computed(() => state.detail?.project.source ?? null)
const current = computed(() => kinds.value.find((k) => k.kind === kind.value) ?? null)
const kindOptions = computed(() =>
  kinds.value.map((k) => ({ value: k.kind, label: lt(k.title) ?? k.kind })),
)
const hostingKinds = computed(() => kinds.value.filter((k) => k.hosting))
// Only a source on another machine takes a hosting; a repository source is one already.
const takesHosting = computed(() => !!current.value?.remote && hostingKinds.value.length > 0)
const hostingCurrent = computed(
  () => hostingKinds.value.find((k) => k.kind === hostingKind.value) ?? null,
)
const hostingOptions = computed(() => [
  { value: '', label: t('project.source.hostingNone') },
  ...hostingKinds.value.map((k) => ({ value: k.kind, label: lt(k.title) ?? k.kind })),
])

async function load() {
  problem.value = null
  const project = state.detail?.project
  if (!project) return
  try {
    const [{ kinds: list }, stored] = await Promise.all([
      api.sourceKinds(),
      api.getSource(project.id),
    ])
    kinds.value = list
    kind.value = stored.source?.kind ?? list[0]?.kind ?? null
    const own = stored.secretsSet.filter((key) => !key.startsWith(HOSTING))
    const hosting = stored.secretsSet
      .filter((key) => key.startsWith(HOSTING))
      .map((key) => key.slice(HOSTING.length))
    secretsSet.value = own
    storedSecrets.value = own
    hostingSecretsSet.value = hosting
    storedHostingSecrets.value = hosting
    if (stored.source) drafts[stored.source.kind] = { ...stored.source.settings }
    hostingKind.value = stored.source?.hosting?.kind ?? null
    if (stored.source?.hosting) {
      hostingDrafts[stored.source.hosting.kind] = { ...stored.source.hosting.settings }
    }
    // With no source yet, an untouched form saves none.
    opened.value = sourceKey()
  } catch (error) {
    problem.value = String(error)
  }
}

onMounted(load)

/** The directories at what was typed, on the machine the form so far points at. */
function browse(_key: string, typed: string) {
  const of = current.value
  if (!of) return Promise.reject(new Error('no kind'))
  return api
    .browseSource(of.kind, {
      projectId: state.detail?.project.id ?? null,
      settings: { ...values() },
      path: typed,
    })
    .then((result) => result.listing)
}

function values(): Record<string, unknown> {
  const key = kind.value ?? ''
  drafts[key] ??= {}
  return drafts[key]
}

function hostingValues(): Record<string, unknown> {
  const key = hostingKind.value ?? ''
  hostingDrafts[key] ??= {}
  return hostingDrafts[key]
}

function chooseHosting(next: string) {
  hostingKind.value = next || null
  hostingSecretsSet.value =
    next && next === source.value?.hosting?.kind ? storedHostingSecrets.value : []
}

function clearHostingSecret(key: string) {
  hostingValues()[key] = null
  hostingSecretsSet.value = hostingSecretsSet.value.filter((name) => name !== key)
  storedHostingSecrets.value = storedHostingSecrets.value.filter((name) => name !== key)
}

/** The hosting the form would save: none unless the source takes one. */
function hostingInput() {
  return takesHosting.value && hostingKind.value
    ? { kind: hostingKind.value, settings: { ...hostingValues() } }
    : null
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
  return JSON.stringify([kind.value, values(), hostingInput()])
}

/** Save the source if it changed; `false` with the reason shown when it was refused. */
async function save(): Promise<boolean> {
  if (opened.value === null || !kind.value || sourceKey() === opened.value) return true
  problem.value = await setSource({
    kind: kind.value,
    settings: { ...values() },
    hosting: hostingInput(),
  })
  if (!problem.value) opened.value = sourceKey()
  return !problem.value
}

/** Let go of the source at once, like a removal elsewhere in the settings. */
async function clear() {
  clearing.value = true
  problem.value = await setSource(null)
  clearing.value = false
  if (!problem.value) void load()
}

defineExpose({ save })
</script>

<template>
  <DouForm layout="vertical" size="sm" class="source-form">
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
      :browse="current.browse ? browse : undefined"
      @clear-secret="clearSecret"
    />
    <template v-if="takesHosting">
      <DouFormItem :label="t('project.source.hosting')">
        <DouSelect
          :model-value="hostingKind ?? ''"
          :options="hostingOptions"
          @update:model-value="(value) => chooseHosting(String(value))"
        />
      </DouFormItem>
      <SettingsFields
        v-if="hostingCurrent"
        :key="`hosting-${hostingCurrent.kind}`"
        :schema="hostingCurrent.settings"
        :values="hostingValues()"
        :secrets-set="hostingSecretsSet"
        @clear-secret="clearHostingSecret"
      />
    </template>
    <DouText v-if="problem" tone="danger" size="sm" role="alert" class="source-problem">
      {{ problem }}
    </DouText>
    <DouButton
      v-if="source"
      size="sm"
      variant="ghost"
      tone="danger"
      class="source-clear"
      :loading="clearing"
      @click="clear"
    >
      {{ t('project.source.clear') }}
    </DouButton>
  </DouForm>
</template>

<style scoped>
.source-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.source-problem {
  overflow-wrap: anywhere;
}

.source-clear {
  align-self: flex-start;
}
</style>
