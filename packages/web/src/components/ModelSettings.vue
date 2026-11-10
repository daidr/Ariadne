<script setup lang="ts">
import {
  DouAlert,
  DouAutocomplete,
  DouButton,
  DouCard,
  DouDialog,
  DouEmpty,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputNumber,
  DouSelect,
  DouSwitch,
  DouTabPanel,
  DouTabs,
  DouText,
  DouTextarea,
  type AutocompleteOption,
} from 'dou-ui'
import { ChevronDownIcon, ChevronRightIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import {
  api,
  ApiError,
  type Concurrency,
  type Currency,
  type Effort,
  COMMON_EFFORTS,
  EFFORTS,
  effortWithin,
  type ListedModel,
  type ModelInfo,
  type ModelKind,
  type ModelProblem,
  type ModelSettings,
} from '../api'
import { applyModelSettings } from '../store'
import { lt, t, tm } from '../i18n'
import InputSettings from './InputSettings.vue'
import McpSettings from './McpSettings.vue'
import WorkSettings from './WorkSettings.vue'
import DataTab from './DataTab.vue'
import SearchSettings from './SearchSettings.vue'
import SettingsFields from './SettingsFields.vue'
import SecretPairs, { pairsInput, storedPairs, type DraftPair } from './SecretPairs.vue'
import { modelLabels } from '../model-label'

const open = defineModel<boolean>('open', { default: false })

interface DraftProvider {
  id: string
  kind: string
  name: string
  settings: Record<string, unknown>
  secretsSet: string[]
  headers: DraftPair[]
  /** The extra body fields as JSON text, checked on save. */
  extraBody: string
  timeoutSeconds: number | null
  /** The request options are shown; they start shown when any is set. */
  advanced: boolean
}

interface DraftModel {
  id: string
  provider: string
  model: string
  /** In thousands of tokens; empty uses the default. */
  contextK: number | null
  /** Whether it looks at images; off for text-only models. */
  images: boolean
  note: string
  maxTokens: number | null
  /** Per million tokens; a price needs input and output, the cache ones default to input. */
  currency: Currency
  priceInput: number | null
  priceOutput: number | null
  priceCacheRead: number | null
  priceCacheWrite: number | null
  /** Everything past its name is shown: filled in from the catalog, there to be corrected. */
  open: boolean
  looking: boolean
  /** Why the catalog could not fill it in, when the user asked it to. */
  lookError: string | null
  /** The name its settings were last filled in for; another name gets them filled anew. */
  described: string
  /** What the catalog last filled in, untouched since; `null` for the user's own values. */
  filled: string | null
  /** The efforts it accepts, as its service or the catalog said; `null` when unknown. */
  efforts: Effort[] | null
}

const kinds = ref<ModelKind[]>([])
const providers = ref<DraftProvider[]>([])
const models = ref<DraftModel[]>([])
// Threads come first: they do the work. The coordinator may use another model, and think less.
const threadModel = ref<string | null>(null)
const threadEffort = ref<Effort | ''>('')
const coordinatorEffort = ref<Effort | ''>('')
// `''` stands for "the threads'", which the select cannot hold as `null`.
const coordinatorModel = ref<string>('')
// Questions a conversation hands to a research; `''` is the asking conversation's own model.
const researchModel = ref<string>('')
// What a turn goes on with when its model keeps being turned away, for every role; '' is none.
const fallback = ref('')
// What the page no longer shows is saved back as it was: the triage role and how hard a
// research thinks.
const hiddenRoles = ref<Pick<ModelSettings['roles'], 'triage' | 'research'> | null>(null)
// Usage also shows the estimated cost.
const showCost = ref(false)
// What the scheduler may run at once; null until loaded, then saved with the models.
const concurrency = ref<Concurrency | null>(null)
const saving = ref(false)
const error = ref<string | null>(null)
// The models just saved that did not answer when asked, shown where each is set up.
const problems = ref<ModelProblem[]>([])
const problemOf = (model: DraftModel) =>
  problems.value.find((problem) => problem.model === model.id)?.problem ?? null

const kindOf = (kind: string) => kinds.value.find((k) => k.kind === kind)
const kindOptions = computed(() =>
  kinds.value.map((kind) => ({ value: kind.kind, label: lt(kind.title) ?? kind.kind })),
)
const titleOf = (kind: string) => {
  const known = kindOf(kind)
  return known ? (lt(known.title) ?? known.kind) : kind
}
// A provider is told by its type; a name is asked for only where that is not enough.
const named = (provider: DraftProvider) =>
  providers.value.filter((other) => other.kind === provider.kind).length > 1 ||
  (!!provider.name.trim() && provider.name !== titleOf(provider.kind))
const modelsOf = (provider: DraftProvider) =>
  models.value.filter((model) => model.provider === provider.id)
const modelOptions = computed(() =>
  modelLabels(
    models.value.filter((model) => model.model.trim()),
    providers.value,
  ).map(({ id, label }) => ({ value: id, label })),
)
// With one model there is no model to choose per role: both use it, and only how hard
// threads and the coordinator each think is left to choose.
const several = computed(() => modelOptions.value.length > 1)
const coordinatorOptions = computed(() => [
  { value: '', label: t('settings.sameAsThreads') },
  ...modelOptions.value,
])
const researchOptions = computed(() => [
  { value: '', label: t('settings.sameAsAsking') },
  ...modelOptions.value,
])
const fallbackOptions = computed(() => [
  { value: '', label: t('settings.fallbackNone') },
  ...modelOptions.value,
])
/** The efforts the model `id` accepts, as its service or the catalog said; `null` unknown. */
const toldOf = (id: string | null): readonly Effort[] | null =>
  models.value.find((model) => model.id === id)?.efforts ?? null
// The coordinator's model is the threads' unless it has its own.
const threadTold = computed(() => toldOf(threadModel.value ?? models.value[0]?.id ?? null))
const coordinatorTold = computed(() =>
  coordinatorModel.value ? toldOf(coordinatorModel.value) : threadTold.value,
)
/**
 * What may be picked: what the model accepts, the common ones when unknown, and then also
 * what is picked already (it may well take it; the server asks the nearest it does).
 */
const effortOptionsOf = (told: readonly Effort[] | null, picked: Effort | '') => [
  { value: '', label: t('settings.effort.default') },
  ...EFFORTS.filter(
    (effort) => (told ?? COMMON_EFFORTS).includes(effort) || (!told && effort === picked),
  ).map((effort) => ({ value: effort, label: t(`settings.effort.${effort}`) })),
]
const threadEffortOptions = computed(() => effortOptionsOf(threadTold.value, threadEffort.value))
const coordinatorEffortOptions = computed(() =>
  effortOptionsOf(coordinatorTold.value, coordinatorEffort.value),
)
// A model known not to take what was picked: the nearest it does.
watch([threadTold, threadEffort], ([told, effort]) => {
  if (told && effort && !told.includes(effort))
    threadEffort.value = effortWithin(effort, told) ?? ''
})
watch([coordinatorTold, coordinatorEffort], ([told, effort]) => {
  if (told && effort && !told.includes(effort)) {
    coordinatorEffort.value = effortWithin(effort, told) ?? ''
  }
})

function load(settings: ModelSettings) {
  applyModelSettings(settings)
  kinds.value = settings.kinds
  providers.value = settings.providers.map((provider) => {
    const extra = Object.keys(provider.extraBody).length
    return {
      id: provider.id,
      kind: provider.kind,
      name: provider.name,
      secretsSet: provider.secretsSet,
      settings: { ...provider.settings },
      headers: storedPairs(provider.headers),
      extraBody: extra ? JSON.stringify(provider.extraBody, null, 2) : '',
      timeoutSeconds: provider.timeoutSeconds,
      advanced: !!(extra || provider.headers.length || provider.timeoutSeconds),
    }
  })
  models.value = settings.models.map((model) => ({
    id: model.id,
    provider: model.provider,
    model: model.model,
    contextK: model.contextTokens ? Math.round(model.contextTokens / 1000) : null,
    images: model.images !== false,
    note: model.note ?? '',
    maxTokens: model.maxTokens ?? null,
    currency: model.price?.currency ?? 'USD',
    priceInput: model.price?.input ?? null,
    priceOutput: model.price?.output ?? null,
    priceCacheRead: model.price?.cacheRead ?? null,
    priceCacheWrite: model.price?.cacheWrite ?? null,
    open: false,
    looking: false,
    lookError: null,
    described: model.model,
    filled: null,
    efforts: model.efforts ?? null,
  }))
  showCost.value = settings.showCost
  threadModel.value = settings.roles.thread.model
  threadEffort.value = settings.roles.thread.effort ?? ''
  coordinatorModel.value = settings.roles.coordinator.model ?? ''
  coordinatorEffort.value = settings.roles.coordinator.effort ?? ''
  researchModel.value = settings.roles.research.model ?? ''
  fallback.value = settings.roles.thread.fallback ?? settings.roles.coordinator.fallback ?? ''
  hiddenRoles.value = { triage: settings.roles.triage, research: settings.roles.research }
  learnEfforts()
}

/**
 * Models saved before their efforts were known learn them from what their provider lists,
 * so the effort offered is what each takes.
 */
function learnEfforts() {
  for (const provider of providers.value) {
    const untold = modelsOf(provider).filter((model) => model.efforts === null)
    if (!untold.length) continue
    void listed(provider).then((all) => {
      for (const model of untold) {
        const efforts = all.find((entry) => entry.id === model.model.trim())?.info?.efforts
        if (efforts && model.efforts === null) model.efforts = efforts
      }
    })
  }
}

/** Some model has a price, so usage can show what it cost. */
const priced = computed(() =>
  models.value.some((model) => model.priceInput !== null && model.priceOutput !== null),
)

export type SettingsTab = 'models' | 'instructions' | 'commands' | 'input' | 'mcp' | 'data'
type WorkPart = 'instructions' | 'commands'
const tab = defineModel<SettingsTab>('tab', { default: 'models' })
const mcp = ref<InstanceType<typeof McpSettings> | null>(null)
const input = ref<InstanceType<typeof InputSettings> | null>(null)
const search = ref<InstanceType<typeof SearchSettings> | null>(null)
const work = ref<Partial<Record<WorkPart, InstanceType<typeof WorkSettings> | null>>>({})
const WORK_PARTS: WorkPart[] = ['instructions', 'commands']

/** Reload the protocols only: a plugin added or removed changes them, not the draft. */
async function reloadKinds() {
  try {
    kinds.value = (await api.getModelSettings()).kinds
  } catch {
    // The models tab shows the protocols it had.
  }
}

watch(open, async (isOpen) => {
  if (!isOpen) return
  error.value = null
  problems.value = []
  // Not loaded yet: saving now would send an empty list over the saved one.
  hiddenRoles.value = null
  try {
    const [saved, slots] = await Promise.all([api.getModelSettings(), api.getConcurrency()])
    load(saved)
    concurrency.value = slots
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
})

const newId = (prefix: string) => `${prefix}_${Date.now().toString(36)}`

function addProvider() {
  const kind = kinds.value[0]
  if (!kind) return
  const id = newId('p')
  providers.value.push({
    id,
    kind: kind.kind,
    name: lt(kind.title) ?? kind.kind,
    settings: {},
    secretsSet: [],
    headers: [],
    extraBody: '',
    timeoutSeconds: null,
    advanced: false,
  })
  addModel(id)
}

/** A new type brings its own settings; a name still the old type's follows the new one. */
function setKind(provider: (typeof providers.value)[number], kind: string) {
  const before = kindOf(provider.kind)
  const after = kindOf(kind)
  const oldName = before ? (lt(before.title) ?? before.kind) : provider.kind
  if (!provider.name.trim() || provider.name === oldName) {
    provider.name = after ? (lt(after.title) ?? after.kind) : kind
  }
  provider.kind = kind
  provider.secretsSet = []
  for (const model of modelsOf(provider)) presetModel(model)
}

/** An agent program runs the model it is itself set to, unless told another. */
function presetModel(model: DraftModel) {
  const provider = providers.value.find((p) => p.id === model.provider)
  if (provider && kindOf(provider.kind)?.program && !model.model.trim()) model.model = 'default'
}

function removeProvider(id: string) {
  providers.value = providers.value.filter((provider) => provider.id !== id)
  // Its models go with it.
  for (const model of models.value.filter((m) => m.provider === id)) removeModel(model.id)
}

function addModel(provider: string) {
  const id = newId('m')
  models.value.push({
    id,
    provider,
    model: '',
    contextK: null,
    images: true,
    note: '',
    maxTokens: null,
    currency: 'USD',
    priceInput: null,
    priceOutput: null,
    priceCacheRead: null,
    priceCacheWrite: null,
    open: false,
    looking: false,
    lookError: null,
    described: '',
    filled: null,
    efforts: null,
  })
  presetModel(models.value.at(-1)!)
  threadModel.value ??= id
}

function removeModel(id: string) {
  models.value = models.value.filter((model) => model.id !== id)
  if (threadModel.value === id) threadModel.value = models.value[0]?.id ?? null
  if (coordinatorModel.value === id) coordinatorModel.value = ''
  if (researchModel.value === id) researchModel.value = ''
}

const currencyOptions = [
  { value: 'USD', label: 'USD $' },
  { value: 'CNY', label: 'CNY ¥' },
]

/** A provider's kind and what is typed for it, to tell services apart in the catalog. */
const hintOf = (provider: DraftProvider | undefined) =>
  [provider?.kind, ...Object.values(provider?.settings ?? {})]
    .filter((value) => typeof value === 'string')
    .join(' ')

// What each provider lists, asked once for what is typed into it: its address and key.
const listings = new Map<string, { key: string; models: Promise<ListedModel[]> }>()
// Why a provider's models could not be listed, shown under the name being typed.
const listProblems = ref<Record<string, string>>({})

function listed(provider: DraftProvider): Promise<ListedModel[]> {
  const key = JSON.stringify([provider.kind, provider.settings, provider.secretsSet])
  const known = listings.get(provider.id)
  if (known?.key === key) return known.models
  const asked = api
    .listModels({ id: provider.id, kind: provider.kind, settings: provider.settings })
    .then(
      (result) => {
        delete listProblems.value[provider.id]
        return result.models ?? []
      },
      (cause) => {
        // Not filled in far enough to ask (no address, no key) is no problem to show.
        if (cause instanceof ApiError && cause.status === 502) {
          listProblems.value[provider.id] = tm(cause.localized)
        } else delete listProblems.value[provider.id]
        // Asked again once the form changes, or the name is focused again.
        if (listings.get(provider.id)?.models === asked) listings.delete(provider.id)
        return []
      },
    )
  listings.set(provider.id, { key, models: asked })
  return asked
}

/**
 * The provider's models for what is typed, by name or by what the service calls them; all
 * of them once a whole name stands there.
 */
async function suggest(provider: DraftProvider, typed: string): Promise<AutocompleteOption[]> {
  const all = await listed(provider)
  const text = typed.trim().toLowerCase()
  const whole = all.some((model) => model.id.toLowerCase() === text)
  const matches = (model: ListedModel) =>
    model.id.toLowerCase().includes(text) || !!model.title?.toLowerCase().includes(text)
  return all
    .filter((model) => whole || !text || matches(model))
    .map((model) => ({ value: model.id, ...(model.title ? { description: model.title } : {}) }))
}

/** The settings a catalog entry fills in, to tell later whether the user changed them. */
const filledOf = (model: DraftModel) =>
  JSON.stringify([
    model.contextK,
    model.images,
    model.maxTokens,
    model.currency,
    model.priceInput,
    model.priceOutput,
    model.priceCacheRead,
    model.priceCacheWrite,
  ])

function fill(model: DraftModel, info: ModelInfo | null) {
  model.efforts = info?.efforts ?? null
  model.contextK = info?.contextTokens ? Math.floor(info.contextTokens / 1000) : null
  model.images = info?.images !== false
  model.maxTokens = info?.maxTokens ?? null
  model.currency = info?.price?.currency ?? 'USD'
  model.priceInput = info?.price?.input ?? null
  model.priceOutput = info?.price?.output ?? null
  model.priceCacheRead = info?.price?.cacheRead ?? null
  model.priceCacheWrite = info?.price?.cacheWrite ?? null
  model.filled = filledOf(model)
}

/**
 * A name nobody knows: what the catalog filled in for the name before goes, since it was
 * another model's; what the user typed in themselves stays.
 */
function unknown(model: DraftModel) {
  model.efforts = null
  if (model.filled !== null && model.filled === filledOf(model)) fill(model, null)
  model.filled = null
}

// Models being filled in from the catalog; saving waits for them.
const describing = new Set<Promise<void>>()

/**
 * Fill a model's settings in for its name, once per name: how much it reads and writes,
 * whether it looks at images, what it costs. From what its provider listed, else from the
 * catalog by the name alone; a name nobody knows keeps what the user typed in. `again` asks
 * anew for the same name, and says why when nothing comes back.
 */
function describe(model: DraftModel, again = false): Promise<void> {
  const name = model.model.trim()
  if (!again && name === model.described) return Promise.resolve()
  model.described = name
  model.lookError = null
  if (!name) {
    unknown(model)
    return Promise.resolve()
  }
  const provider = providers.value.find((p) => p.id === model.provider)
  model.looking = true
  const filling = (async () => {
    let info: ModelInfo | null = null
    try {
      const known = provider
        ? (await listed(provider)).find((entry) => entry.id === name)?.info
        : undefined
      info = known ?? (await api.modelInfo(name, hintOf(provider))).info
    } catch (cause) {
      if (again) model.lookError = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    }
    // Typed on meanwhile: the newer name is described by its own call.
    if (model.described !== name) return
    if (info) fill(model, info)
    else if (!again) unknown(model)
  })().finally(() => {
    model.looking = false
    describing.delete(filling)
  })
  describing.add(filling)
  return filling
}

/** Forget a stored secret: saving sends `null`, which the server treats as "clear". */
function clearSecret(provider: DraftProvider, key: string) {
  provider.settings[key] = null
  provider.secretsSet = provider.secretsSet.filter((name) => name !== key)
}

/**
 * Save every tab at once; a tab never opened has nothing changed. A tab that turns its part
 * down keeps the dialog open, on that tab. Saved MCP servers then connect, so the dialog
 * stays open on MCP too: their tools show up to be marked.
 */
async function saveAll() {
  saving.value = true
  try {
    const parts: [SettingsTab, (() => Promise<boolean>) | undefined][] = [
      ['models', saveModels],
      ['models', search.value?.save],
      ['instructions', work.value.instructions?.save],
      ['commands', work.value.commands?.save],
      ['input', input.value?.save],
      ['mcp', mcp.value?.save],
    ]
    for (const [part, save] of parts) {
      if (save && !(await save())) {
        tab.value = part
        return
      }
    }
    // A model that did not answer is saved all the same, and shown to be put right.
    if (problems.value.length) tab.value = 'models'
    else if (tab.value !== 'mcp') open.value = false
  } finally {
    saving.value = false
  }
}

async function saveModels(): Promise<boolean> {
  if (!hiddenRoles.value) return true
  error.value = null
  try {
    // A name typed and saved at once has its settings filled in first.
    for (const model of models.value) void describe(model)
    await Promise.all(describing)
    // A model without a name is left out, and so is a role that pointed at it.
    const kept = models.value.filter((model) => model.model.trim())
    const keep = (id: string | null) => (id && kept.some((m) => m.id === id) ? id : null)
    const hidden = hiddenRoles.value
    const bodies = providers.value.map((provider) => {
      const text = provider.extraBody.trim()
      if (!text) return {}
      let body: unknown
      try {
        body = JSON.parse(text)
      } catch {
        body = null
      }
      if (!body || typeof body !== 'object' || Array.isArray(body)) {
        throw new Error(t('settings.extraBodyInvalid', { name: provider.name || provider.id }))
      }
      return body as Record<string, unknown>
    })
    const saved = await api.saveModelSettings({
      providers: providers.value.map((provider, at) => ({
        id: provider.id,
        kind: provider.kind,
        name: provider.name,
        settings: provider.settings,
        headers: pairsInput(provider.headers),
        extraBody: bodies[at]!,
        timeoutSeconds: provider.timeoutSeconds || null,
      })),
      models: kept.map((model) => ({
        id: model.id,
        provider: model.provider,
        model: model.model.trim(),
        ...(model.contextK ? { contextTokens: model.contextK * 1000 } : {}),
        ...(model.images ? {} : { images: false as const }),
        ...(model.note.trim() ? { note: model.note.trim() } : {}),
        ...(model.maxTokens ? { maxTokens: model.maxTokens } : {}),
        ...(model.efforts ? { efforts: model.efforts } : {}),
        ...(model.priceInput !== null && model.priceOutput !== null
          ? {
              price: {
                currency: model.currency,
                input: model.priceInput,
                output: model.priceOutput,
                ...(model.priceCacheRead !== null ? { cacheRead: model.priceCacheRead } : {}),
                ...(model.priceCacheWrite !== null ? { cacheWrite: model.priceCacheWrite } : {}),
              },
            }
          : {}),
      })),
      showCost: showCost.value,
      roles: {
        thread: {
          model: keep(threadModel.value) ?? kept[0]?.id ?? null,
          effort: threadEffort.value || null,
          fallback: keep(fallback.value || null),
        },
        coordinator: {
          model: keep(coordinatorModel.value || null),
          effort: coordinatorEffort.value || null,
          fallback: keep(fallback.value || null),
        },
        triage: {
          model: keep(hidden?.triage.model ?? null),
          effort: hidden?.triage.effort ?? null,
          fallback: keep(hidden?.triage.fallback ?? null),
        },
        research: {
          model: keep(researchModel.value || null),
          effort: hidden?.research.effort ?? null,
          fallback: null,
        },
      },
    })
    load(saved)
    problems.value = saved.problems
    if (concurrency.value) concurrency.value = await api.saveConcurrency(concurrency.value)
    return true
  } catch (cause) {
    error.value =
      cause instanceof ApiError
        ? tm(cause.localized)
        : cause instanceof Error
          ? cause.message
          : String(cause)
    tab.value = 'models'
    return false
  }
}
</script>

<template>
  <DouDialog v-model:open="open" :title="t('settings.title')" :width="760">
    <DouTabs v-model="tab" placement="start" :height="560" class="settings-tabs">
      <DouTabPanel value="models" :label="t('settings.tab.models')">
        <!-- A provider with its models, then who uses which. -->
        <div class="settings">
          <section class="settings-section">
            <div class="settings-head">
              <DouText weight="medium">{{ t('settings.providers') }}</DouText>
              <DouButton size="sm" variant="ghost" :disabled="!kinds.length" @click="addProvider">
                <template #leading><PlusIcon /></template>
                {{ t('settings.add') }}
              </DouButton>
            </div>
            <DouEmpty v-if="!providers.length" size="sm" :description="t('settings.noProviders')" />
            <DouCard v-for="provider in providers" :key="provider.id" padding="sm">
              <DouForm layout="vertical" size="sm">
                <div class="settings-row">
                  <DouFormItem :label="t('settings.kind')" class="settings-grow">
                    <DouSelect
                      :model-value="provider.kind"
                      :options="kindOptions"
                      @update:model-value="(value) => setKind(provider, String(value))"
                    />
                  </DouFormItem>
                  <DouFormItem
                    v-if="named(provider)"
                    :label="t('settings.name')"
                    class="settings-grow"
                  >
                    <DouInput v-model="provider.name" />
                  </DouFormItem>
                  <DouButton
                    size="sm"
                    variant="ghost"
                    tone="danger"
                    class="settings-remove"
                    :aria-label="t('settings.remove')"
                    :title="t('settings.remove')"
                    @click="removeProvider(provider.id)"
                  >
                    <TrashIcon />
                  </DouButton>
                </div>
                <SettingsFields
                  v-if="kindOf(provider.kind)"
                  :schema="kindOf(provider.kind)!.settings"
                  :values="provider.settings"
                  :secrets-set="provider.secretsSet"
                  @clear-secret="(key) => clearSecret(provider, key)"
                />
                <DouFormItem :label="t('settings.models')">
                  <div class="settings-models">
                    <div v-for="model in modelsOf(provider)" :key="model.id" class="settings-model">
                      <div class="settings-row" @focusout="describe(model)">
                        <DouAutocomplete
                          v-model="model.model"
                          :fetch="(typed: string) => suggest(provider, typed)"
                          :highlight="false"
                          :placeholder="t('settings.modelName')"
                          :aria-label="t('settings.modelName')"
                          spellcheck="false"
                          class="settings-grow"
                          @select="describe(model)"
                        >
                          <template v-if="listProblems[provider.id]" #empty>
                            <DouText size="sm" tone="danger">{{
                              listProblems[provider.id]
                            }}</DouText>
                          </template>
                        </DouAutocomplete>
                        <DouButton
                          size="sm"
                          variant="ghost"
                          :aria-expanded="model.open"
                          :aria-label="t('settings.modelMore')"
                          :title="t('settings.modelMore')"
                          @click="model.open = !model.open"
                        >
                          <ChevronDownIcon v-if="model.open" />
                          <ChevronRightIcon v-else />
                        </DouButton>
                        <DouButton
                          size="sm"
                          variant="ghost"
                          tone="danger"
                          :aria-label="t('settings.remove')"
                          :title="t('settings.remove')"
                          @click="removeModel(model.id)"
                        >
                          <TrashIcon />
                        </DouButton>
                      </div>
                      <DouText v-if="problemOf(model)" tone="danger" size="sm">
                        {{ tm(problemOf(model)!) }}
                      </DouText>
                      <div v-if="model.open" class="settings-model-more">
                        <div class="settings-row">
                          <DouFormItem :label="t('settings.contextTokens')" class="settings-narrow">
                            <DouInputNumber
                              v-model="model.contextK"
                              :min="8"
                              :max="10000"
                              :step="8"
                              placeholder="128"
                            />
                          </DouFormItem>
                          <DouFormItem :label="t('settings.maxTokens')" class="settings-narrow">
                            <DouInputNumber
                              v-model="model.maxTokens"
                              :min="256"
                              :max="1000000"
                              :step="1024"
                              :placeholder="t('settings.maxTokensDefault')"
                            />
                          </DouFormItem>
                          <DouFormItem :label="t('settings.modelImages')">
                            <DouSwitch v-model="model.images" />
                          </DouFormItem>
                        </div>
                        <DouFormItem v-if="several" :label="t('settings.modelNote')">
                          <DouInput
                            v-model="model.note"
                            :maxlength="200"
                            :placeholder="t('settings.modelNotePlaceholder')"
                          />
                        </DouFormItem>
                        <div class="settings-row">
                          <DouFormItem :label="t('settings.currency')" class="settings-currency">
                            <DouSelect v-model="model.currency" :options="currencyOptions" />
                          </DouFormItem>
                          <DouFormItem :label="t('usage.input')" class="settings-grow">
                            <DouInputNumber v-model="model.priceInput" :min="0" :step="0.1" />
                          </DouFormItem>
                          <DouFormItem :label="t('usage.output')" class="settings-grow">
                            <DouInputNumber v-model="model.priceOutput" :min="0" :step="0.1" />
                          </DouFormItem>
                          <DouFormItem :label="t('usage.cacheRead')" class="settings-grow">
                            <DouInputNumber
                              v-model="model.priceCacheRead"
                              :min="0"
                              :step="0.01"
                              :placeholder="t('settings.sameAsInput')"
                            />
                          </DouFormItem>
                          <DouFormItem :label="t('usage.cacheWrite')" class="settings-grow">
                            <DouInputNumber
                              v-model="model.priceCacheWrite"
                              :min="0"
                              :step="0.1"
                              :placeholder="t('settings.sameAsInput')"
                            />
                          </DouFormItem>
                        </div>
                        <div class="settings-price-actions">
                          <DouButton
                            size="sm"
                            variant="outline"
                            :disabled="!model.model.trim()"
                            :loading="model.looking"
                            @click="describe(model, true)"
                          >
                            {{ t('settings.priceFill') }}
                          </DouButton>
                          <DouText v-if="model.lookError" tone="danger" size="sm">
                            {{ model.lookError }}
                          </DouText>
                        </div>
                      </div>
                    </div>
                    <DouButton
                      size="sm"
                      variant="ghost"
                      class="settings-more"
                      @click="addModel(provider.id)"
                    >
                      <template #leading><PlusIcon /></template>
                      {{ t('settings.addModel') }}
                    </DouButton>
                  </div>
                </DouFormItem>
                <DouButton
                  v-if="!kindOf(provider.kind)?.program"
                  size="sm"
                  variant="ghost"
                  class="settings-more"
                  :aria-expanded="provider.advanced"
                  @click="provider.advanced = !provider.advanced"
                >
                  <template #leading>
                    <ChevronDownIcon v-if="provider.advanced" />
                    <ChevronRightIcon v-else />
                  </template>
                  {{ t('settings.request') }}
                </DouButton>
                <template v-if="provider.advanced && !kindOf(provider.kind)?.program">
                  <DouFormItem :label="t('settings.headers')">
                    <SecretPairs v-model="provider.headers" :add-label="t('mcp.addHeader')" />
                  </DouFormItem>
                  <DouFormItem :label="t('settings.extraBody')">
                    <DouTextarea
                      v-model="provider.extraBody"
                      :autosize="{ minRows: 2, maxRows: 10 }"
                      placeholder='{ "temperature": 0.2 }'
                      class="settings-code"
                    />
                  </DouFormItem>
                  <DouFormItem :label="t('settings.timeoutSeconds')" class="settings-narrow">
                    <DouInputNumber
                      v-model="provider.timeoutSeconds"
                      :min="10"
                      :max="3600"
                      :step="10"
                      :placeholder="t('settings.timeoutNone')"
                    />
                  </DouFormItem>
                </template>
              </DouForm>
            </DouCard>
            <div v-if="priced" class="settings-switch">
              <span>{{ t('settings.showCost') }}</span>
              <DouSwitch v-model="showCost" />
            </div>
          </section>

          <section v-if="modelOptions.length || concurrency" class="settings-section">
            <DouText weight="medium">{{ t('settings.part.roles') }}</DouText>
            <DouForm layout="vertical" size="sm">
              <div
                v-if="modelOptions.length === 1 && threadTold?.length !== 0"
                class="settings-row"
              >
                <DouFormItem :label="t('settings.threadEffort')" class="settings-grow">
                  <DouSelect v-model="threadEffort" :options="threadEffortOptions" />
                </DouFormItem>
                <DouFormItem :label="t('settings.coordinatorEffort')" class="settings-grow">
                  <DouSelect v-model="coordinatorEffort" :options="coordinatorEffortOptions" />
                </DouFormItem>
              </div>
              <template v-else-if="several">
                <div class="settings-row">
                  <DouFormItem :label="t('settings.threadModel')" class="settings-grow">
                    <DouSelect v-model="threadModel" :options="modelOptions" />
                  </DouFormItem>
                  <DouFormItem
                    v-if="threadTold?.length !== 0"
                    :label="t('settings.effort')"
                    class="settings-effort"
                  >
                    <DouSelect v-model="threadEffort" :options="threadEffortOptions" />
                  </DouFormItem>
                </div>
                <div class="settings-row">
                  <DouFormItem :label="t('settings.coordinatorModel')" class="settings-grow">
                    <DouSelect v-model="coordinatorModel" :options="coordinatorOptions" />
                  </DouFormItem>
                  <DouFormItem
                    v-if="coordinatorTold?.length !== 0"
                    :label="t('settings.effort')"
                    class="settings-effort"
                  >
                    <DouSelect v-model="coordinatorEffort" :options="coordinatorEffortOptions" />
                  </DouFormItem>
                </div>
                <div class="settings-row">
                  <DouFormItem
                    :label="t('settings.researchModel')"
                    :help="t('settings.researchModelHelp')"
                    class="settings-grow"
                  >
                    <DouSelect v-model="researchModel" :options="researchOptions" />
                  </DouFormItem>
                  <DouFormItem
                    :label="t('settings.fallbackModel')"
                    :help="t('settings.fallbackModelHelp')"
                    class="settings-grow"
                  >
                    <DouSelect v-model="fallback" :options="fallbackOptions" />
                  </DouFormItem>
                </div>
              </template>
              <DouFormItem v-if="concurrency" :label="t('settings.slots')" class="settings-narrow">
                <DouInputNumber v-model="concurrency.slots" :min="1" :max="16" :step="1" />
              </DouFormItem>
            </DouForm>
          </section>
          <SearchSettings v-if="open" ref="search" />
          <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
        </div>
      </DouTabPanel>
      <DouTabPanel
        v-for="part in WORK_PARTS"
        :key="part"
        :value="part"
        :label="t(`settings.tab.${part}`)"
      >
        <WorkSettings
          v-if="open"
          :ref="(el) => (work[part] = el as InstanceType<typeof WorkSettings> | null)"
          :part="part"
        />
      </DouTabPanel>
      <DouTabPanel value="input" :label="t('settings.tab.input')">
        <InputSettings v-if="open" ref="input" />
      </DouTabPanel>
      <DouTabPanel value="mcp" :label="t('settings.tab.mcp')">
        <McpSettings v-if="open" ref="mcp" />
      </DouTabPanel>
      <DouTabPanel value="data" :label="t('settings.tab.data')">
        <DataTab v-if="open" @plugins-changed="reloadKinds" />
      </DouTabPanel>
    </DouTabs>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
      <DouButton tone="primary" :loading="saving" @click="saveAll">
        {{ t('settings.save') }}
      </DouButton>
    </template>
  </DouDialog>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.settings-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.settings-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.settings-row {
  display: flex;
  gap: 12px;
}

.settings-grow {
  flex: 1;
  min-width: 0;
}

.settings-effort {
  width: 120px;
}

.settings-currency {
  flex: none;
  width: 100px;
}

.settings-price-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.settings-models {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}

.settings-model {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.settings-model-more {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-block: 4px 8px;
}

.settings-narrow {
  flex: none;
  width: 140px;
}

.settings-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.settings-switch-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.settings-more {
  align-self: flex-start;
}

.settings-code :deep(textarea),
.settings-code :deep(input) {
  font-family: var(--dou-font-family-mono);
}

.settings-remove {
  flex: none;
  align-self: flex-start;
}
</style>
