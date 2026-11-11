<script setup lang="ts">
import { DouAlert, DouButton, DouSpinner, DouText } from 'dou-ui'
import { inject, ref, useTemplateRef, watch } from 'vue'
import { api, ApiError, type FoundSetup } from '../api'
import { lt, t, tm } from '../i18n'
import { openSettingsKey } from '../open-settings'
import { applyModelSettings, state } from '../store'
import NewProjectForm from './NewProjectForm.vue'

// The first screen, and the one a new project is made on: before any model is set up, what
// this machine already has to talk to one with; then the project's form.
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

const form = useTemplateRef<InstanceType<typeof NewProjectForm>>('form')
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
      <DouText size="lg" weight="medium">{{ t('nav.newProject') }}</DouText>
      <DouAlert v-if="problem" tone="warning" closable @close="problem = null">
        {{ problem }}
        <template #action>
          <DouButton size="sm" variant="outline" @click="openSettings()">
            {{ t('conversation.openSettings') }}
          </DouButton>
        </template>
      </DouAlert>
      <NewProjectForm ref="form" />
      <div class="start-actions">
        <DouButton v-if="state.currentId" variant="ghost" @click="state.starting = false">
          {{ t('settings.cancel') }}
        </DouButton>
        <DouButton
          tone="primary"
          :loading="form?.creating"
          :disabled="!form?.ready"
          @click="form?.create()"
        >
          {{ t('newProject.create') }}
        </DouButton>
      </div>
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
  width: min(480px, 100%);
}

.start-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
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
