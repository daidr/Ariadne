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
  DouTabPanel,
  DouTabs,
} from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { api, ApiError, type Concurrency, type ModelKind, type ModelSettings } from '../api'
import { state } from '../store'
import { lt, t, tm } from '../i18n'
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

const kinds = ref<ModelKind[]>([])
const providers = ref<DraftProvider[]>([])
const defaultProvider = ref<string | null>(null)
const defaultModel = ref('')
// In thousands of tokens; empty uses the default.
const contextK = ref<number | null>(null)
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

function load(settings: ModelSettings) {
  state.modelReady = !!settings.default
  kinds.value = settings.kinds
  providers.value = settings.providers.map((provider) => ({
    ...provider,
    settings: { ...provider.settings },
  }))
  defaultProvider.value = settings.default?.provider ?? null
  defaultModel.value = settings.default?.model ?? ''
  const tokens = settings.default?.contextTokens
  contextK.value = tokens ? Math.round(tokens / 1000) : null
}

const tab = ref<'models' | 'plugins' | 'worktrees'>('models')

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
    const [models, slots] = await Promise.all([api.getModelSettings(), api.getConcurrency()])
    load(models)
    concurrency.value = slots
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
})

function addProvider() {
  const kind = kinds.value[0]
  if (!kind) return
  const id = `p_${Date.now().toString(36)}`
  providers.value.push({
    id,
    kind: kind.kind,
    name: lt(kind.title) ?? kind.kind,
    settings: {},
    secretsSet: [],
  })
  defaultProvider.value ??= id
}

function removeProvider(id: string) {
  providers.value = providers.value.filter((provider) => provider.id !== id)
  if (defaultProvider.value === id) {
    // The model name belonged to the removed provider; do not carry it over.
    defaultProvider.value = providers.value[0]?.id ?? null
    defaultModel.value = ''
  }
}

/** Forget a stored secret: saving sends `null`, which the server treats as "clear". */
function clearSecret(provider: DraftProvider, key: string) {
  provider.settings[key] = null
  provider.secretsSet = provider.secretsSet.filter((name) => name !== key)
}

async function save() {
  saving.value = true
  error.value = null
  try {
    load(
      await api.saveModelSettings({
        providers: providers.value.map(({ id, kind, name, settings }) => ({
          id,
          kind,
          name,
          settings,
        })),
        default:
          defaultProvider.value && defaultModel.value.trim()
            ? {
                provider: defaultProvider.value,
                model: defaultModel.value.trim(),
                ...(contextK.value ? { contextTokens: contextK.value * 1000 } : {}),
              }
            : null,
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

          <DouForm v-if="providers.length || concurrency" layout="vertical" size="sm">
            <template v-if="providers.length">
              <div class="settings-row">
                <DouFormItem :label="t('settings.defaultProvider')" class="settings-grow">
                  <DouSelect v-model="defaultProvider" :options="providerOptions" />
                </DouFormItem>
                <DouFormItem
                  :label="t('settings.defaultModel')"
                  :help="t('settings.defaultModelHelp')"
                  class="settings-grow"
                >
                  <DouInput v-model="defaultModel" />
                </DouFormItem>
              </div>
              <div class="settings-row">
                <DouFormItem
                  :label="t('settings.contextTokens')"
                  :help="t('settings.contextTokensHelp')"
                  class="settings-grow"
                >
                  <DouInputNumber
                    v-model="contextK"
                    :min="8"
                    :max="10000"
                    :step="8"
                    placeholder="128"
                  />
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
      <DouTabPanel value="plugins" :label="t('settings.tab.plugins')">
        <PluginSettings v-if="open" @changed="reloadKinds" />
      </DouTabPanel>
      <DouTabPanel value="worktrees" :label="t('settings.tab.worktrees')">
        <WorktreeSettings v-if="open && tab === 'worktrees'" />
      </DouTabPanel>
    </DouTabs>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">
        {{ t(tab === 'models' ? 'settings.cancel' : 'settings.close') }}
      </DouButton>
      <DouButton v-if="tab === 'models'" tone="primary" :loading="saving" @click="save">
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

.settings-remove {
  flex: none;
  align-self: flex-start;
}
</style>
