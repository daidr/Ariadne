<script setup lang="ts">
import { DouAlert, DouButton, DouChatInput, DouSpinner, DouText } from 'dou-ui'
import { computed, inject, nextTick, onMounted, ref, useTemplateRef, watch } from 'vue'
import { api, ApiError, type FoundSetup, type SourceKind } from '../api'
import { lt, t, tm } from '../i18n'
import { offerNotifications } from '../notify'
import { openSettingsKey } from '../open-settings'
import { applyModelSettings, report, startProject, state } from '../store'
import DirectoryInput from './DirectoryInput.vue'

// The first screen, and the one a new project starts from: say what you want and, if it is
// about files, where they are. Sending makes the project. Before any model is set up, it
// offers what this machine already has to talk to one with.
const openSettings = inject(openSettingsKey, () => {})

const found = ref<FoundSetup[] | null>(null)
const using = ref<string | null>(null)
const problem = ref<string | null>(null)

async function look() {
  try {
    found.value = (await api.foundSetups()).found
  } catch {
    found.value = []
  }
}
// Looked for again once the settings close: what was found may be in use by then.
watch(
  () => state.modelReady,
  (ready) => {
    if (ready === false) void look()
  },
  { immediate: true },
)

const where = (setup: FoundSetup) =>
  'env' in setup.from ? t('start.fromEnv', { name: setup.from.env }) : t('start.signedIn')

async function use(setup: FoundSetup) {
  using.value = setup.id
  problem.value = null
  try {
    const saved = await api.useFoundSetup(setup.id)
    applyModelSettings(saved)
    const [failed] = saved.problems
    if (failed) {
      // It is set up all the same; the settings show it, to be put right there.
      problem.value = tm(failed.problem)
    }
  } catch (cause) {
    problem.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
  } finally {
    using.value = null
  }
}

// Where the files are: the first kind of source that is a directory on this machine.
const kind = ref<SourceKind | null>(null)
const pathKey = computed(() => {
  const fields = Object.entries(kind.value?.settings.properties ?? {})
  return fields.find(([, field]) => field.type === 'string' && field.directory)?.[0] ?? null
})
onMounted(async () => {
  try {
    const { kinds } = await api.sourceKinds()
    kind.value = kinds.find((k) => k.browse && !k.remote && !k.hosting) ?? null
  } catch {
    // Without it the project starts with no source; its settings take one later.
  }
})

const draft = ref('')
const directory = ref('')
const sending = ref(false)
const input = useTemplateRef<{ focus: () => void }>('input')
watch(
  () => state.modelReady,
  async (ready) => {
    if (!ready) return
    await nextTick()
    input.value?.focus()
  },
  { immediate: true },
)

function browse(typed: string) {
  if (!kind.value || !pathKey.value) return Promise.resolve(null)
  return api
    .browseSource(kind.value.kind, {
      projectId: null,
      settings: { [pathKey.value]: typed },
      path: typed,
    })
    .then((result) => result.listing)
}

/** The last part of a path, whichever way its separators lean. */
const lastPart = (path: string) =>
  path
    .replace(/[\\/]+$/, '')
    .split(/[\\/]/)
    .pop() ?? ''

async function submit(text: string) {
  const message = text.trim()
  if (!message || sending.value) return
  const path = directory.value.trim()
  sending.value = true
  const source =
    path && kind.value && pathKey.value
      ? { kind: kind.value.kind, settings: { [pathKey.value]: path } }
      : null
  const name = source ? lastPart(path) : ''
  const started = await startProject({
    message,
    ...(source ? { source } : {}),
    ...(name ? { name } : {}),
  }).catch((error) => {
    report(error)
    return false
  })
  sending.value = false
  if (!started) return
  draft.value = ''
  directory.value = ''
  offerNotifications()
}
</script>

<template>
  <div class="start">
    <section v-if="state.modelReady === false" class="start-box" :aria-label="t('start.model')">
      <DouText weight="medium">{{ t('start.model') }}</DouText>
      <DouSpinner v-if="found === null" size="sm" />
      <ul v-else-if="found.length" class="start-found">
        <li v-for="setup in found" :key="setup.id" class="start-setup">
          <span class="start-setup-name">
            <DouText>{{ lt(setup.title) ?? setup.kind }}</DouText>
            <DouText variant="secondary" size="sm">{{ where(setup) }}</DouText>
          </span>
          <DouButton
            size="sm"
            tone="primary"
            :loading="using === setup.id"
            :disabled="using !== null && using !== setup.id"
            @click="use(setup)"
          >
            {{ t('start.use') }}
          </DouButton>
        </li>
      </ul>
      <DouAlert v-if="problem" tone="danger">{{ problem }}</DouAlert>
      <div>
        <DouButton
          size="sm"
          :tone="found?.length ? undefined : 'primary'"
          :variant="found?.length ? 'ghost' : 'solid'"
          @click="openSettings()"
        >
          {{ t(found?.length ? 'start.addOther' : 'start.add') }}
        </DouButton>
      </div>
    </section>
    <section v-else-if="state.modelReady" class="start-box" :aria-label="t('nav.newProject')">
      <DouText size="lg" weight="medium">{{ t('start.ask') }}</DouText>
      <DouAlert v-if="problem" tone="warning" closable @close="problem = null">
        {{ problem }}
        <template #action>
          <DouButton size="sm" variant="outline" @click="openSettings()">
            {{ t('conversation.openSettings') }}
          </DouButton>
        </template>
      </DouAlert>
      <DouChatInput
        ref="input"
        v-model="draft"
        :submit-on="state.input.submitOn"
        :placeholder="t('start.example')"
        :min-rows="3"
        :disabled="sending"
        @submit="submit"
      />
      <DirectoryInput
        v-if="kind && pathKey"
        v-model="directory"
        :browse="browse"
        :placeholder="t('start.directory')"
        :aria-label="t('start.directory')"
        size="sm"
      />
    </section>
  </div>
</template>

<style scoped>
.start {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  min-height: 0;
  padding: 24px;
  /* A little above the middle, where the eye rests. */
  padding-bottom: 12vh;
}

.start-box {
  display: flex;
  flex-direction: column;
  gap: 12px;
  width: min(560px, 100%);
}

.start-found {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.start-setup {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.start-setup-name {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}
</style>
