<script setup lang="ts">
import {
  DouAlert,
  DouAutocomplete,
  DouButton,
  DouCard,
  DouCascader,
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
  useBreakpoints,
  type AutocompleteOption,
  type CascaderOption,
  type CascaderValue,
} from 'dou-ui'
import {
  ChevronDownIcon,
  ChevronRightIcon,
  CpuIcon,
  DatabaseIcon,
  GlobeIcon,
  McpServerIcon,
  PlugIcon,
  PlusIcon,
  RefreshIcon,
  ScrollTextIcon,
  SlidersIcon,
  TerminalIcon,
  TrashIcon,
} from '@dou-ui/icons'
import { computed, ref, watch, type Component, type ComputedRef, type Ref } from 'vue'
import {
  api,
  ApiError,
  type Concurrency,
  type CoordinatorEffort,
  type Currency,
  type Effort,
  type ListedModel,
  type ModelInfo,
  type ModelKind,
  type ModelProblem,
  type ModelSettings,
} from '../api'
import { applyModelSettings, refreshConversationModels } from '../store'
import { coordinatorDefaultEffort, EFFORTS, effortsOf, effortWithin } from '../effort'
import { lt, t, tm } from '../i18n'
import InputSettings from './InputSettings.vue'
import McpSettings from './McpSettings.vue'
import WorkSettings from './WorkSettings.vue'
import DataTab from './DataTab.vue'
import PluginSettings from './PluginSettings.vue'
import SearchSettings from './SearchSettings.vue'
import UpdatesSettings from './UpdatesSettings.vue'
import SettingsFields from './SettingsFields.vue'
import SettingsLabelHelp from './SettingsLabelHelp.vue'
import SecretPairs, { pairsInput, storedPairs, type DraftPair } from './SecretPairs.vue'
import { modelLabels } from '../model-label'
import { resolveWorkspaceLayout, type WorkspaceLayoutMode } from '../workspace-layout'

const props = defineProps<{ layoutMode?: WorkspaceLayoutMode }>()
const open = defineModel<boolean>('open', { default: false })
const breakpoints = useBreakpoints()
const mode = computed<WorkspaceLayoutMode>(
  () => props.layoutMode ?? resolveWorkspaceLayout(breakpoints),
)
const isNarrow = computed(() => mode.value === 'narrow')
const fullscreen = computed(() => mode.value !== 'wide')
const tabsPlacement = computed(() => (isNarrow.value ? 'top' : 'start'))
const tabsHeight = computed(() => (fullscreen.value ? '100%' : 560))

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
const threadModel = ref('')
const threadEffort = ref<Effort | ''>('')
const coordinatorEffort = ref<CoordinatorEffort | ''>('')
// An empty value means the coordinator inherits the discussion thread's model.
const coordinatorModel = ref('')
// Questions a conversation hands to a research; `''` is the asking conversation's own model.
const researchModel = ref<string>('')
const researchEffort = ref<Effort | ''>('')
// What the page no longer shows is saved back as it was: the triage role.
const hiddenRoles = ref<Pick<ModelSettings['roles'], 'triage'> | null>(null)
// Usage also shows the estimated cost.
const showCost = ref(false)
// What the scheduler may run at once; null until loaded, then saved with the models.
const concurrency = ref<Concurrency | null>(null)
const saving = ref(false)
const error = ref<string | null>(null)
// The models just saved that did not answer when asked, shown where each is set up.
const problems = ref<ModelProblem[]>([])
let savedModelDraft = ''
function modelDraft() {
  return JSON.stringify([
    providers.value.map((provider) => [
      provider.id,
      provider.kind,
      provider.name,
      provider.settings,
      provider.headers,
      provider.extraBody,
      provider.timeoutSeconds,
    ]),
    models.value.map((model) => [
      model.id,
      model.provider,
      model.model,
      model.note,
      model.images,
      model.efforts,
      filledOf(model),
    ]),
    threadModel.value,
    threadEffort.value,
    coordinatorModel.value,
    coordinatorEffort.value,
    researchModel.value,
    researchEffort.value,
    hiddenRoles.value,
    showCost.value,
    concurrency.value,
  ])
}
const problemOf = (model: DraftModel) =>
  problems.value.find((problem) => problem.model === model.id)?.problem ?? null

const kindOf = (kind: string) => kinds.value.find((k) => k.kind === kind)
const kindOptions = computed(() =>
  kinds.value.map((kind) => ({ value: kind.kind, label: lt(kind.title) ?? kind.kind })),
)
const kindOptionsFor = (current: string) =>
  kindOf(current)
    ? kindOptions.value
    : [{ value: current, label: current, disabled: true }, ...kindOptions.value]
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
const several = computed(() => modelOptions.value.length > 1)
/** The efforts the model `id` accepts, as its service or the catalog said; `null` unknown. */
const toldOf = (id: string | undefined): readonly Effort[] | null =>
  models.value.find((model) => model.id === id)?.efforts ?? null
// The coordinator's model is the threads' unless it has its own.
const threadTold = computed(() =>
  toldOf(
    (modelOptions.value.find((model) => model.value === threadModel.value) ?? modelOptions.value[0])
      ?.value,
  ),
)
const coordinatorTold = computed(() =>
  modelOptions.value.some((model) => model.value === coordinatorModel.value)
    ? toldOf(coordinatorModel.value)
    : threadTold.value,
)
// When research inherits the asking conversation's model, any effort accepted by a configured model
// can be asked for; the server clamps to the nearest effort the asking model supports.
const askingTold = computed<readonly Effort[]>(() => {
  const accepted = new Set<Effort>()
  for (const model of models.value) {
    if (!model.model.trim()) continue
    for (const effort of effortsOf(model.efforts)) accepted.add(effort)
  }
  return EFFORTS.filter((effort) => accepted.has(effort))
})
const researchTold = computed<readonly Effort[] | null>(() =>
  modelOptions.value.some((model) => model.value === researchModel.value)
    ? toldOf(researchModel.value)
    : askingTold.value,
)
function modelBranch(option: CascaderOption, told: readonly Effort[] | null): CascaderOption {
  const efforts = effortsOf(told)
  if (!efforts.length) return option
  return {
    ...option,
    children: [
      { value: '', label: t('settings.effort.default') },
      ...efforts.map((effort) => ({ value: effort, label: t(`settings.effort.${effort}`) })),
    ],
  }
}
const threadOptions = computed(() =>
  modelOptions.value.map((model) => modelBranch(model, toldOf(model.value))),
)
const coordinatorOptions = computed(() => [
  modelBranch({ value: '', label: t('settings.sameAsThreads') }, threadTold.value),
  ...threadOptions.value,
])
const researchOptions = computed(() => [
  modelBranch({ value: '', label: t('settings.sameAsAsking') }, askingTold.value),
  ...threadOptions.value,
])

// Keep the cascader's path in sync with the separate fields stored in settings.
function rolePath(
  options: ComputedRef<CascaderOption[]>,
  model: Ref<string>,
  effort: Ref<Effort | ''>,
) {
  return computed({
    get() {
      const current =
        options.value.find((option) => option.value === model.value) ?? options.value[0]
      if (!current) return null
      const depth =
        current.children?.find((child) => child.value === effort.value) ?? current.children?.[0]
      return depth ? [current.value, depth.value] : [current.value]
    },
    set(path: CascaderValue[] | CascaderValue[][] | null) {
      const [id, depth] = path ?? []
      if (typeof id !== 'string') return
      model.value = id
      effort.value = EFFORTS.find((value) => value === depth) ?? ''
    },
  })
}
const threadPath = rolePath(threadOptions, threadModel, threadEffort)
const coordinatorPath = computed({
  get() {
    const current =
      coordinatorOptions.value.find((option) => option.value === coordinatorModel.value) ??
      coordinatorOptions.value[0]
    if (!current) return null
    const wanted =
      current.value === ''
        ? coordinatorEffort.value === 'default'
          ? ''
          : coordinatorEffort.value || (coordinatorDefaultEffort(threadTold.value) ?? '')
        : coordinatorEffort.value === 'default'
          ? ''
          : coordinatorEffort.value
    const depth = current.children?.find((child) => child.value === wanted) ?? current.children?.[0]
    return depth ? [current.value, depth.value] : [current.value]
  },
  set(path: CascaderValue[] | CascaderValue[][] | null) {
    const [id, depth] = path ?? []
    if (typeof id !== 'string') return
    coordinatorModel.value = id
    const explicit = EFFORTS.find((value) => value === depth)
    if (explicit) {
      coordinatorEffort.value = explicit
    } else if (id === '' && depth === '') {
      coordinatorEffort.value = 'default'
    } else {
      coordinatorEffort.value = ''
    }
  },
})
const researchPath = rolePath(researchOptions, researchModel, researchEffort)
// A model that does not take what was picked: the nearest it does, as the server asks.
watch([threadTold, threadEffort], ([told, effort]) => {
  const accepted = effortsOf(told)
  if (effort && !accepted.includes(effort))
    threadEffort.value = effortWithin(effort, accepted) ?? ''
})
watch([coordinatorTold, coordinatorEffort], ([told, effort]) => {
  const accepted = effortsOf(told)
  if (effort && effort !== 'default' && !accepted.includes(effort)) {
    coordinatorEffort.value = effortWithin(effort, accepted) ?? ''
  }
})
watch([researchTold, researchEffort], ([told, effort]) => {
  const accepted = effortsOf(told)
  if (effort && !accepted.includes(effort)) {
    researchEffort.value = effortWithin(effort, accepted) ?? ''
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
  threadModel.value = settings.roles.thread.model ?? ''
  threadEffort.value = settings.roles.thread.effort ?? ''
  coordinatorModel.value = settings.roles.coordinator.model ?? ''
  coordinatorEffort.value = settings.roles.coordinator.effort ?? ''
  researchModel.value = settings.roles.research.model ?? ''
  researchEffort.value = settings.roles.research.effort ?? ''
  hiddenRoles.value = { triage: settings.roles.triage }
}

/** Some model has a price, so usage can show what it cost. */
const priced = computed(() =>
  models.value.some((model) => model.priceInput !== null && model.priceOutput !== null),
)

export type SettingsTab =
  | 'models'
  | 'search'
  | 'instructions'
  | 'commands'
  | 'input'
  | 'mcp'
  | 'plugins'
  | 'data'
  | 'updates'
type WorkPart = 'instructions' | 'commands'
const tab = defineModel<SettingsTab>('tab', { default: 'models' })
const mcp = ref<InstanceType<typeof McpSettings> | null>(null)
const input = ref<InstanceType<typeof InputSettings> | null>(null)
const search = ref<InstanceType<typeof SearchSettings> | null>(null)
const work = ref<Partial<Record<WorkPart, InstanceType<typeof WorkSettings> | null>>>({})
const WORK_PARTS: WorkPart[] = ['instructions', 'commands']
const WORK_PART_ICONS: Record<WorkPart, Component> = {
  instructions: ScrollTextIcon,
  commands: TerminalIcon,
}

/** Reload protocols and hydrate settings for providers whose plugin was just re-enabled. */
async function reloadKinds() {
  try {
    const cleanBefore = hiddenRoles.value !== null && modelDraft() === savedModelDraft
    const previousKinds = new Set(kinds.value.map((k) => k.kind))
    const latest = await api.getModelSettings()
    applyModelSettings(latest)
    kinds.value = latest.kinds
    const latestById = new Map(latest.providers.map((p) => [p.id, p]))
    for (const provider of providers.value) {
      if (previousKinds.has(provider.kind)) continue
      if (!kindOf(provider.kind)) continue
      const saved = latestById.get(provider.id)
      if (!saved || saved.kind !== provider.kind) continue
      provider.settings = { ...saved.settings }
      provider.secretsSet = [...saved.secretsSet]
    }
    if (cleanBefore) {
      savedModelDraft = modelDraft()
    }
  } catch {
    // The models tab shows the protocols it had.
  }
}

watch(
  open,
  async (isOpen) => {
    if (!isOpen) return
    error.value = null
    problems.value = []
    // Not loaded yet: saving now would send an empty list over the saved one.
    hiddenRoles.value = null
    try {
      const [saved, slots] = await Promise.all([api.getModelSettings(), api.getConcurrency()])
      load(saved)
      concurrency.value = slots
      savedModelDraft = modelDraft()
    } catch (cause) {
      error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    }
  },
  { immediate: true },
)

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
  if (!threadModel.value) threadModel.value = id
}

function removeModel(id: string) {
  models.value = models.value.filter((model) => model.id !== id)
  if (threadModel.value === id) threadModel.value = models.value[0]?.id ?? ''
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
        if (
          cause instanceof ApiError &&
          (cause.localized.key === 'error.modelCheck' ||
            cause.localized.key === 'error.modelCheckTimeout' ||
            cause.status === 502 ||
            cause.status === 503)
        ) {
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
      ['search', search.value?.save],
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
  // Saving another tab must not contact every model again when none was edited.
  if (tab.value !== 'models' && modelDraft() === savedModelDraft) return true
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
    const keptCoordinatorModel = keep(coordinatorModel.value || null)
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
        },
        coordinator: {
          model: keptCoordinatorModel,
          effort:
            coordinatorEffort.value === 'default'
              ? keptCoordinatorModel
                ? null
                : 'default'
              : coordinatorEffort.value || null,
        },
        triage: {
          model: keep(hidden?.triage.model ?? null),
          effort: hidden?.triage.effort ?? null,
        },
        research: {
          model: keep(researchModel.value || null),
          effort: researchEffort.value || null,
        },
      },
    })
    load(saved)
    void refreshConversationModels()
    problems.value = saved.problems
    if (concurrency.value) concurrency.value = await api.saveConcurrency(concurrency.value)
    savedModelDraft = modelDraft()
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
  <DouDialog
    v-model:open="open"
    :title="t('settings.title')"
    :width="760"
    :fullscreen="fullscreen"
    :class="{ 'settings-dialog-fullscreen': fullscreen, 'settings-dialog-narrow': isNarrow }"
  >
    <DouTabs v-model="tab" :placement="tabsPlacement" :height="tabsHeight" class="settings-tabs">
      <DouTabPanel value="models" :label="t('settings.tab.models')" :icon="CpuIcon">
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
                      :options="kindOptionsFor(provider.kind)"
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
                <DouText v-else size="sm" variant="secondary">
                  {{ t('settings.providerDisabled') }}
                </DouText>
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
                    <SecretPairs v-model="provider.headers" />
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
              <div v-if="modelOptions.length" class="settings-role-fields">
                <DouFormItem :label="t('settings.threadModel')">
                  <DouCascader v-model="threadPath" :options="threadOptions" separator=" · " />
                </DouFormItem>
                <DouFormItem :label="t('settings.coordinatorModel')">
                  <DouCascader
                    v-model="coordinatorPath"
                    :options="coordinatorOptions"
                    separator=" · "
                  />
                </DouFormItem>
                <DouFormItem>
                  <template #label>
                    <SettingsLabelHelp
                      :label="t('settings.researchModel')"
                      :content="t('settings.researchModelHelp')"
                    />
                  </template>
                  <DouCascader v-model="researchPath" :options="researchOptions" separator=" · " />
                </DouFormItem>
              </div>
              <DouFormItem v-if="concurrency" :label="t('settings.slots')" class="settings-narrow">
                <DouInputNumber v-model="concurrency.slots" :min="1" :max="16" :step="1" />
              </DouFormItem>
            </DouForm>
          </section>
          <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
        </div>
      </DouTabPanel>
      <DouTabPanel value="search" :label="t('settings.tab.search')" :icon="GlobeIcon">
        <SearchSettings v-if="open" ref="search" />
      </DouTabPanel>
      <DouTabPanel
        v-for="part in WORK_PARTS"
        :key="part"
        :value="part"
        :label="t(`settings.tab.${part}`)"
        :icon="WORK_PART_ICONS[part]"
      >
        <WorkSettings
          v-if="open"
          :ref="(el) => (work[part] = el as InstanceType<typeof WorkSettings> | null)"
          :part="part"
        />
      </DouTabPanel>
      <DouTabPanel value="input" :label="t('settings.tab.input')" :icon="SlidersIcon">
        <InputSettings v-if="open" ref="input" />
      </DouTabPanel>
      <DouTabPanel value="mcp" :label="t('settings.tab.mcp')" :icon="McpServerIcon">
        <McpSettings v-if="open" ref="mcp" />
      </DouTabPanel>
      <DouTabPanel value="plugins" :label="t('settings.tab.plugins')" :icon="PlugIcon">
        <PluginSettings v-if="open && tab === 'plugins'" @changed="reloadKinds" />
      </DouTabPanel>
      <DouTabPanel value="data" :label="t('settings.tab.data')" :icon="DatabaseIcon">
        <DataTab v-if="open" />
      </DouTabPanel>
      <DouTabPanel value="updates" :label="t('settings.tab.updates')" :icon="RefreshIcon">
        <UpdatesSettings v-if="open && tab === 'updates'" />
      </DouTabPanel>
    </DouTabs>
    <template #footer="{ close }">
      <DouButton v-if="tab === 'updates' || tab === 'plugins'" @click="close">{{
        t('view.close')
      }}</DouButton>
      <template v-else>
        <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
        <DouButton tone="primary" :loading="saving" :disabled="search?.checking" @click="saveAll">
          {{ t('settings.save') }}
        </DouButton>
      </template>
    </template>
  </DouDialog>
</template>

<style>
/* The dialog panel teleports outside this component, so safe-area padding stays unscoped. */
.settings-dialog-fullscreen {
  padding-top: var(--ariadne-safe-top);
  padding-right: var(--ariadne-safe-right);
  padding-bottom: var(--ariadne-safe-bottom);
  padding-left: var(--ariadne-safe-left);
}

.settings-dialog-narrow {
  --dou-dialog-padding: 16px;
}
</style>

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

.settings-role-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (max-width: 580px) {
  .settings-role-fields {
    grid-template-columns: minmax(0, 1fr);
  }
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
