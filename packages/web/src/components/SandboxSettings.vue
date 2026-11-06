<script setup lang="ts">
import { DouButton, DouSwitch, DouText, DouTextarea } from 'dou-ui'
import { computed, onMounted, ref } from 'vue'
import { api, ApiError, type SandboxConfig, type SandboxStatus } from '../api'
import { t, tm } from '../i18n'

/**
 * How thread commands are isolated on this machine: what runs now, and the setting for the
 * next start (whether to isolate, and which paths to hide besides Ariadne's own).
 */
const status = ref<SandboxStatus | null>(null)
const saved = ref<SandboxConfig | null>(null)
const isolate = ref(true)
const hide = ref('')
const saving = ref(false)
const error = ref<string | null>(null)

onMounted(async () => {
  try {
    const result = await api.getSandbox()
    status.value = result.sandbox
    saved.value = result.configured
    if (result.configured) apply(result.configured)
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
})

function apply(config: SandboxConfig) {
  isolate.value = config.mode !== 'off'
  hide.value = config.hide.join('\n')
}

const draft = computed<SandboxConfig>(() => ({
  mode: isolate.value ? 'auto' : 'off',
  hide: hide.value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean),
}))
const same = (a: SandboxConfig | null, b: SandboxConfig) =>
  !!a && a.mode === b.mode && a.hide.join('\n') === b.hide.join('\n')
const dirty = computed(() => !same(saved.value, draft.value))
// The setting is read when Ariadne starts: once it differs from what runs, say so.
const savedNow = ref(false)
const pending = computed(
  () =>
    !dirty.value &&
    (savedNow.value || (!!status.value && !!saved.value && saved.value.mode !== status.value.mode)),
)

const now = computed(() => {
  const current = status.value
  if (!current) return ''
  if (current.mode === 'off') return t('sandbox.now.off')
  if (current.kind) return t('sandbox.now.on', { kind: current.kind })
  return t(current.platform === 'win32' ? 'sandbox.now.windows' : 'sandbox.now.none')
})

async function save() {
  saving.value = true
  error.value = null
  try {
    saved.value = (await api.saveSandbox(draft.value)).configured
    apply(saved.value)
    savedNow.value = true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="sandbox" :aria-label="t('sandbox.title')">
    <div class="sandbox-head">
      <DouText weight="medium">{{ t('sandbox.title') }}</DouText>
      <DouText size="sm" variant="tertiary">{{ now }}</DouText>
    </div>
    <div class="sandbox-switch">
      <span>{{ t('sandbox.isolate') }}</span>
      <DouSwitch v-model="isolate" :aria-label="t('sandbox.isolate')" />
    </div>
    <DouTextarea
      v-model="hide"
      size="sm"
      :autosize="{ minRows: 2, maxRows: 6 }"
      :disabled="!isolate"
      :placeholder="t('sandbox.hidePlaceholder')"
      :aria-label="t('sandbox.hide')"
      class="sandbox-paths"
    />
    <div class="sandbox-actions">
      <DouText v-if="error" tone="danger" size="sm">{{ error }}</DouText>
      <DouText v-else-if="pending" size="sm" variant="tertiary">
        {{ t('sandbox.restart') }}
      </DouText>
      <DouButton size="sm" :disabled="!dirty" :loading="saving" @click="save">
        {{ t('settings.save') }}
      </DouButton>
    </div>
  </section>
</template>

<style scoped>
.sandbox {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.sandbox-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}

.sandbox-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.sandbox-paths :deep(textarea) {
  font-family: var(--dou-font-family-mono);
}

.sandbox-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
}
</style>
