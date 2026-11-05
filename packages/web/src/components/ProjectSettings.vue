<script setup lang="ts">
import type { MergeMethod, ProjectSettings, ShellChoice } from '@ariadne/core'
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
import { computed, ref, watch } from 'vue'
import { api, ApiError, type McpServerView, type ToolGroup } from '../api'
import { clockText, t, tm } from '../i18n'
import { newRoutine, nextRunOf, WEEK } from './routines'
import { saveProjectSettings, state } from '../store'
import SecretPairs, { pairsInput, storedPairs, type DraftPair } from './SecretPairs.vue'

/**
 * The open project's own settings: instructions, workspaces, tools, budgets, slots, MCP and
 * routines.
 */

const SHELLS: ShellChoice[] = ['auto', 'bash', 'zsh', 'sh', 'pwsh', 'powershell', 'cmd']

const tab = ref('instructions')
const settings = ref<ProjectSettings | null>(null)
const env = ref<DraftPair[]>([])
const tools = ref<ToolGroup[]>([])
/** Budgets in thousands of tokens, as typed; empty is no cap. */
const threadK = ref<number | null>(null)
const projectK = ref<number | null>(null)
const slots = ref<number | null>(null)
const mcp = ref<string[]>([])
const mcpServers = ref<McpServerView[]>([])
const saving = ref(false)
const error = ref<string | null>(null)

const methodOptions = (['squash', 'merge'] as MergeMethod[]).map((method) => ({
  value: method,
  label: t(`changes.method.${method}`),
}))

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

const shellOptions = SHELLS.map((shell) => ({
  value: shell,
  label: shell === 'auto' ? t('projectSettings.shellAuto') : shell,
}))
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
    tab.value = 'instructions'
    now.value = Date.now()
    slots.value = project.slots
    mcp.value = [...project.mcp]
    try {
      const [view, servers] = await Promise.all([
        api.getProjectSettings(project.id),
        api.getMcpSettings().catch(() => ({ servers: [] as McpServerView[] })),
      ])
      settings.value = structuredClone(view.settings)
      env.value = storedPairs(view.env)
      tools.value = view.tools
      threadK.value = toK(view.settings.budget.thread)
      projectK.value = toK(view.settings.budget.project)
      mcpServers.value = servers.servers
    } catch (cause) {
      error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    }
  },
)

/** Whether the group is on: settings list the groups switched off. */
const toolOn = (group: string) => !settings.value?.tools.includes(group)
function setTool(group: string, on: boolean) {
  if (!settings.value) return
  const off = settings.value.tools.filter((name) => name !== group)
  settings.value.tools = on ? off : [...off, group]
}

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
function addRoutine() {
  settings.value?.routines.push(newRoutine())
}
function removeRoutine(id: string) {
  if (settings.value) settings.value.routines = settings.value.routines.filter((r) => r.id !== id)
}

async function save() {
  if (!settings.value) return
  saving.value = true
  error.value = null
  const next: ProjectSettings = {
    ...settings.value,
    // One left without words does nothing; it goes rather than failing the save.
    routines: settings.value.routines.filter((routine) => routine.prompt.trim()),
    // Cleared, the cap goes back to its default rather than failing the save.
    autopilot: {
      ...settings.value.autopilot,
      threads: settings.value.autopilot.threads || 3,
    },
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
    :width="680"
  >
    <DouTabs v-if="settings" v-model="tab" placement="start" :height="460">
      <DouTabPanel value="instructions" :label="t('projectSettings.tab.instructions')">
        <DouTextarea
          v-model="settings.instructions"
          :autosize="{ minRows: 10, maxRows: 20 }"
          :maxlength="16000"
          :placeholder="t('projectSettings.instructionsPlaceholder')"
          :aria-label="t('projectSettings.tab.instructions')"
        />
      </DouTabPanel>
      <DouTabPanel value="workspace" :label="t('projectSettings.tab.workspace')">
        <DouForm layout="vertical" size="sm">
          <DouFormItem :label="t('projectSettings.setup')">
            <DouTextarea
              v-model="settings.workspace.setup"
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
              v-model="settings.workspace.check"
              :maxlength="2000"
              placeholder="bunx tsc --noEmit"
              class="project-code"
            />
          </DouFormItem>
          <div class="project-row">
            <DouFormItem :label="t('projectSettings.shell')" class="project-grow">
              <DouSelect v-model="settings.workspace.shell" :options="shellOptions" />
            </DouFormItem>
            <DouFormItem :label="t('projectSettings.branchPrefix')" class="project-grow">
              <DouInput
                v-model="settings.workspace.branchPrefix"
                :maxlength="40"
                class="project-code"
              />
            </DouFormItem>
          </div>
          <DouFormItem :label="t('projectSettings.env')">
            <SecretPairs v-model="env" :add-label="t('mcp.addEnv')" />
          </DouFormItem>
        </DouForm>
      </DouTabPanel>
      <DouTabPanel value="tools" :label="t('projectSettings.tab.tools')">
        <div class="project-tools">
          <div class="project-switch">
            <span>{{ t('projectSettings.review') }}</span>
            <DouSwitch
              :model-value="settings.review.enabled"
              :aria-label="t('projectSettings.review')"
              @update:model-value="setReview"
            />
          </div>
          <div v-for="group in tools" :key="group" class="project-switch">
            <span>{{ t(`projectSettings.tool.${group}`) }}</span>
            <DouSwitch
              :model-value="toolOn(group)"
              :aria-label="t(`projectSettings.tool.${group}`)"
              @update:model-value="(on: boolean) => setTool(group, on)"
            />
          </div>
        </div>
        <DouForm v-if="mcpOptions.length" layout="vertical" size="sm" class="project-mcp">
          <DouFormItem :label="t('project.mcp')">
            <DouSelect
              v-model="mcp"
              multiple
              clearable
              :options="mcpOptions"
              :placeholder="t('project.mcpNone')"
            />
          </DouFormItem>
        </DouForm>
      </DouTabPanel>
      <DouTabPanel value="autopilot" :label="t('projectSettings.tab.autopilot')">
        <DouForm layout="vertical" size="sm">
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
          <div class="project-row">
            <DouFormItem :label="t('projectSettings.autopilotMethod')" class="project-grow">
              <DouSelect
                v-model="settings.autopilot.method"
                :options="methodOptions"
                :disabled="!settings.autopilot.enabled"
              />
            </DouFormItem>
            <DouFormItem :label="t('projectSettings.autopilotThreads')" class="project-grow">
              <DouInputNumber
                v-model="settings.autopilot.threads"
                :min="1"
                :max="16"
                :step="1"
                :disabled="!settings.autopilot.enabled"
              />
            </DouFormItem>
          </div>
        </DouForm>
      </DouTabPanel>
      <DouTabPanel value="limits" :label="t('projectSettings.tab.limits')">
        <DouForm layout="vertical" size="sm">
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
          <DouFormItem :label="t('project.slots')" :help="t('project.slotsHelp')">
            <DouInputNumber
              v-model="slots"
              :min="1"
              :max="16"
              :step="1"
              :placeholder="t('project.slotsShared')"
            />
          </DouFormItem>
        </DouForm>
      </DouTabPanel>
      <DouTabPanel value="routines" :label="t('projectSettings.tab.routines')">
        <div class="project-routines">
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
          <DouButton
            v-if="settings.routines.length < 20"
            variant="ghost"
            size="sm"
            class="project-routine-add"
            @click="addRoutine"
          >
            <template #leading><PlusIcon /></template>
            {{ t('routine.add') }}
          </DouButton>
        </div>
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

.project-tools {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.project-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.project-mcp {
  margin-top: 16px;
}

.project-routines {
  display: flex;
  flex-direction: column;
  gap: 16px;
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
