<script setup lang="ts">
import type { MergeMethod, ProjectSettings } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouCheckTagGroup,
  DouEmpty,
  DouDialog,
  DouForm,
  DouFormItem,
  DouInput,
  DouInputNumber,
  DouSelect,
  DouSwitch,
  DouTabPanel,
  DouTabs,
  DouText,
  DouTextarea,
  DouTimePicker,
} from 'dou-ui'
import { PlusIcon, TrashIcon } from '@dou-ui/icons'
import { computed, inject, ref, watch } from 'vue'
import { api, ApiError, type McpServerView } from '../api'
import { clockText, t, tm } from '../i18n'
import { nextRunOf, WEEK } from './routines'
import { saveProjectSettings, state } from '../store'
import { openSettingsKey } from '../open-settings'
import SecretPairs, { pairsInput, storedPairs, type DraftPair } from './SecretPairs.vue'
import SourceForm from './SourceForm.vue'
import SourceNotes from './SourceNotes.vue'
import MemoryList from './MemoryList.vue'
import ProjectUsage from './ProjectUsage.vue'

/**
 * The open project's own settings: where its files are, instructions, workspaces and MCP,
 * and the review, autopilot, budgets and routines. What the page no longer shows (tool
 * switches, the shell, the branch prefix, the project's own concurrency) is saved back as
 * it was.
 */

const tab = computed({
  get: () => state.projectSettingsTab,
  set: (value) => (state.projectSettingsTab = value),
})
const source = ref<InstanceType<typeof SourceForm> | null>(null)
const settings = ref<ProjectSettings | null>(null)
const env = ref<DraftPair[]>([])
/** Budgets in thousands of tokens, as typed; empty is no cap. */
const threadK = ref<number | null>(null)
const projectK = ref<number | null>(null)
const slots = ref<number | null>(null)
const mcp = ref<string[]>([])
const mcpServers = ref<McpServerView[]>([])
/** How many turns may run at once in all, the cap of a project without its own. */
const totalSlots = ref(3)
const saving = ref(false)
const error = ref<string | null>(null)
/**
 * The setup script and the check as the fields show them. Left to Ariadne, a field shows
 * what it found (`found`), and saved as it stands it stays Ariadne's; changed or emptied,
 * it is the user's own from then on.
 */
const setup = ref('')
const check = ref('')
let found = { setup: '', check: '' }

// Where merging happens in the app, the autopilot merges the way the user last did. A hosted
// repository is merged on its platform, so there the way is chosen here.
const hostedMerge = ref(false)
const methodOptions = (['squash', 'merge'] as MergeMethod[]).map((method) => ({
  value: method,
  label: t(`changes.method.${method}`),
}))
/** The routines as loaded: left alone, a save keeps what the coordinator set meanwhile. */
let loadedRoutines = ''

// The autopilot merges what the review let through: one goes on with the other.
function setReview(on: boolean) {
  if (!settings.value) return
  settings.value.review.enabled = on
  if (!on) settings.value.autopilot.enabled = false
}
function setAutopilot(on: boolean) {
  if (!settings.value) return
  settings.value.autopilot.enabled = on
  if (on) settings.value.review.enabled = true
}

/**
 * The plan nodes the autopilot would start on right away: tasks with nothing open before
 * them and no thread yet, up to the project's limit on concurrent work.
 */
const startsNow = computed(() => {
  const plan = state.plan
  if (!plan) return []
  const status = new Map(plan.nodes.map((node) => [node.id, node.status]))
  const asked = new Set(
    plan.proposals
      .filter((proposal) => proposal.status === 'open')
      .flatMap((proposal) =>
        proposal.ops.flatMap((op) => ('id' in op ? [op.id] : [op.from, op.to])),
      ),
  )
  const atWork =
    state.detail?.threads.filter(
      (thread) => !thread.reviewOf && thread.stage !== 'done' && thread.stage !== 'stopped',
    ).length ?? 0
  const room = Math.max(0, (slots.value || totalSlots.value) - atWork)
  return plan.nodes
    .filter(
      (node) =>
        node.kind === 'task' &&
        !node.threads.length &&
        !asked.has(node.id) &&
        node.dependsOn.every((id) => status.get(id) === 'done'),
    )
    .slice(0, room)
    .map((node) => node.title)
})

const openSettings = inject(openSettingsKey, () => {})
function openMcp() {
  state.projectSettingsOpen = false
  openSettings('mcp')
}

const mcpOptions = computed(() =>
  mcpServers.value.map((server) => ({ value: server.id, label: server.name })),
)

const toK = (tokens: number | null) => (tokens ? Math.round(tokens / 1000) : null)

watch(
  () => state.projectSettingsOpen,
  async (open) => {
    const project = state.detail?.project
    if (!open || !project) return
    error.value = null
    settings.value = null
    now.value = Date.now()
    slots.value = project.slots
    mcp.value = [...project.mcp]
    try {
      const [view, servers, concurrency] = await Promise.all([
        api.getProjectSettings(project.id),
        api.getMcpSettings().catch(() => ({ servers: [] as McpServerView[] })),
        api.getConcurrency().catch(() => null),
      ])
      if (concurrency) totalSlots.value = concurrency.slots
      settings.value = structuredClone(view.settings)
      hostedMerge.value = view.hostedMerge
      loadedRoutines = JSON.stringify(view.settings.routines)
      const { auto } = view.settings.workspace
      found = { setup: view.detected.setup ?? '', check: view.detected.check?.command ?? '' }
      setup.value = auto.setup ? found.setup : view.settings.workspace.setup
      check.value = auto.check ? found.check : view.settings.workspace.check
      env.value = storedPairs(view.env)
      threadK.value = toK(view.settings.budget.thread)
      projectK.value = toK(view.settings.budget.project)
      mcpServers.value = servers.servers
    } catch (cause) {
      error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    }
  },
)

// A routine's days as tags, Monday first; its next run as it would be once saved.
const dayOptions = computed(() =>
  WEEK.map((day) => ({ value: day, label: t(`routine.day.${day}`) })),
)
const now = ref(Date.now())
function nextText(routine: ProjectSettings['routines'][number]): string {
  if (!routine.enabled) return t('routine.off')
  const next = nextRunOf(routine, now.value)
  return next === null ? t('routine.noDays') : t('routine.next', { time: clockText(next) })
}
function removeRoutine(id: string) {
  if (settings.value) settings.value.routines = settings.value.routines.filter((r) => r.id !== id)
}

async function save() {
  if (!settings.value) return
  saving.value = true
  error.value = null
  // The source first: one refused keeps the dialog open on it.
  if (source.value && !(await source.value.save())) {
    tab.value = 'source'
    saving.value = false
    return
  }
  const { workspace } = settings.value
  const auto = {
    setup: workspace.auto.setup && setup.value.trim() === found.setup,
    check: workspace.auto.check && check.value.trim() === found.check,
  }
  const next: ProjectSettings = {
    ...settings.value,
    workspace: {
      ...workspace,
      auto,
      setup: auto.setup ? '' : setup.value,
      check: auto.check ? '' : check.value,
    },
    routines:
      JSON.stringify(settings.value.routines) === loadedRoutines
        ? (state.detail?.project.settings.routines ?? settings.value.routines)
        : // One left without words does nothing; it goes rather than failing the save.
          settings.value.routines.filter((routine) => routine.prompt.trim()),
    budget: {
      thread: threadK.value ? threadK.value * 1000 : null,
      project: projectK.value ? projectK.value * 1000 : null,
    },
  }
  error.value = await saveProjectSettings(
    { settings: next, env: pairsInput(env.value) },
    slots.value || null,
    mcp.value,
  )
  saving.value = false
  if (!error.value) state.projectSettingsOpen = false
}
</script>

<template>
  <DouDialog
    v-model:open="state.projectSettingsOpen"
    :title="t('projectSettings.title', { name: state.detail?.project.name ?? '' })"
    :width="760"
  >
    <DouTabs v-if="settings" v-model="tab" placement="start" :height="560">
      <DouTabPanel value="source" :label="t('projectSettings.tab.source')">
        <div class="project-stack">
          <SourceForm ref="source" />
          <SourceNotes />
        </div>
      </DouTabPanel>
      <DouTabPanel value="instructions" :label="t('projectSettings.tab.instructions')">
        <div class="project-stack">
          <DouTextarea
            v-model="settings.instructions"
            :autosize="{ minRows: 6, maxRows: 14 }"
            :maxlength="16000"
            :placeholder="t('projectSettings.instructionsPlaceholder')"
            :aria-label="t('projectSettings.tab.instructions')"
          />
          <DouText weight="medium" size="sm">{{ t('memory.title') }}</DouText>
          <MemoryList />
        </div>
      </DouTabPanel>
      <DouTabPanel value="workspace" :label="t('projectSettings.tab.workspace')">
        <DouForm layout="vertical" size="sm">
          <DouFormItem :label="t('projectSettings.setup')">
            <DouTextarea
              v-model="setup"
              :autosize="{ minRows: 2, maxRows: 8 }"
              :maxlength="8000"
              placeholder="bun install"
              class="project-code"
            />
          </DouFormItem>
          <DouFormItem
            :label="t('projectSettings.check')"
            :help="t('projectSettings.checkHelp', { placeholder: '{file}' })"
          >
            <DouInput
              v-model="check"
              :maxlength="2000"
              placeholder="bunx tsc --noEmit"
              class="project-code"
            />
          </DouFormItem>
          <DouFormItem :label="t('projectSettings.env')">
            <SecretPairs v-model="env" :add-label="t('mcp.addEnv')" />
          </DouFormItem>
          <DouFormItem :label="t('project.mcp')">
            <DouSelect
              v-if="mcpOptions.length"
              v-model="mcp"
              multiple
              clearable
              :options="mcpOptions"
              :placeholder="t('project.mcpNone')"
            />
            <!-- Servers are set up for all projects at once; a project picks from them. -->
            <DouButton v-else size="sm" variant="outline" @click="openMcp">
              {{ t('project.mcpAdd') }}
            </DouButton>
          </DouFormItem>
        </DouForm>
      </DouTabPanel>
      <DouTabPanel value="autopilot" :label="t('projectSettings.tab.autopilot')">
        <DouForm layout="vertical" size="sm">
          <div class="project-switch">
            <span>{{ t('projectSettings.review') }}</span>
            <DouSwitch
              :model-value="settings.review.enabled"
              :aria-label="t('projectSettings.review')"
              @update:model-value="setReview"
            />
          </div>
          <DouFormItem :help="t('projectSettings.autopilotHelp')">
            <div class="project-switch">
              <span>{{ t('projectSettings.autopilot') }}</span>
              <DouSwitch
                :model-value="settings.autopilot.enabled"
                :aria-label="t('projectSettings.autopilot')"
                @update:model-value="setAutopilot"
              />
            </div>
          </DouFormItem>
          <!-- Switching it on starts work at once: which, is said before saving. -->
          <DouText
            v-if="settings.autopilot.enabled && !state.detail?.project.settings.autopilot.enabled"
            size="sm"
            variant="secondary"
          >
            {{
              startsNow.length
                ? t('projectSettings.autopilotStarts', { titles: startsNow.join('、') })
                : t('projectSettings.autopilotStartsNone')
            }}
          </DouText>
          <DouFormItem
            v-if="hostedMerge && settings.autopilot.enabled"
            :label="t('projectSettings.autopilotMethod')"
          >
            <DouSelect v-model="settings.autopilot.method" :options="methodOptions" />
          </DouFormItem>
          <div class="project-row">
            <DouFormItem :label="t('projectSettings.budgetThread')" class="project-grow">
              <DouInputNumber
                v-model="threadK"
                :min="1"
                :max="1000000"
                :step="100"
                :placeholder="t('projectSettings.noCap')"
              />
            </DouFormItem>
            <DouFormItem :label="t('projectSettings.budgetProject')" class="project-grow">
              <DouInputNumber
                v-model="projectK"
                :min="1"
                :max="100000000"
                :step="1000"
                :placeholder="t('projectSettings.noCap')"
              />
            </DouFormItem>
          </div>
        </DouForm>
        <div class="project-routines">
          <DouText weight="medium" size="sm">{{ t('projectSettings.tab.routines') }}</DouText>
          <DouEmpty v-if="!settings.routines.length" size="sm" :description="t('routine.empty')" />
          <div v-for="routine in settings.routines" :key="routine.id" class="project-routine">
            <DouTextarea
              v-model="routine.prompt"
              :autosize="{ minRows: 2, maxRows: 6 }"
              :maxlength="2000"
              :placeholder="t('routine.placeholder')"
              :aria-label="t('routine.prompt')"
            />
            <div class="project-routine-when">
              <DouCheckTagGroup
                v-model="routine.days as number[]"
                :options="dayOptions"
                multiple
                size="sm"
              />
              <div class="project-routine-time">
                <DouTimePicker
                  v-model="routine.time as string"
                  format="HH:mm"
                  size="sm"
                  :minute-step="5"
                />
              </div>
              <DouText variant="tertiary" size="sm" class="project-routine-next">
                {{ nextText(routine) }}
              </DouText>
              <DouSwitch v-model="routine.enabled" size="sm" :aria-label="t('routine.enabled')" />
              <DouButton
                variant="ghost"
                size="sm"
                :aria-label="t('routine.remove')"
                :title="t('routine.remove')"
                @click="removeRoutine(routine.id)"
              >
                <TrashIcon />
              </DouButton>
            </div>
          </div>
        </div>
      </DouTabPanel>
      <DouTabPanel value="usage" :label="t('usage.title')">
        <ProjectUsage />
      </DouTabPanel>
    </DouTabs>
    <DouAlert v-if="error" tone="danger" class="project-error">{{ error }}</DouAlert>
    <template #footer="{ close }">
      <DouButton variant="ghost" @click="close">{{ t('settings.cancel') }}</DouButton>
      <DouButton tone="primary" :loading="saving" :disabled="!settings" @click="save">
        {{ t('settings.save') }}
      </DouButton>
    </template>
  </DouDialog>
</template>

<style scoped>
.project-stack {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.project-row {
  display: flex;
  gap: 12px;
}

.project-grow {
  flex: 1;
  min-width: 0;
}

.project-code :deep(textarea),
.project-code :deep(input) {
  font-family: var(--dou-font-family-mono);
}

.project-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.project-routines {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-top: 24px;
}

.project-routine {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.project-routine-when {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.project-routine-time {
  flex: none;
  width: 92px;
}

.project-routine-next {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-routine-add {
  align-self: flex-start;
}

.project-error {
  margin-top: 12px;
}
</style>
