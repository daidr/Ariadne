<script setup lang="ts">
import { computed } from 'vue'
import {
  DouButton,
  DouMarkdown,
  DouSelect,
  DouSpinner,
  DouSwitch,
  DouText,
  type MarkdownUrlKind,
} from 'dou-ui'
import { t } from '../i18n'
import { useUpdatesSettings } from './useUpdatesSettings'

const {
  status,
  pending,
  busy,
  updating,
  error,
  release,
  message,
  installable,
  installLabel,
  restartable,
  restartPhase,
  hasActiveWork,
  confirmingRestartNow,
  restartNowWarning,
  check,
  setChannel,
  setAutomatic,
  install,
  restart,
  requestRestartNow,
  dismissRestartNowConfirm,
  cancelRestart,
  load,
} = useUpdatesSettings()
const channels = computed(() => [
  { value: 'stable', label: t('updates.stable') },
  { value: 'preview', label: t('updates.preview') },
])

function releaseNoteURL(address: string, kind: MarkdownUrlKind) {
  const url = address.trim()
  return kind === 'link' && /^https:\/\//i.test(url) ? url : undefined
}
</script>

<template>
  <div class="updates-settings">
    <DouSpinner v-if="pending === 'load'" size="sm" />
    <DouButton v-else-if="!status && error" class="update-action" size="sm" @click="load">{{
      t('updates.retry')
    }}</DouButton>
    <template v-if="status">
      <div class="update-row">
        <DouText size="sm">{{ t('updates.version') }}</DouText>
        <DouText size="sm">{{
          status.version === 'dev' ? t('updates.dev') : status.version
        }}</DouText>
      </div>
      <div class="update-row update-channel-row">
        <DouText size="sm">{{ t('updates.channel') }}</DouText>
        <div class="update-channel">
          <DouSelect
            size="sm"
            :model-value="status.preferences.channel"
            :options="channels"
            :disabled="busy"
            :aria-label="t('updates.channel')"
            @update:model-value="setChannel"
          />
        </div>
      </div>
      <div v-if="status.canInstall" class="update-row">
        <DouText size="sm">{{ t('updates.automatic') }}</DouText>
        <DouSwitch
          size="sm"
          :model-value="status.preferences.automatic"
          :aria-label="t('updates.automatic')"
          :disabled="busy"
          @update:model-value="setAutomatic"
        />
      </div>
      <div v-if="status.state !== 'ready' && restartPhase === ''" class="update-actions">
        <DouButton
          class="update-action"
          size="sm"
          :disabled="busy"
          :loading="pending === 'check'"
          @click="check"
        >
          {{ t('updates.check') }}
        </DouButton>
        <DouButton
          v-if="installable || updating"
          class="update-action update-install"
          size="sm"
          tone="primary"
          :disabled="busy"
          :aria-busy="updating"
          @click="install"
        >
          <span aria-live="polite">{{ installLabel }}</span>
        </DouButton>
      </div>
      <div
        v-else-if="restartable || restartPhase === 'draining' || restartPhase === 'waiting_idle'"
        class="update-restart-section"
      >
        <div v-if="confirmingRestartNow && restartNowWarning" class="update-confirm-box">
          <DouText size="sm">{{ restartNowWarning }}</DouText>
          <div class="update-actions">
            <DouButton
              class="update-action"
              size="sm"
              tone="danger"
              :disabled="busy"
              :loading="pending === 'restartNow'"
              @click="restart('now')"
            >
              {{ t('updates.restartNowConfirm') }}
            </DouButton>
            <DouButton
              class="update-action"
              size="sm"
              variant="ghost"
              :disabled="busy"
              @click="dismissRestartNowConfirm"
            >
              {{ t('settings.cancel') }}
            </DouButton>
          </div>
        </div>
        <div v-else-if="restartPhase === 'draining'" class="update-actions">
          <DouButton
            class="update-action"
            size="sm"
            tone="primary"
            :disabled="pending === 'cancelRestart'"
            :loading="pending === 'cancelRestart'"
            @click="cancelRestart"
          >
            {{ t('updates.cancelDrain') }}
          </DouButton>
          <DouButton
            v-if="restartable"
            class="update-action"
            size="sm"
            :disabled="busy"
            :loading="pending === 'restartNow'"
            @click="requestRestartNow"
          >
            {{ t('updates.restartNow') }}
          </DouButton>
        </div>
        <div v-else-if="restartPhase === 'waiting_idle'" class="update-actions">
          <DouButton
            v-if="restartable"
            class="update-action"
            size="sm"
            tone="primary"
            :disabled="busy"
            :loading="pending === 'restartNow'"
            @click="requestRestartNow"
          >
            {{ t('updates.restartNow') }}
          </DouButton>
          <DouButton
            class="update-action"
            size="sm"
            :disabled="pending === 'cancelRestart'"
            :loading="pending === 'cancelRestart'"
            @click="cancelRestart"
          >
            {{ t('updates.cancelRestart') }}
          </DouButton>
        </div>
        <div v-else-if="restartPhase === '' && restartable" class="update-actions">
          <DouButton
            class="update-action"
            size="sm"
            tone="primary"
            :disabled="busy"
            :loading="pending === 'restart'"
            @click="restart('when-idle')"
          >
            {{ hasActiveWork ? t('updates.restart') : t('updates.restartApply') }}
          </DouButton>
          <DouButton
            v-if="hasActiveWork"
            class="update-action"
            size="sm"
            :disabled="busy"
            :loading="pending === 'restartNow'"
            @click="requestRestartNow"
          >
            {{ t('updates.restartNow') }}
          </DouButton>
        </div>
      </div>
    </template>
    <DouText
      v-if="message"
      size="sm"
      :tone="error || status?.failure ? 'danger' : undefined"
      role="status"
    >
      {{ message }}
    </DouText>
    <section v-if="release" class="update-notes">
      <div class="update-row">
        <DouText size="sm" aria-live="polite">
          {{
            t(status?.state === 'available' ? 'updates.available' : 'updates.notes', {
              version: release.version,
            })
          }}
        </DouText>
        <a :href="release.url" target="_blank" rel="noopener noreferrer" class="release-link">
          {{ t('updates.releasePage') }}
        </a>
      </div>
      <DouMarkdown
        v-if="release.notes"
        class="update-notes-body"
        :content="release.notes"
        :sanitize-url="releaseNoteURL"
        link-target="_blank"
      />
    </section>
  </div>
</template>

<style scoped>
.updates-settings,
.update-notes {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.update-row,
.update-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.update-restart-section,
.update-confirm-box {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.update-confirm-box {
  padding: 10px 12px;
  border: 1px solid var(--dou-color-border-subtle);
  border-radius: var(--dou-radius-control);
  background: var(--dou-color-bg-muted);
}

.update-row {
  justify-content: space-between;
  flex-wrap: wrap;
}

.update-channel-row {
  flex-wrap: nowrap;
}

.update-channel {
  flex: 0 1 140px;
  min-width: 0;
  margin-inline-start: auto;
}

.update-action {
  font-weight: var(--dou-font-weight-regular);
}

.update-install {
  font-variant-numeric: tabular-nums;
}

.update-notes {
  margin-top: 4px;
  padding-top: 12px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.update-notes-body {
  font-size: var(--dou-font-size-sm);
}

.release-link {
  color: var(--dou-color-text-secondary);
  font-size: var(--dou-font-size-sm, 13px);
}
</style>
