<script setup lang="ts">
import { KeyIcon, LockKeyholeIcon } from '@dou-ui/icons'
import { DouButton, DouDialog, DouForm, DouFormItem, DouInput, DouResult } from 'dou-ui'
import { nextTick, ref, useTemplateRef, watch } from 'vue'
import { getToken, isValidToken, parseTokenInput, saveToken } from '../api'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    /** Override the page reload action (primarily for tests). */
    reload?: () => void
  }>(),
  {
    reload: () => window.location.reload(),
  },
)

const hasStoredToken = !!getToken()
const dialogOpen = ref(false)
const draft = ref('')
const validationError = ref<string | null>(null)
const tokenInput = useTemplateRef<InstanceType<typeof DouInput>>('tokenInput')

watch(draft, () => {
  if (validationError.value) validationError.value = null
})

function openDialog() {
  draft.value = ''
  validationError.value = null
  dialogOpen.value = true
  void nextTick(() => tokenInput.value?.focus())
}

function focusInput() {
  tokenInput.value?.focus()
}

function submit() {
  const token = parseTokenInput(draft.value)
  if (!token) {
    validationError.value = t('auth.tokenRequired')
    tokenInput.value?.focus()
    return
  }
  if (!isValidToken(token)) {
    validationError.value = t('auth.tokenInvalidChars')
    tokenInput.value?.focus()
    return
  }
  saveToken(token)
  dialogOpen.value = false
  props.reload()
}

function onEnter(event: KeyboardEvent) {
  if (event.defaultPrevented || event.isComposing || !(event.target instanceof HTMLInputElement)) {
    return
  }
  event.preventDefault()
  submit()
}
</script>

<template>
  <div class="unauthorized-screen">
    <DouResult
      class="unauthorized-result"
      :status="hasStoredToken ? 'danger' : 'warning'"
      :title="t('error.unauthorized')"
      :description="t(hasStoredToken ? 'auth.invalidDescription' : 'auth.missingDescription')"
    >
      <template #icon>
        <LockKeyholeIcon aria-hidden="true" />
      </template>
      <DouButton tone="primary" @click="openDialog">
        <KeyIcon aria-hidden="true" />
        {{ t('auth.setToken') }}
      </DouButton>
      <DouButton variant="ghost" @click="props.reload()">
        {{ t('error.reload') }}
      </DouButton>
    </DouResult>

    <DouDialog
      v-model:open="dialogOpen"
      :title="t('auth.dialogTitle')"
      :width="440"
      centered
      @opened="focusInput"
    >
      <DouForm
        layout="vertical"
        size="sm"
        class="token-form"
        @keydown.enter="onEnter"
        @submit.prevent="submit"
      >
        <DouFormItem :label="t('auth.tokenLabel')" :error="validationError ?? undefined" required>
          <DouInput
            ref="tokenInput"
            v-model="draft"
            type="password"
            show-password
            autocomplete="off"
            spellcheck="false"
            :invalid="!!validationError"
            :placeholder="t('auth.tokenPlaceholder')"
          />
        </DouFormItem>
      </DouForm>
      <template #footer="{ close }">
        <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
        <DouButton tone="primary" @click="submit">{{ t('auth.confirm') }}</DouButton>
      </template>
    </DouDialog>
  </div>
</template>

<style scoped>
.unauthorized-screen {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  min-height: 0;
  padding: 24px;
  /* A little above the middle, matching StartScreen. */
  padding-bottom: max(12vh, 24px, var(--ariadne-safe-bottom));
  overflow: auto;
}

:global(html[data-fold-mode='vertical']) .unauthorized-screen {
  padding-bottom: calc(
    var(--ariadne-fold-bottom-height, 0px) + var(--ariadne-fold-gap, 0px) + 16px
  );
}

.unauthorized-result {
  width: min(480px, 100%);
  --dou-result-padding: 24px 0;
}

.token-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
