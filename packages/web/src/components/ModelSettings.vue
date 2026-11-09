<script setup lang="ts">
import {
  DouAlert,
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
} from 'dou-ui'
import { ChevronDownIcon, ChevronRightIcon, PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import {
  api,
  ApiError,
  type Concurrency,
  type Currency,
  type Effort,
  type ModelKind,
  type ModelSettings,
} from '../api'
import { applyModelSettings } from '../store'
import { lt, t, tm } from '../i18n'
import InputSettings from './InputSettings.vue'
import McpSettings from './McpSettings.vue'
import WorkSettings from './WorkSettings.vue'
import DataTab from './DataTab.vue'
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
  /** The price fields are shown; they start shown when a price is set. */
  priceOpen: boolean
  priceLooking: boolean
  /** Why the catalog had no price for it. */
  priceError: string | null
}

const kinds = ref<ModelKind[]>([])
const providers = ref<DraftProvider[]>([])
const models = ref<DraftModel[]>([])
// Threads come first: they do the work. The coordinator may use another model, and think less.
const threadModel = ref<string | null>(null)
const threadEffort = ref<Effort | ''>('')
const coordinatorEffort = ref<Effort | ''>('')
// The one model's effort, for threads and the coordinator alike.
const soleEffort = computed({
  get: () => threadEffort.value,
  set: (effort) => {
    threadEffort.value = effort
    coordinatorEffort.value = effort
  },
})
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

const kindOf = (kind: string) => kinds.value.find((k) => k.kind === kind)
const kindOptions = computed(() =>
  kinds.value.map((kind) => ({ value: kind.kind, label: lt(kind.title) ?? kind.kind })),
)
const providerOptions = computed(() =>
  providers.value.map((provider) => ({ value: provider.id, label: provider.name || provider.id })),
)
const modelOptions = computed(() =>
  modelLabels(
    models.value.filter((model) => model.model.trim()),
    providers.value,
  ).map(({ id, label }) => ({ value: id, label })),
)
// With one model there is nothing to divide: the roles all use it, and only how hard it
// thinks is left to choose.
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
const effortOptions = computed(() => [
  { value: '', label: t('settings.effort.default') },
  ...(['low', 'medium', 'high'] as const).map((effort) => ({
    value: effort,
    label: t(`settings.effort.${effort}`),
  })),
])

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
    priceOpen: !!model.price,
    priceLooking: false,
    priceError: null,
  }))
  showCost.value = settings.showCost
  threadModel.value = settings.roles.thread.model
  threadEffort.value = settings.roles.thread.effort ?? ''
  coordinatorModel.value = settings.roles.coordinator.model ?? ''
  coordinatorEffort.value = settings.roles.coordinator.effort ?? ''
  researchModel.value = settings.roles.research.model ?? ''
  fallback.value = settings.roles.thread.fallback ?? settings.roles.coordinator.fallback ?? ''
  hiddenRoles.value = { triage: settings.roles.triage, research: settings.roles.research }
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
const data = ref<InstanceType<typeof DataTab> | null>(null)
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
  if (!models.value.length) addModel(id)
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
}

function removeProvider(id: string) {
  providers.value = providers.value.filter((provider) => provider.id !== id)
  // Its models go with it.
  for (const model of models.value.filter((m) => m.provider === id)) removeModel(model.id)
}

function addModel(provider = providers.value[0]?.id) {
  if (!provider) return
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
    priceOpen: false,
    priceLooking: false,
    priceError: null,
  })
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

/** Fill a model's price from the public catalog; the user saves it or not. */
async function fillPrice(model: DraftModel) {
  const provider = providers.value.find((p) => p.id === model.provider)
  const hint = [provider?.kind, ...Object.values(provider?.settings ?? {})]
    .filter((value) => typeof value === 'string')
    .join(' ')
  model.priceLooking = true
  model.priceError = null
  try {
    const { price } = await api.lookupModelPrice(model.model.trim(), hint)
    model.currency = price.currency
    model.priceInput = price.input
    model.priceOutput = price.output
    model.priceCacheRead = price.cacheRead ?? null
    model.priceCacheWrite = price.cacheWrite ?? null
  } catch (cause) {
    model.priceError = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    model.priceLooking = false
  }
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
      ['instructions', work.value.instructions?.save],
      ['commands', work.value.commands?.save],
      ['input', input.value?.save],
      ['mcp', mcp.value?.save],
      ['data', data.value?.save],
    ]
    for (const [part, save] of parts) {
      if (save && !(await save())) {
        tab.value = part
        return
      }
    }
    if (tab.value !== 'mcp') open.value = false
  } finally {
    saving.value = false
  }
}

async function saveModels(): Promise<boolean> {
  if (!hiddenRoles.value) return true
  error.value = null
  try {
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
    load(
      await api.saveModelSettings({
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
      }),
    )
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
        <!-- In the order they are set up: a provider, then its models, then who uses which. -->
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
                  <DouFormItem :label="t('settings.name')" class="settings-grow">
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
          </section>

          <section v-if="providers.length" class="settings-section">
            <div class="settings-head">
              <DouText weight="medium">{{ t('settings.models') }}</DouText>
              <DouButton size="sm" variant="ghost" @click="addModel()">
                <template #leading><PlusIcon /></template>
                {{ t('settings.addModel') }}
              </DouButton>
            </div>
            <DouEmpty v-if="!models.length" size="sm" :description="t('settings.noModels')" />
            <DouCard v-for="model in models" :key="model.id" padding="sm">
              <DouForm layout="vertical" size="sm">
                <div class="settings-row">
                  <DouFormItem :label="t('settings.provider')" class="settings-grow">
                    <DouSelect v-model="model.provider" :options="providerOptions" />
                  </DouFormItem>
                  <DouFormItem :label="t('settings.modelName')" class="settings-grow">
                    <DouInput v-model="model.model" />
                  </DouFormItem>
                  <DouButton
                    size="sm"
                    variant="ghost"
                    tone="danger"
                    class="settings-remove"
                    :aria-label="t('settings.remove')"
                    :title="t('settings.remove')"
                    @click="removeModel(model.id)"
                  >
                    <TrashIcon />
                  </DouButton>
                </div>
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
                <DouButton
                  size="sm"
                  variant="ghost"
                  class="settings-more"
                  :aria-expanded="model.priceOpen"
                  @click="model.priceOpen = !model.priceOpen"
                >
                  <template #leading>
                    <ChevronDownIcon v-if="model.priceOpen" />
                    <ChevronRightIcon v-else />
                  </template>
                  {{ t('settings.modelMore') }}
                </DouButton>
                <template v-if="model.priceOpen">
                  <DouFormItem :label="t('settings.maxTokens')" class="settings-narrow">
                    <DouInputNumber
                      v-model="model.maxTokens"
                      :min="256"
                      :max="1000000"
                      :step="1024"
                      :placeholder="t('settings.maxTokensDefault')"
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
                      :loading="model.priceLooking"
                      @click="fillPrice(model)"
                    >
                      {{ t('settings.priceFill') }}
                    </DouButton>
                    <DouText v-if="model.priceError" tone="danger" size="sm">
                      {{ model.priceError }}
                    </DouText>
                  </div>
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
              <DouFormItem
                v-if="modelOptions.length === 1"
                :label="t('settings.effort')"
                class="settings-effort"
              >
                <DouSelect v-model="soleEffort" :options="effortOptions" />
              </DouFormItem>
              <template v-else-if="several">
                <div class="settings-row">
                  <DouFormItem :label="t('settings.threadModel')" class="settings-grow">
                    <DouSelect v-model="threadModel" :options="modelOptions" />
                  </DouFormItem>
                  <DouFormItem :label="t('settings.effort')" class="settings-effort">
                    <DouSelect v-model="threadEffort" :options="effortOptions" />
                  </DouFormItem>
                </div>
                <div class="settings-row">
                  <DouFormItem :label="t('settings.coordinatorModel')" class="settings-grow">
                    <DouSelect v-model="coordinatorModel" :options="coordinatorOptions" />
                  </DouFormItem>
                  <DouFormItem :label="t('settings.effort')" class="settings-effort">
                    <DouSelect v-model="coordinatorEffort" :options="effortOptions" />
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
        <DataTab v-if="open" ref="data" @plugins-changed="reloadKinds" />
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
