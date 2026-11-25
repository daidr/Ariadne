<script setup lang="ts">
import {
  DouAlert,
  DouButton,
  DouCard,
  DouEmpty,
  DouForm,
  DouFormItem,
  DouInput,
  DouSwitch,
  DouTag,
  DouText,
  toast,
} from 'dou-ui'
import { BellIcon, PlugIcon, PlusIcon, SendIcon, TrashIcon } from '@dou-ui/icons'
import { computed, onMounted, ref, watch } from 'vue'
import {
  api,
  ApiError,
  type NotificationSettings,
  type NotificationSettingsInput,
  type NotifierServiceInput,
  type NotifierServiceView,
  type PushSubscriptionView,
  type TestNotificationResult,
  type WebhookInput,
} from '../api'
import { locale, lt, t, tm } from '../i18n'
import {
  notifications as browserNotifications,
  setNotifications as setBrowserNotifications,
} from '../notify'
import { pluginRevision } from '../plugin-config'
import {
  detectPushSupport,
  pushState,
  subscribeCurrentDevice,
  syncBrowserPushState,
  unsubscribeCurrentDevice,
} from '../push'
import { report } from '../store'
import SecretPairs, { pairsInput, storedPairs, type DraftPair } from './SecretPairs.vue'
import SettingsFields from './SettingsFields.vue'

interface DraftWebhook {
  id: string
  name: string
  url: string
  enabled: boolean
  secret: string
  secretSet: boolean
  clearSecret: boolean
  headers: DraftPair[]
}

const emit = defineEmits<{ openPlugins: [] }>()

const loaded = ref<NotificationSettings | null>(null)
const enabled = ref(true)
const webPushEnabled = ref(true)
const vapidPublicKey = ref('')
const subscriptions = ref<PushSubscriptionView[]>([])
const webhooks = ref<DraftWebhook[]>([])
const notifiers = ref<NotifierServiceView[]>([])
const notifierEnabled = ref<Record<string, boolean>>({})
const notifierDrafts = ref<Record<string, Record<string, unknown>>>({})

const error = ref<string | null>(null)
const testingKey = ref<string | null>(null)
const removingSubId = ref<string | null>(null)
const savedSnapshot = ref('')

const support = computed(() => detectPushSupport())
const currentEndpoint = computed(() => pushState.endpoint)
const isCurrentSubscribed = computed(() =>
  Boolean(
    currentEndpoint.value &&
    subscriptions.value.some((sub) => sub.endpoint === currentEndpoint.value),
  ),
)

const pushNotice = computed<{ tone: 'warning' | 'primary'; text: string } | null>(() => {
  if (!support.value.supported) {
    if (support.value.reason === 'insecure') {
      return { tone: 'warning', text: t('notifications.webPush.insecure') }
    }
    if (support.value.reason === 'ios-install') {
      return { tone: 'primary', text: t('notifications.webPush.iosInstall') }
    }
    return { tone: 'warning', text: t('notifications.webPush.unsupported') }
  }
  if (pushState.permission === 'denied') {
    return { tone: 'warning', text: t('notifications.webPush.denied') }
  }
  return null
})

function draftForNotifier(service: NotifierServiceView): Record<string, unknown> {
  const values: Record<string, unknown> = {}
  for (const [key, field] of Object.entries(service.settings.properties ?? {})) {
    if (field.type === 'string' && field.secret) {
      values[key] = ''
      continue
    }
    if (key in service.values) {
      values[key] = service.values[key]
    }
  }
  return values
}

function payloadForNotifier(service: NotifierServiceView): NotifierServiceInput {
  const draft = notifierDrafts.value[service.kind] ?? {}
  const values: Record<string, unknown> = {}
  for (const [key, field] of Object.entries(service.settings.properties ?? {})) {
    const value = draft[key]
    if (field.type === 'string' && field.secret) {
      if (value === null) {
        values[key] = null
      } else if (typeof value === 'string' && value.trim() !== '') {
        values[key] = value.trim()
      }
      continue
    }
    if (value !== undefined) {
      values[key] = value
    }
  }
  return {
    enabled: Boolean(notifierEnabled.value[service.kind]),
    values,
  }
}

function secretsSetForNotifier(service: NotifierServiceView): string[] {
  const draft = notifierDrafts.value[service.kind] ?? {}
  return service.secretsSet.filter((key) => draft[key] !== null)
}

function hasNotifierSettings(service: NotifierServiceView): boolean {
  return Object.keys(service.settings.properties ?? {}).length > 0
}

function webhookToInput(hook: DraftWebhook): WebhookInput {
  const secret = hook.secret.trim()
  return {
    id: hook.id,
    name: hook.name.trim(),
    url: hook.url.trim(),
    enabled: hook.enabled,
    ...(secret ? { secret } : {}),
    clearSecret: hook.clearSecret && !secret,
    headers: pairsInput(hook.headers),
  }
}

function isBlankWebhook(hook: DraftWebhook): boolean {
  return (
    !hook.name.trim() &&
    !hook.url.trim() &&
    !hook.secret.trim() &&
    !hook.secretSet &&
    hook.headers.length === 0
  )
}

function buildInput(): NotificationSettingsInput {
  const notifierMap: Record<string, NotifierServiceInput> = {}
  for (const service of notifiers.value) {
    notifierMap[service.kind] = payloadForNotifier(service)
  }
  return {
    enabled: enabled.value,
    webPushEnabled: webPushEnabled.value,
    webhooks: webhooks.value.filter((hook) => !isBlankWebhook(hook)).map(webhookToInput),
    notifiers: notifierMap,
  }
}

function currentSnapshot(): string {
  return JSON.stringify(buildInput())
}

const dirty = computed(() => loaded.value !== null && currentSnapshot() !== savedSnapshot.value)

function load(data: NotificationSettings, preserve = false) {
  loaded.value = data
  vapidPublicKey.value = data.vapidPublicKey
  subscriptions.value = data.subscriptions
  notifiers.value = data.notifiers

  if (!preserve) {
    enabled.value = data.enabled
    webPushEnabled.value = data.webPushEnabled
    webhooks.value = data.webhooks.map((hook) => ({
      id: hook.id,
      name: hook.name,
      url: hook.url,
      enabled: hook.enabled,
      secret: '',
      secretSet: hook.secretSet,
      clearSecret: false,
      headers: storedPairs(hook.headers),
    }))
    notifierEnabled.value = Object.fromEntries(
      data.notifiers.map((service) => [service.kind, service.enabled]),
    )
    notifierDrafts.value = Object.fromEntries(
      data.notifiers.map((service) => [service.kind, draftForNotifier(service)]),
    )
    savedSnapshot.value = currentSnapshot()
  } else {
    const nextEnabled: Record<string, boolean> = {}
    const nextDrafts: Record<string, Record<string, unknown>> = {}
    for (const service of data.notifiers) {
      nextEnabled[service.kind] =
        service.kind in notifierEnabled.value
          ? Boolean(notifierEnabled.value[service.kind])
          : service.enabled
      nextDrafts[service.kind] = notifierDrafts.value[service.kind] ?? draftForNotifier(service)
    }
    notifierEnabled.value = nextEnabled
    notifierDrafts.value = nextDrafts
  }

  void syncBrowserPushState({
    subscriptions: data.subscriptions,
    webPushActive: data.enabled && data.webPushEnabled,
  })
}

async function reload(preserve = false) {
  error.value = null
  try {
    load(await api.getNotificationSettings(), preserve)
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  }
}

onMounted(() => void reload(false))
watch(pluginRevision, () => {
  if (loaded.value) void reload(true)
})

function addWebhook() {
  const id = `wh_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`
  webhooks.value.push({
    id,
    name: '',
    url: '',
    enabled: true,
    secret: '',
    secretSet: false,
    clearSecret: false,
    headers: [],
  })
}

function removeWebhook(index: number) {
  webhooks.value.splice(index, 1)
}

function clearWebhookSecret(hook: DraftWebhook) {
  hook.secret = ''
  hook.secretSet = false
  hook.clearSecret = true
}

function clearNotifierSecret(kind: string, key: string) {
  const draft = (notifierDrafts.value[kind] ??= {})
  draft[key] = null
}

function formatTime(ms: number | null): string {
  if (!ms) return t('notifications.webPush.neverUsed')
  try {
    return t('notifications.webPush.lastUsed', {
      time: new Intl.DateTimeFormat(locale.value, {
        dateStyle: 'short',
        timeStyle: 'short',
      }).format(new Date(ms)),
    })
  } catch {
    return t('notifications.webPush.lastUsed', { time: new Date(ms).toLocaleString() })
  }
}

async function handleSubscribe() {
  error.value = null
  try {
    const result = await subscribeCurrentDevice(vapidPublicKey.value)
    if (!result.ok) {
      if (result.reason === 'denied') {
        report(t('notifications.webPush.denied'))
      } else if (result.reason === 'ios-install') {
        report(t('notifications.webPush.iosInstall'))
      } else if (result.reason === 'insecure') {
        report(t('notifications.webPush.insecure'))
      } else {
        report(t('notifications.webPush.unsupported'))
      }
      return
    }
    subscriptions.value = result.subscriptions
    await syncBrowserPushState({
      subscriptions: result.subscriptions,
      webPushActive: enabled.value && webPushEnabled.value,
    })
    toast.success(t('notifications.webPush.subscribeOk'))
  } catch (cause) {
    report(cause)
  }
}

async function handleUnsubscribe() {
  error.value = null
  try {
    const next = await unsubscribeCurrentDevice()
    subscriptions.value = next
    toast.success(t('notifications.webPush.unsubscribeOk'))
  } catch (cause) {
    report(cause)
  }
}

async function removeSubscription(sub: PushSubscriptionView) {
  removingSubId.value = sub.id
  error.value = null
  try {
    if (sub.endpoint === currentEndpoint.value) {
      subscriptions.value = await unsubscribeCurrentDevice()
    } else {
      const response = await api.deletePushSubscription(sub.id)
      subscriptions.value = response.subscriptions
    }
    await syncBrowserPushState({
      subscriptions: subscriptions.value,
      webPushActive: enabled.value && webPushEnabled.value,
    })
  } catch (cause) {
    report(cause)
  } finally {
    removingSubId.value = null
  }
}

function reportTestResult(result: TestNotificationResult) {
  const detail = lt(result.message)
  if (result.ok) {
    toast.success(detail || t('notifications.testOk'))
  } else {
    report(detail || t('notifications.testFailed'))
  }
}

async function testWebPush(targetId?: string) {
  const key = targetId ? `webpush:${targetId}` : 'webpush'
  testingKey.value = key
  error.value = null
  try {
    const result = await api.testNotification({
      channel: 'webpush',
      ...(targetId ? { targetId } : {}),
    })
    reportTestResult(result)
    // Refresh subscription list so updated lastUsedAt / lastError / 410 auto-cleanup show immediately.
    await reload(true)
  } catch (cause) {
    report(cause)
  } finally {
    testingKey.value = null
  }
}

async function testWebhook(hook: DraftWebhook) {
  testingKey.value = `webhook:${hook.id}`
  error.value = null
  try {
    const result = await api.testNotification({
      channel: 'webhook',
      targetId: hook.id,
      webhookDraft: webhookToInput(hook),
    })
    reportTestResult(result)
  } catch (cause) {
    report(cause)
  } finally {
    testingKey.value = null
  }
}

async function testNotifier(service: NotifierServiceView) {
  testingKey.value = `notifier:${service.kind}`
  error.value = null
  try {
    const result = await api.testNotification({
      channel: 'notifier',
      targetId: service.kind,
      notifierDraft: payloadForNotifier(service),
    })
    reportTestResult(result)
  } catch (cause) {
    report(cause)
  } finally {
    testingKey.value = null
  }
}

async function save(): Promise<boolean> {
  if (!loaded.value) return true
  if (!dirty.value) return true
  error.value = null
  try {
    const saved = await api.saveNotificationSettings(buildInput())
    load(saved, false)
    return true
  } catch (cause) {
    error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    return false
  }
}

defineExpose({ save, dirty, reload })
</script>

<template>
  <div class="notif-settings">
    <template v-if="loaded">
      <div class="notif-master">
        <div class="notif-heading">
          <DouText weight="medium">{{ t('notifications.enabled') }}</DouText>
          <DouText size="sm" variant="secondary">{{ t('notifications.enabledHint') }}</DouText>
        </div>
        <DouSwitch v-model="enabled" :aria-label="t('notifications.enabled')" />
      </div>

      <!-- Channel 1: PWA Web Push -->
      <section class="notif-section">
        <DouCard padding="sm">
          <div class="notif-card-head">
            <div class="notif-heading">
              <div class="notif-title-line">
                <BellIcon class="notif-icon" />
                <DouText weight="medium">{{ t('notifications.webPush.title') }}</DouText>
              </div>
              <DouText size="sm" variant="secondary">{{ t('notifications.webPush.hint') }}</DouText>
            </div>
            <DouSwitch
              v-model="webPushEnabled"
              :disabled="!enabled"
              :aria-label="t('notifications.webPush.enable')"
            />
          </div>

          <DouAlert v-if="pushNotice" :tone="pushNotice.tone" class="notif-alert">
            {{ pushNotice.text }}
          </DouAlert>

          <div class="notif-push-actions">
            <template v-if="isCurrentSubscribed">
              <DouTag tone="success" size="sm">{{ t('notifications.webPush.subscribed') }}</DouTag>
              <DouButton
                size="sm"
                variant="soft"
                :loading="testingKey === 'webpush'"
                :disabled="!enabled || !webPushEnabled || testingKey !== null"
                @click="testWebPush()"
              >
                <template #leading><SendIcon /></template>
                {{ t('notifications.webPush.testAll') }}
              </DouButton>
              <DouButton
                size="sm"
                variant="ghost"
                tone="danger"
                :loading="pushState.busy"
                :disabled="pushState.busy"
                @click="handleUnsubscribe"
              >
                {{ t('notifications.webPush.unsubscribe') }}
              </DouButton>
            </template>
            <template v-else>
              <DouButton
                size="sm"
                tone="primary"
                :loading="pushState.busy"
                :disabled="
                  pushState.busy ||
                  !support.supported ||
                  pushState.permission === 'denied' ||
                  !vapidPublicKey
                "
                @click="handleSubscribe"
              >
                <template #leading><BellIcon /></template>
                {{ t('notifications.webPush.subscribe') }}
              </DouButton>
              <DouButton
                v-if="subscriptions.length"
                size="sm"
                variant="soft"
                :loading="testingKey === 'webpush'"
                :disabled="!enabled || !webPushEnabled || testingKey !== null"
                @click="testWebPush()"
              >
                <template #leading><SendIcon /></template>
                {{ t('notifications.webPush.testAll') }}
              </DouButton>
            </template>
          </div>

          <div v-if="subscriptions.length" class="notif-devices">
            <div v-for="sub in subscriptions" :key="sub.id" class="notif-device">
              <div class="notif-device-info">
                <div class="notif-device-name">
                  <DouText size="sm" weight="medium">{{ sub.label || sub.endpoint }}</DouText>
                  <DouTag v-if="sub.endpoint === currentEndpoint" size="sm" tone="primary">
                    {{ t('notifications.webPush.currentDevice') }}
                  </DouTag>
                </div>
                <DouText size="sm" variant="secondary">{{ formatTime(sub.lastUsedAt) }}</DouText>
                <DouText v-if="sub.lastError" size="sm" tone="danger">{{ sub.lastError }}</DouText>
              </div>
              <div class="notif-device-actions">
                <DouButton
                  size="xs"
                  variant="ghost"
                  :loading="testingKey === `webpush:${sub.id}`"
                  :disabled="!enabled || !webPushEnabled || testingKey !== null"
                  @click="testWebPush(sub.id)"
                >
                  {{ t('notifications.webPush.testDevice') }}
                </DouButton>
                <DouButton
                  size="xs"
                  variant="ghost"
                  tone="danger"
                  :loading="removingSubId === sub.id"
                  :disabled="removingSubId !== null"
                  :aria-label="t('notifications.webPush.removeDevice')"
                  :title="t('notifications.webPush.removeDevice')"
                  @click="removeSubscription(sub)"
                >
                  <TrashIcon />
                </DouButton>
              </div>
            </div>
          </div>
          <DouEmpty
            v-else
            size="sm"
            :description="t('notifications.webPush.empty')"
            class="notif-empty"
          />

          <div class="notif-local-fallback">
            <div class="notif-heading">
              <DouText size="sm">{{ t('notifications.browserLocal') }}</DouText>
              <DouText size="sm" variant="secondary">
                {{ t('notifications.browserLocalHint') }}
              </DouText>
            </div>
            <DouSwitch
              :model-value="browserNotifications.enabled"
              :disabled="
                browserNotifications.permission === 'unsupported' ||
                browserNotifications.permission === 'denied'
              "
              :aria-label="t('notifications.browserLocal')"
              @update:model-value="(on: boolean) => setBrowserNotifications(on)"
            />
          </div>
        </DouCard>
      </section>

      <!-- Channel 2: Generic Webhooks -->
      <section class="notif-section">
        <div class="notif-section-head">
          <div class="notif-heading">
            <DouText weight="medium">{{ t('notifications.webhook.title') }}</DouText>
            <DouText size="sm" variant="secondary">{{ t('notifications.webhook.hint') }}</DouText>
          </div>
          <DouButton size="sm" variant="ghost" @click="addWebhook">
            <template #leading><PlusIcon /></template>
            {{ t('notifications.webhook.add') }}
          </DouButton>
        </div>

        <DouEmpty
          v-if="!webhooks.length"
          size="sm"
          :description="t('notifications.webhook.empty')"
        />

        <DouCard v-for="(hook, index) in webhooks" :key="hook.id" padding="sm">
          <div class="notif-hook-head">
            <DouInput
              v-model="hook.name"
              size="sm"
              class="notif-hook-name"
              :placeholder="t('notifications.webhook.namePlaceholder')"
              :aria-label="t('notifications.webhook.name')"
            />
            <div class="notif-hook-actions">
              <DouSwitch v-model="hook.enabled" :aria-label="t('notifications.webhook.name')" />
              <DouButton
                size="sm"
                variant="ghost"
                :loading="testingKey === `webhook:${hook.id}`"
                :disabled="!hook.url.trim() || testingKey !== null"
                @click="testWebhook(hook)"
              >
                <template #leading><SendIcon /></template>
                {{ t('notifications.webhook.test') }}
              </DouButton>
              <DouButton
                size="sm"
                variant="ghost"
                tone="danger"
                :aria-label="t('settings.remove')"
                :title="t('settings.remove')"
                @click="removeWebhook(index)"
              >
                <TrashIcon />
              </DouButton>
            </div>
          </div>

          <DouForm layout="vertical" size="sm" class="notif-hook-form">
            <DouFormItem :label="t('notifications.webhook.url')" required>
              <DouInput
                v-model="hook.url"
                :placeholder="t('notifications.webhook.urlPlaceholder')"
              />
            </DouFormItem>
            <DouFormItem :label="t('notifications.webhook.secret')">
              <DouInput
                v-model="hook.secret"
                type="password"
                show-password
                :placeholder="
                  hook.secretSet && !hook.clearSecret
                    ? t('settings.secretSet')
                    : t('notifications.webhook.secretPlaceholder')
                "
                @update:model-value="
                  (val) => {
                    if (val) hook.clearSecret = false
                  }
                "
              >
                <template v-if="hook.secretSet && !hook.clearSecret" #suffix>
                  <DouButton size="xs" variant="ghost" @click="clearWebhookSecret(hook)">
                    {{ t('settings.clearSecret') }}
                  </DouButton>
                </template>
              </DouInput>
            </DouFormItem>
            <DouFormItem :label="t('notifications.webhook.headers')">
              <SecretPairs v-model="hook.headers" />
            </DouFormItem>
          </DouForm>
        </DouCard>
      </section>

      <!-- Channel 3: Notifier Plugins (Lua / Built-in) -->
      <section class="notif-section">
        <div class="notif-section-head">
          <div class="notif-heading">
            <DouText weight="medium">{{ t('notifications.plugins.title') }}</DouText>
            <DouText size="sm" variant="secondary">{{ t('notifications.plugins.hint') }}</DouText>
          </div>
          <DouButton size="sm" variant="ghost" @click="emit('openPlugins')">
            <template #leading><PlugIcon /></template>
            {{ t('notifications.plugins.openPlugins') }}
          </DouButton>
        </div>

        <DouEmpty
          v-if="!notifiers.length"
          size="sm"
          :description="t('notifications.plugins.empty')"
        />

        <DouCard v-for="service in notifiers" :key="service.kind" padding="sm">
          <div class="notif-card-head">
            <div class="notif-heading">
              <div class="notif-title-line">
                <DouText weight="medium">{{ lt(service.title) ?? service.kind }}</DouText>
                <DouTag size="sm" :tone="service.ready ? 'success' : 'warning'">
                  {{
                    t(
                      service.ready
                        ? 'notifications.plugins.ready'
                        : 'notifications.plugins.needsConfig',
                    )
                  }}
                </DouTag>
              </div>
              <DouText
                v-if="service.description && lt(service.description)"
                size="sm"
                variant="secondary"
              >
                {{ lt(service.description) }}
              </DouText>
            </div>
            <div class="notif-hook-actions">
              <DouSwitch
                v-model="notifierEnabled[service.kind]"
                :disabled="!enabled"
                :aria-label="lt(service.title) ?? service.kind"
              />
              <DouButton
                size="sm"
                variant="ghost"
                :loading="testingKey === `notifier:${service.kind}`"
                :disabled="testingKey !== null"
                @click="testNotifier(service)"
              >
                <template #leading><SendIcon /></template>
                {{ t('notifications.plugins.test') }}
              </DouButton>
            </div>
          </div>

          <DouForm
            v-if="hasNotifierSettings(service)"
            layout="vertical"
            size="sm"
            class="notif-hook-form"
          >
            <SettingsFields
              :schema="service.settings"
              :values="notifierDrafts[service.kind] ?? {}"
              :secrets-set="secretsSetForNotifier(service)"
              @clear-secret="(key) => clearNotifierSecret(service.kind, key)"
            />
          </DouForm>
        </DouCard>
      </section>
    </template>

    <DouAlert v-if="error" tone="danger">{{ error }}</DouAlert>
    <DouButton v-if="!loaded" size="sm" @click="reload(false)">
      {{ t('view.retry') }}
    </DouButton>
  </div>
</template>

<style scoped>
.notif-settings {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.notif-master {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.notif-section {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.notif-section-head,
.notif-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.notif-heading {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.notif-title-line {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.notif-icon {
  flex: none;
  width: 15px;
  height: 15px;
  color: var(--dou-color-text-secondary);
}

.notif-alert {
  margin-top: 10px;
}

.notif-push-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}

.notif-devices {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.notif-device {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.notif-device-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.notif-device-name {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.notif-device-actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: none;
}

.notif-empty {
  margin-top: 8px;
}

.notif-local-fallback {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--dou-color-border-subtle);
}

.notif-hook-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.notif-hook-name {
  flex: 1 1 180px;
  min-width: 0;
}

.notif-hook-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

.notif-hook-form {
  margin-top: 10px;
}
</style>
