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
} from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import {
  api,
  ApiError,
  type Concurrency,
  type Effort,
  type ModelKind,
  type ModelSettings,
} from '../api'
import { notifications, setNotifications } from '../notify'
import { applyModelSettings } from '../store'
import { lt, t, tm } from '../i18n'
import InputSettings from './InputSettings.vue'
import McpSettings from './McpSettings.vue'
import PluginSettings from './PluginSettings.vue'
import WorktreeSettings from './WorktreeSettings.vue'
import SettingsFields from './SettingsFields.vue'

const open = defineModel<boolean>('open', { default: false })

interface DraftProvider {
  id: string
  kind: string
  name: string
  settings: Record<string, unknown>
  secretsSet: string[]
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
}

const kinds = ref<ModelKind[]>([])
const providers = ref<DraftProvider[]>([])
const models = ref<DraftModel[]>([])
// Threads come first: they do the work. The coordinator may use another model, and think less.
const threadModel = ref<string | null>(null)
const threadEffort = ref<Effort | ''>('')
const coordinatorEffort = ref<Effort | ''>('')
// `''` stands for "the coordinator's", which the select cannot hold as `null`.
const coordinatorModel = ref<string>('')
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
  models.value
    .filter((model) => model.model.trim())
    .map((model) => ({
      value: model.id,
      label: `${providers.value.find((p) => p.id === model.provider)?.name ?? ''} / ${model.model.trim()}`,
    })),
)
const coordinatorOptions = computed(() => [
  { value: '', label: t('settings.sameAsThreads') },
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
  providers.value = settings.providers.map((provider) => ({
    ...provider,
    settings: { ...provider.settings },
  }))
  models.value = settings.models.map((model) => ({
    id: model.id,
    provider: model.provider,
    model: model.model,
    contextK: model.contextTokens ? Math.round(model.contextTokens / 1000) : null,
    images: model.images !== false,
    note: model.note ?? '',
  }))
  threadModel.value = settings.roles.thread.model
  threadEffort.value = settings.roles.thread.effort ?? ''
  coordinatorModel.value = settings.roles.coordinator.model ?? ''
  coordinatorEffort.value = settings.roles.coordinator.effort ?? ''
}

export type SettingsTab = 'models' | 'input' | 'notifications' | 'plugins' | 'mcp' | 'worktrees'
const tab = defineModel<SettingsTab>('tab', { default: 'models' })
const mcp = ref<InstanceType<typeof McpSettings> | null>(null)
const input = ref<InstanceType<typeof InputSettings> | null>(null)
// The footer saves the tab in view; the models, the input and the MCP servers wait for it.
const saves = computed(() => ['models', 'input', 'mcp'].includes(tab.value))

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
  })
  if (!models.value.length) addModel(id)
}

function removeProvider(id: string) {
  providers.value = providers.value.filter((provider) => provider.id !== id)
  // Its models go with it.
  for (const model of models.value.filter((m) => m.provider === id)) removeModel(model.id)
}

function addModel(provider = providers.value[0]?.id) {
  if (!provider) return
  const id = newId('m')
  models.value.push({ id, provider, model: '', contextK: null, images: true, note: '' })
  threadModel.value ??= id
}

function removeModel(id: string) {
  models.value = models.value.filter((model) => model.id !== id)
  if (threadModel.value === id) threadModel.value = models.value[0]?.id ?? null
  if (coordinatorModel.value === id) coordinatorModel.value = ''
}

/** Forget a stored secret: saving sends `null`, which the server treats as "clear". */
function clearSecret(provider: DraftProvider, key: string) {
  provider.settings[key] = null
  provider.secretsSet = provider.secretsSet.filter((name) => name !== key)
}

/**
 * Save the tab in view. Saved MCP servers then connect, so the dialog stays open: their
 * tools show up to be marked.
 */
async function saveTab() {
  if (tab.value === 'input') {
    saving.value = true
    try {
      if (await input.value?.save()) open.value = false
    } finally {
      saving.value = false
    }
    return
  }
  if (tab.value !== 'mcp') return save()
  saving.value = true
  try {
    await mcp.value?.save()
  } finally {
    saving.value = false
  }
}

async function save() {
  saving.value = true
  error.value = null
  try {
    // A model without a name is left out, and so is a role that pointed at it.
    const kept = models.value.filter((model) => model.model.trim())
    const keep = (id: string | null) => (id && kept.some((m) => m.id === id) ? id : null)
    load(
      await api.saveModelSettings({
        providers: providers.value.map(({ id, kind, name, settings }) => ({
          id,
          kind,
          name,
          settings,
        })),
        models: kept.map((model) => ({
          id: model.id,
          provider: model.provider,
          model: model.model.trim(),
          ...(model.contextK ? { contextTokens: model.contextK * 1000 } : {}),
          ...(model.images ? {} : { images: false as const }),
          ...(model.note.trim() ? { note: model.note.trim() } : {}),
        })),
        roles: {
          thread: {
            model: keep(threadModel.value) ?? kept[0]?.id ?? null,
            effort: threadEffort.value || null,
          },
          coordinator: {
            model: keep(coordinatorModel.value || null),
            effort: coordinatorEffort.value || null,
          },
        },
      }),
    )
    if (concurrency.value) concurrency.value = await api.saveConcurrency(concurrency.value)
    open.value = false
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <DouDialog v-model:open="open" :title="t('settings.title')" :width="560">
    <DouTabs v-model="tab">
      <DouTabPanel value="models" :label="t('settings.tab.models')">
        <div class="settings">
          <div class="settings-head">
            <span>{{ t('settings.providers') }}</span>
            <DouButton size="sm" variant="outline" :disabled="!kinds.length" @click="addProvider">
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
                    @update:model-value="
                      (value) => ((provider.kind = String(value)), (provider.secretsSet = []))
                    "
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
            </DouForm>
          </DouCard>

          <template v-if="providers.length">
            <div class="settings-head">
              <span>{{ t('settings.models') }}</span>
              <DouButton size="sm" variant="outline" @click="addModel()">
                <template #leading><PlusIcon /></template>
                {{ t('settings.addModel') }}
              </DouButton>
            </div>
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
                  <DouFormItem :label="t('settings.modelNote')" class="settings-grow">
                    <DouInput
                      v-model="model.note"
                      :maxlength="200"
                      :placeholder="t('settings.modelNotePlaceholder')"
                    />
                  </DouFormItem>
                </div>
              </DouForm>
            </DouCard>
          </template>

          <DouForm v-if="modelOptions.length || concurrency" layout="vertical" size="sm">
            <template v-if="modelOptions.length">
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
            </template>
            <div v-if="concurrency" class="settings-row">
              <DouFormItem
                :label="t('settings.slots')"
                :help="t('settings.slotsHelp')"
                class="settings-grow"
              >
                <DouInputNumber v-model="concurrency.slots" :min="1" :max="16" :step="1" />
              </DouFormItem>
              <DouFormItem
                :label="t('settings.reservedSlots')"
                :help="t('settings.reservedSlotsHelp')"
                class="settings-grow"
              >
                <DouInputNumber v-model="concurrency.reserved" :min="0" :max="4" :step="1" />
              </DouFormItem>
            </div>
          </DouForm>
          <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
        </div>
      </DouTabPanel>
      <DouTabPanel value="input" :label="t('settings.tab.input')">
        <InputSettings v-if="open" ref="input" />
      </DouTabPanel>
      <DouTabPanel value="notifications" :label="t('settings.tab.notifications')">
        <div class="settings">
          <div class="settings-switch">
            <div class="settings-switch-text">
              <span>{{ t('settings.notify') }}</span>
            </div>
            <DouSwitch
              :model-value="notifications.enabled"
              :disabled="
                notifications.permission === 'unsupported' || notifications.permission === 'denied'
              "
              @update:model-value="(on: boolean) => setNotifications(on)"
            />
          </div>
          <DouText
            v-if="
              notifications.permission === 'denied' || notifications.permission === 'unsupported'
            "
            tone="warning"
            size="sm"
          >
            {{
              t(
                notifications.permission === 'denied'
                  ? 'settings.notifyDenied'
                  : 'settings.notifyUnsupported',
              )
            }}
          </DouText>
        </div>
      </DouTabPanel>
      <DouTabPanel value="plugins" :label="t('settings.tab.plugins')">
        <PluginSettings v-if="open" @changed="reloadKinds" />
      </DouTabPanel>
      <DouTabPanel value="mcp" :label="t('settings.tab.mcp')">
        <McpSettings v-if="open" ref="mcp" />
      </DouTabPanel>
      <DouTabPanel value="worktrees" :label="t('settings.tab.worktrees')">
        <WorktreeSettings v-if="open && tab === 'worktrees'" />
      </DouTabPanel>
    </DouTabs>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">
        {{ t(saves ? 'settings.cancel' : 'settings.close') }}
      </DouButton>
      <DouButton v-if="saves" tone="primary" :loading="saving" @click="saveTab">
        {{ t('settings.save') }}
      </DouButton>
    </template>
  </DouDialog>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 12px;
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

.settings-remove {
  flex: none;
  align-self: flex-start;
}
</style>
