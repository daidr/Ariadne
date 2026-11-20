<script setup lang="ts">
import { DouButton, DouDialog, DouForm, DouFormItem, DouSelect } from 'dou-ui'
import { computed, ref } from 'vue'
import { api, type SourceInput, type SourceKind } from '../api'
import { lt, t } from '../i18n'
import SettingsFields from './SettingsFields.vue'
import SourceKindOption from './SourceKindOption.vue'

const props = defineProps<{
  source: SourceInput & { id?: string }
  existing: boolean
  kinds: SourceKind[]
  projectId?: string | undefined
  secretsSet: string[]
}>()
const emit = defineEmits<{ apply: [source: SourceInput]; cancel: [] }>()

const draft = ref<SourceInput>({
  kind: props.source.kind,
  settings: { ...props.source.settings },
  hosting: props.source.hosting
    ? { kind: props.source.hosting.kind, settings: { ...props.source.hosting.settings } }
    : null,
})
const current = computed(() => props.kinds.find((kind) => kind.kind === draft.value.kind))
const hostingKinds = computed(() => props.kinds.filter((kind) => kind.hosting))
const hosting = computed(() =>
  hostingKinds.value.find((kind) => kind.kind === draft.value.hosting?.kind),
)
const option = (kind: SourceKind) => ({
  value: kind.kind,
  label: lt(kind.title) ?? kind.kind,
  disabled: kind.plugin?.ready === false,
})
const kindOf = (kind: unknown) => props.kinds.find((item) => item.kind === kind)
const pluginOf = (kind: unknown) => kindOf(kind)?.plugin
const kinds = computed(() =>
  props.kinds.filter((kind) => kind.provides.includes('workspace')).map(option),
)
const hostingOptions = computed(() => [
  { value: '', label: t('project.source.hostingNone') },
  ...hostingKinds.value.map(option),
])
const storedKeys = computed(() => (draft.value.kind === props.source.kind ? props.secretsSet : []))
const mainKeys = computed(() => storedKeys.value.filter((key) => !key.startsWith('hosting.')))
const hostingKeys = computed(() =>
  draft.value.hosting?.kind === props.source.hosting?.kind
    ? storedKeys.value.filter((key) => key.startsWith('hosting.')).map((key) => key.slice(8))
    : [],
)
const canApply = computed(
  () =>
    !!current.value &&
    (current.value.plugin?.ready !== false ||
      (props.existing && draft.value.kind === props.source.kind)) &&
    (hosting.value?.plugin?.ready !== false ||
      (props.existing && draft.value.hosting?.kind === props.source.hosting?.kind)) &&
    (current.value.settings.required ?? []).every((key) => {
      const value = draft.value.settings[key]
      return typeof value === 'string'
        ? !!value.trim()
        : value != null || mainKeys.value.includes(key)
    }),
)

function changeKind(value: unknown) {
  if (pluginOf(value)?.ready === false) return
  draft.value = { kind: String(value), settings: { ...kindOf(value)?.defaults }, hosting: null }
}

function changeHosting(value: unknown) {
  if (pluginOf(value)?.ready === false) return
  draft.value.hosting = value
    ? { kind: String(value), settings: { ...kindOf(value)?.defaults } }
    : null
}

function browse(_key: string, path: string) {
  return api
    .browseSource(draft.value.kind, {
      projectId: props.projectId ?? null,
      sourceId: props.source.id ?? '',
      settings: { ...draft.value.settings },
      path,
    })
    .then((result) => result.listing)
}

function apply() {
  if (canApply.value) emit('apply', draft.value)
}

function onEnter(event: KeyboardEvent) {
  const target = event.target
  if (
    event.defaultPrevented ||
    event.isComposing ||
    !(target instanceof HTMLInputElement) ||
    target.getAttribute('aria-expanded') === 'true'
  )
    return
  event.preventDefault()
  apply()
}
</script>

<template>
  <DouDialog
    :open="true"
    :title="t(existing ? 'project.sources.edit' : 'project.sources.add')"
    :width="480"
    centered
    @update:open="
      (open) => {
        if (!open) emit('cancel')
      }
    "
  >
    <DouForm
      layout="vertical"
      size="sm"
      class="source-fields"
      @keydown.enter="onEnter"
      @submit.prevent="apply"
    >
      <DouFormItem :label="t('project.source.kind')">
        <DouSelect :model-value="draft.kind" :options="kinds" @update:model-value="changeKind">
          <template #option="{ option }">
            <SourceKindOption :label="option.label" :plugin="pluginOf(option.value)" />
          </template>
        </DouSelect>
      </DouFormItem>
      <SettingsFields
        v-if="current"
        :key="current.kind"
        :schema="current.settings"
        :values="draft.settings"
        :secrets-set="mainKeys"
        :browse="current.browse ? browse : undefined"
        @clear-secret="
          (key) => {
            draft.settings[key] = null
          }
        "
      />
      <template v-if="current?.remote && hostingKinds.length">
        <DouFormItem :label="t('project.source.hosting')">
          <DouSelect
            :model-value="draft.hosting?.kind ?? ''"
            :options="hostingOptions"
            @update:model-value="changeHosting"
          >
            <template #option="{ option }">
              <SourceKindOption :label="option.label" :plugin="pluginOf(option.value)" />
            </template>
          </DouSelect>
        </DouFormItem>
        <SettingsFields
          v-if="hosting && draft.hosting"
          :key="'hosting-' + hosting.kind"
          :schema="hosting.settings"
          :values="draft.hosting.settings"
          :secrets-set="hostingKeys"
          @clear-secret="
            (key) => {
              if (draft.hosting) draft.hosting.settings[key] = null
            }
          "
        />
      </template>
    </DouForm>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
      <DouButton tone="primary" :disabled="!canApply" @click="apply">
        {{ t(existing ? 'settings.save' : 'project.references.confirm') }}
      </DouButton>
    </template>
  </DouDialog>
</template>

<style scoped>
.source-fields {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
