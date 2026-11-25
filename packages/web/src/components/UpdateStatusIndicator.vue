<script setup lang="ts">
import { ArrowUpCircleIcon } from '@dou-ui/icons'
import { DouButton, DouPopover, DouSpinner, DouStatusDot, DouText } from 'dou-ui'
import { computed, ref, watch } from 'vue'
import { api, ApiError, type UpdateStatus } from '../api'
import { t, tm } from '../i18n'
import { applyUpdateStatus, state } from '../store'
import {
  effectiveRestartPhase,
  hasActiveRestartWork,
  restartNowWarningMessage,
  restartStatusMessage,
} from '../update-restart'

defineProps<{ compact?: boolean }>()
const emit = defineEmits<{ openSettings: [] }>()

const open = ref(false)
const busy = ref<'idle' | 'now' | 'cancel' | null>(null)
const confirmingNow = ref(false)
const error = ref<string | null>(null)

const status = computed(() => state.updateStatus)
const phase = computed(() => effectiveRestartPhase(status.value))
const restartable = computed(
  () => status.value?.state === 'ready' && Boolean(status.value?.canRestart),
)
const visible = computed(() => phase.value !== '' || restartable.value)
const hasActive = computed(() => hasActiveRestartWork(status.value))
const restartNowHint = computed(() => restartNowWarningMessage(status.value))
const description = computed(() => {
  const current = status.value
  if (!current) return ''
  const phaseMessage = restartStatusMessage(current)
  if (phaseMessage) return phaseMessage
  return t('updates.ready', { version: current.release?.version ?? '' })
})

const triggerLabel = computed(() => {
  if (phase.value === 'restarting') return t('updates.header.restarting')
  if (phase.value === 'draining') return t('updates.header.draining')
  if (phase.value === 'waiting_idle') return t('updates.header.waitingIdle')
  return t('updates.header.ready')
})

const popoverTitle = computed(() => {
  if (phase.value === 'restarting') return t('updates.header.titleRestarting')
  if (phase.value === 'draining') return t('updates.header.titleDraining')
  const version = status.value?.release?.version
  if (version) return t('updates.header.titleReady', { version })
  return triggerLabel.value
})

watch(open, (opened) => {
  if (!opened) {
    confirmingNow.value = false
    error.value = null
  }
})

watch([phase, hasActive], ([nextPhase, active]) => {
  if (nextPhase === 'restarting' || !active) {
    confirmingNow.value = false
  }
})

async function runAction(kind: 'idle' | 'now' | 'cancel', request: () => Promise<UpdateStatus>) {
  if (busy.value) return
  busy.value = kind
  error.value = null
  try {
    const view = await request()
    applyUpdateStatus(view)
    confirmingNow.value = false
  } catch (cause) {
    if (kind === 'idle' || kind === 'now') {
      const isBusinessError =
        cause instanceof ApiError && cause.localized && cause.status < 500 && cause.status !== 499
      if (isBusinessError) {
        error.value = tm(cause.localized)
      } else if (status.value) {
        applyUpdateStatus({
          ...status.value,
          restarting: true,
          restartPhase: 'restarting',
        })
        confirmingNow.value = false
      }
      return
    }
    error.value =
      cause instanceof ApiError && cause.localized
        ? tm(cause.localized)
        : t('updates.error.progress')
  } finally {
    busy.value = null
  }
}

function restartWhenIdle() {
  void runAction('idle', () => api.restartToApplyUpdate('when-idle'))
}

function requestRestartNow() {
  if (hasActive.value && !confirmingNow.value) {
    confirmingNow.value = true
    error.value = null
    return
  }
  restartNow()
}

function restartNow() {
  void runAction('now', () => api.restartToApplyUpdate('now'))
}

function cancel() {
  void runAction('cancel', () => api.cancelRestart())
}

function openUpdateSettings() {
  open.value = false
  emit('openSettings')
}
</script>

<template>
  <DouPopover
    v-if="visible"
    v-model:open="open"
    :title="popoverTitle"
    :width="320"
    placement="bottom-end"
  >
    <DouButton
      variant="ghost"
      size="sm"
      class="update-indicator"
      :class="{ 'update-indicator-draining': phase === 'draining' }"
      :aria-label="triggerLabel"
      :title="triggerLabel"
      :aria-expanded="open"
    >
      <template #leading>
        <DouSpinner v-if="phase === 'restarting'" :size="12" />
        <DouStatusDot v-else-if="phase === 'draining'" tone="warning" ripple />
        <DouStatusDot v-else-if="phase === 'waiting_idle'" tone="primary" />
        <ArrowUpCircleIcon v-else class="update-ready-icon" aria-hidden="true" />
      </template>
      <template v-if="!compact" #default>
        {{ triggerLabel }}
      </template>
    </DouButton>
    <template #content>
      <div class="update-popover">
        <DouText size="sm" variant="secondary" role="status">
          {{ description }}
        </DouText>
        <DouText v-if="error" size="sm" tone="danger" role="status">
          {{ error }}
        </DouText>
        <div v-if="confirmingNow && restartNowHint" class="update-confirm-box">
          <DouText size="sm">{{ restartNowHint }}</DouText>
          <div class="update-popover-actions">
            <DouButton
              size="sm"
              tone="danger"
              :loading="busy === 'now'"
              :disabled="busy !== null"
              @click="restartNow"
            >
              {{ t('updates.restartNowConfirm') }}
            </DouButton>
            <DouButton
              size="sm"
              variant="ghost"
              :disabled="busy !== null"
              @click="confirmingNow = false"
            >
              {{ t('settings.cancel') }}
            </DouButton>
          </div>
        </div>
        <div v-else-if="phase !== 'restarting'" class="update-popover-actions">
          <template v-if="phase === 'draining'">
            <DouButton
              size="sm"
              tone="primary"
              :loading="busy === 'cancel'"
              :disabled="busy !== null"
              @click="cancel"
            >
              {{ t('updates.cancelDrain') }}
            </DouButton>
            <DouButton
              v-if="restartable"
              size="sm"
              :loading="busy === 'now'"
              :disabled="busy !== null"
              @click="requestRestartNow"
            >
              {{ t('updates.restartNow') }}
            </DouButton>
          </template>
          <template v-else-if="phase === 'waiting_idle'">
            <DouButton
              size="sm"
              tone="primary"
              :loading="busy === 'now'"
              :disabled="busy !== null"
              @click="requestRestartNow"
            >
              {{ t('updates.restartNow') }}
            </DouButton>
            <DouButton
              size="sm"
              :loading="busy === 'cancel'"
              :disabled="busy !== null"
              @click="cancel"
            >
              {{ t('updates.cancelRestart') }}
            </DouButton>
          </template>
          <template v-else-if="restartable">
            <DouButton
              size="sm"
              tone="primary"
              :loading="busy === 'idle'"
              :disabled="busy !== null"
              @click="restartWhenIdle"
            >
              {{ hasActive ? t('updates.restart') : t('updates.restartApply') }}
            </DouButton>
            <DouButton
              v-if="hasActive"
              size="sm"
              :loading="busy === 'now'"
              :disabled="busy !== null"
              @click="requestRestartNow"
            >
              {{ t('updates.restartNow') }}
            </DouButton>
          </template>
        </div>
        <div class="update-popover-footer">
          <DouButton
            variant="ghost"
            size="sm"
            class="update-popover-settings"
            @click="openUpdateSettings"
          >
            {{ t('updates.header.openSettings') }}
          </DouButton>
        </div>
      </div>
    </template>
  </DouPopover>
</template>

<style scoped>
.update-ready-icon {
  color: var(--dou-color-primary);
}

.update-indicator-draining {
  --dou-button-fg: var(--dou-color-warning);
}

.update-popover {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.update-popover-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.update-confirm-box {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-muted);
}

.update-popover-footer {
  display: flex;
  justify-content: flex-end;
  padding-top: 8px;
  border-top: 1px solid var(--dou-color-border-subtle);
}
</style>
