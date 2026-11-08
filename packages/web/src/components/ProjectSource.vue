<script setup lang="ts">
import { DouAlert, DouButton, DouForm, DouFormItem, DouPopover, DouSelect, DouText } from 'dou-ui'
import { FolderGitIcon, FolderOpenIcon, PencilIcon, ServerIcon } from '@dou-ui/icons'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api, type SandboxStatus, type SourceKind } from '../api'
import { lt, t } from '../i18n'
import { setSource, state } from '../store'
import SettingsFields from './SettingsFields.vue'

const editing = ref(false)
const saving = ref(false)
const problem = ref<string | null>(null)
const kinds = ref<SourceKind[]>([])
/** Whether `kinds` came back: until then a remote source cannot be told from a local one. */
const kindsKnown = ref(false)
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

// With no source there is nothing to look at, so opening goes straight to the form. A form
// left half filled is dropped when the popover closes.
watch(
  () => state.sourceOpen,
  (open) => {
    if (!open) editing.value = false
    else if (!state.detail?.project.source) void edit()
  },
)

const source = computed(() => state.detail?.project.source ?? null)

// Without a sandbox, thread commands can read Ariadne's token; the user should know.
const sandbox = ref<SandboxStatus | null>(null)
onMounted(() => {
  api.getSandbox().then(
    (result) => (sandbox.value = result.sandbox),
    () => {},
  )
  api.sourceKinds().then(
    (result) => {
      kinds.value = result.kinds
      kindsKnown.value = true
    },
    () => {},
  )
})
/** The provider of the stored source, once the kinds are known. */
const storedKind = computed(() => kinds.value.find((k) => k.kind === source.value?.kind) ?? null)
// A remote source runs commands on another machine; this one's sandbox is beside the point.
const remote = computed(() => !!storedKind.value?.remote)
// Notes about this machine's sandbox wait for the kinds, so a remote source never shows one.
const local = computed(() => !!source.value && kindsKnown.value && !remote.value)
const unsandboxed = computed(() => local.value && sandbox.value?.kind === null)
const sandboxNote = computed(() =>
  sandbox.value?.mode === 'off'
    ? 'sandbox.off.body'
    : sandbox.value?.platform === 'win32'
      ? 'sandbox.windows.body'
      : 'sandbox.missing.body',
)
// macOS's sandbox keeps files out of reach but not the server's processes.
const weaker = computed(() => local.value && sandbox.value?.kind === 'sandbox-exec')
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

/**
 * One line for a source: the provider's summary filled in (e.g. `host:path`), or else its
 * first plain setting, e.g. the path or the repository.
 */
function summaryOf(of: SourceKind | null, settings: Record<string, unknown>): string | null {
  const template = of?.summary
  if (template) {
    return template.replace(/\{(\w+)\}/g, (_, key: string) => String(settings[key] ?? ''))
  }
  const value = Object.values(settings).find((v) => typeof v === 'string' && v)
  return typeof value === 'string' ? value : null
}
const summary = computed(() => {
  const own = summaryOf(storedKind.value, source.value?.settings ?? {})
  const hosting = source.value?.hosting
  if (!hosting) return own
  const hostingOf = kinds.value.find((k) => k.kind === hosting.kind) ?? null
  return [summaryOf(hostingOf, hosting.settings), own].filter(Boolean).join(' · ')
})
const isRepo = computed(
  () => !!storedKind.value?.provides.includes('repo') || !!source.value?.hosting,
)
const remoteHost = computed(() => {
  const settings = source.value?.settings ?? {}
  const host = settings.host ?? settings.address
  return typeof host === 'string' ? host : (summary.value ?? '')
})

async function edit() {
  problem.value = null
  editing.value = true
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

async function save(clear = false) {
  saving.value = true
  // Saving the source connects to it again, so it is saved only when it changed.
  problem.value =
    clear || !kind.value
      ? source.value
        ? await setSource(null)
        : null
      : sourceKey() !== opened.value
        ? await setSource({
            kind: kind.value,
            settings: { ...values() },
            hosting: hostingInput(),
          })
        : null
  saving.value = false
  // Saved: what is left is the conversation it opened for.
  if (!problem.value) {
    editing.value = false
    state.sourceOpen = false
  }
}

/** A long path cut in the middle: its start and the folder it ends in both stay readable. */
function middle(text: string, max = 44): string {
  if (text.length <= max) return text
  const tail = Math.ceil(max * 0.6)
  return `${text.slice(0, max - tail - 1)}…${text.slice(-tail)}`
}
</script>

<template>
  <DouPopover
    v-if="state.detail"
    v-model:open="state.sourceOpen"
    :title="t('project.source.title')"
    :width="360"
    placement="bottom-end"
  >
    <!-- Without a sandbox the button is the only sign left in view, so it turns to a warning. -->
    <DouButton
      variant="ghost"
      size="sm"
      :tone="unsandboxed ? 'warning' : undefined"
      :aria-label="t('project.source.title')"
      :title="summary ?? t('project.source.title')"
    >
      <ServerIcon v-if="source && remote" />
      <FolderGitIcon v-else-if="source && isRepo" />
      <FolderOpenIcon v-else />
    </DouButton>
    <template #content>
      <div class="source">
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
          <DouText v-if="summary" size="sm" :title="summary" class="source-path">
            {{ middle(summary) }}
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
          {{ t(sandboxNote) }}
        </DouAlert>
        <DouText v-else-if="weaker" variant="tertiary" size="sm">{{ t('sandbox.weaker') }}</DouText>
        <DouText v-else-if="source && remote" variant="tertiary" size="sm">
          {{ t('sandbox.remote', { host: remoteHost }) }}
        </DouText>
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.source {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.source-row {
  display: flex;
  align-items: center;
  gap: 8px;
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
