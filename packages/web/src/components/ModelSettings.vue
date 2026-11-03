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
} from 'dou-ui'
import type { LocalizedText } from '@ariadne/plugin-sdk'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, ref, watch } from 'vue'
import { api, ApiError, type ModelKind, type ModelSettings } from '../api'
import { locale, t, tm } from '../i18n'

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
const saving = ref(false)
const error = ref<string | null>(null)

/** Plugin text in the current locale, falling back to Chinese. */
const lt = (text: LocalizedText | undefined) =>
  text ? (text[locale.value] ?? text['zh-CN']) : undefined

const kindOf = (kind: string) => kinds.value.find((k) => k.kind === kind)
const kindOptions = computed(() =>
  kinds.value.map((kind) => ({ value: kind.kind, label: lt(kind.title) ?? kind.kind })),
)
const providerOptions = computed(() =>
  providers.value.map((provider) => ({ value: provider.id, label: provider.name || provider.id })),
)

function load(settings: ModelSettings) {
  kinds.value = settings.kinds
  providers.value = settings.providers.map((provider) => ({
    ...provider,
    settings: { ...provider.settings },
  }))
  defaultProvider.value = settings.default?.provider ?? null
  defaultModel.value = settings.default?.model ?? ''
}

watch(open, async (isOpen) => {
  if (!isOpen) return
  error.value = null
  try {
    load(await api.getModelSettings())
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
  if (defaultProvider.value === id) defaultProvider.value = providers.value[0]?.id ?? null
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
            ? { provider: defaultProvider.value, model: defaultModel.value.trim() }
            : null,
      }),
    )
    open.value = false
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    saving.value = false
  }
}

const asString = (value: unknown) => (typeof value === 'string' ? value : '')
const asNumber = (value: unknown) => (typeof value === 'number' ? value : null)
</script>

<template>
  <DouDialog v-model:open="open" :title="t('settings.title')" :width="560">
    <div class="settings">
      <div class="settings-head">
        <span>{{ t('settings.providers') }}</span>
        <DouButton size="sm" variant="outline" :disabled="!kinds.length" @click="addProvider">
          <template #leading><PlusIcon /></template>
          {{ t('settings.add') }}
        </DouButton>
      </div>
      <DouEmpty v-if="!providers.length" size="sm" :description="t('settings.noProviders')" />
      <DouCard v-for="provider in providers" :key="provider.id" padding="md">
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
          </div>
          <template v-for="(field, key) in kindOf(provider.kind)?.settings.properties" :key="key">
            <DouFormItem
              :label="lt(field.title)"
              :help="
                field.type === 'string' && field.secret && provider.secretsSet.includes(String(key))
                  ? t('settings.secretSet')
                  : lt(field.description)
              "
            >
              <DouInput
                v-if="field.type === 'string'"
                :model-value="asString(provider.settings[key])"
                :type="field.secret ? 'password' : 'text'"
                :show-password="field.secret"
                :placeholder="field.default"
                @update:model-value="(value) => (provider.settings[key] = value)"
              />
              <DouInputNumber
                v-else-if="field.type === 'number'"
                :model-value="asNumber(provider.settings[key])"
                :placeholder="field.default === undefined ? undefined : String(field.default)"
                @update:model-value="(value) => (provider.settings[key] = value)"
              />
              <DouSwitch
                v-else-if="field.type === 'boolean'"
                :model-value="provider.settings[key] === true"
                @update:model-value="(value) => (provider.settings[key] = value)"
              />
              <DouSelect
                v-else
                :model-value="asString(provider.settings[key]) || field.default || null"
                :options="field.options"
                @update:model-value="(value) => (provider.settings[key] = value)"
              />
            </DouFormItem>
          </template>
          <div class="settings-remove">
            <DouButton size="sm" variant="ghost" tone="danger" @click="removeProvider(provider.id)">
              <template #leading><TrashIcon /></template>
              {{ t('settings.remove') }}
            </DouButton>
          </div>
        </DouForm>
      </DouCard>

      <DouForm v-if="providers.length" layout="vertical" size="sm">
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
      </DouForm>
      <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
    </div>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
      <DouButton tone="primary" :loading="saving" @click="save">{{ t('settings.save') }}</DouButton>
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
  display: flex;
  justify-content: flex-end;
}
</style>
