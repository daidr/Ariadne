<script setup lang="ts">
import type { MergeMethod, ProjectSettings } from '@ariadne/core'
import {
  DouAlert,
  DouButton,
  DouCascader,
  DouCheckTagGroup,
  DouEmpty,
  DouDialog,
  DouForm,
  DouFormItem,
  DouInput,
  DouSelect,
  DouSwitch,
  DouTabPanel,
  DouTabs,
  DouText,
  DouTextarea,
  DouTimePicker,
  useBreakpoints,
  type CascaderOption,
  type CascaderValue,
} from 'dou-ui'
import {
  CoinsIcon,
  CpuIcon,
  FolderGitIcon,
  PlusIcon,
  RocketIcon,
  ScrollTextIcon,
  TrashIcon,
  WrenchIcon,
} from '@dou-ui/icons'
import { computed, inject, ref, watch } from 'vue'
import { api, ApiError, type Effort, type McpServerView, type ModelSettings } from '../api'
import { EFFORTS, coordinatorDefaultEffort, effortWithin, effortsOf } from '../effort'
import { clockText, t, tm } from '../i18n'
import { modelLabels } from '../model-label'
import { nextRunOf, WEEK } from './routines'
import { applyModelSettings, saveProjectSettings, state } from '../store'
import { openSettingsKey } from '../open-settings'
import SecretPairs, { pairsInput, storedPairs, type DraftPair } from './SecretPairs.vue'
import FileSourcesForm from './FileSourcesForm.vue'
import SourceNotes from './SourceNotes.vue'
import MemoryList from './MemoryList.vue'
import ProjectUsage from './ProjectUsage.vue'
import SettingsLabelHelp from './SettingsLabelHelp.vue'
import { resolveWorkspaceLayout, type WorkspaceLayoutMode } from '../workspace-layout'

/**
 * The open project's own settings: where its files are, instructions, workspaces and MCP,
 * and the review, autopilot and routines. What the page no longer shows (tool
 * switches, the shell, the branch prefix, the project's own concurrency) is saved back as
 * it was.
 */

const props = defineProps<{ layoutMode?: WorkspaceLayoutMode }>()
const breakpoints = useBreakpoints()
const mode = computed<WorkspaceLayoutMode>(
  () => props.layoutMode ?? resolveWorkspaceLayout(breakpoints),
)
const isNarrow = computed(() => mode.value === 'narrow')
const fullscreen = computed(() => mode.value !== 'wide')
const tabsPlacement = computed(() => (isNarrow.value ? 'top' : 'start'))
const tabsHeight = computed(() => (fullscreen.value ? '100%' : 560))

const tab = computed({
  get: () => state.projectSettingsTab,
  set: (value) => (state.projectSettingsTab = value),
})
const source = ref<InstanceType<typeof FileSourcesForm> | null>(null)
const settings = ref<ProjectSettings | null>(null)
const env = ref<DraftPair[]>([])
const mcp = ref<string[]>([])
const mcpServers = ref<McpServerView[]>([])
const globalModelSettings = ref<ModelSettings | null>(null)
/** How many turns may run at once globally across Ariadne. */
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
const deliveryOptions = computed(() => [
  { value: 'merge', label: t('projectSettings.deliveryMerge') },
  { value: 'external', label: t('projectSettings.deliveryExternal') },
])
const retentionOptions = computed(() =>
  (['auto', 'merged', 'keep'] as const).map((value) => ({
    value,
    label: t(`projectSettings.retention.${value}`),
  })),
)
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
  const room = Math.max(0, totalSlots.value - atWork)
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
function openModels() {
  state.projectSettingsOpen = false
  openSettings('models')
}

const activeModels = computed(() => {
  const loaded = globalModelSettings.value
  if (!loaded) return state.models
  const knownKinds = Array.isArray(loaded.kinds)
    ? new Set(loaded.kinds.map((kind) => kind.kind))
    : null
  const activeProviders = knownKinds
    ? loaded.providers.filter((provider) => knownKinds.has(provider.kind))
    : loaded.providers
  const activeProviderIds = new Set(activeProviders.map((provider) => provider.id))
  const list = knownKinds
    ? loaded.models.filter((model) => activeProviderIds.has(model.provider))
    : loaded.models
  const labels = modelLabels(list, activeProviders)
  return list.map((model, index) => ({
    id: model.id,
    label: labels[index]!.label,
    ...(model.efforts !== undefined ? { efforts: model.efforts } : {}),
  }))
})

const activeModelById = (id: string | null | undefined) =>
  id ? activeModels.value.find((model) => model.id === id) : undefined

const toldOf = (id: string | null | undefined): readonly Effort[] | null =>
  activeModelById(id)?.efforts ?? null

const globalThreadRole = computed(
  () =>
    globalModelSettings.value?.roles.thread ?? {
      model: state.threadDefault,
      effort: state.threadDefaultEffort,
    },
)

const effectiveThreadModel = computed(() => {
  const overrideId = settings.value?.models.thread?.model
  return activeModelById(overrideId) ?? activeModelById(globalThreadRole.value.model) ?? null
})
const effectiveThreadTold = computed<readonly Effort[] | null>(
  () => effectiveThreadModel.value?.efforts ?? null,
)

const askingTold = computed<readonly Effort[]>(() => {
  const accepted = new Set<Effort>()
  for (const model of activeModels.value) {
    for (const effort of effortsOf(model.efforts)) accepted.add(effort)
  }
  return EFFORTS.filter((effort) => accepted.has(effort))
})

function modelBranch(
  option: CascaderOption,
  told: readonly Effort[] | null | undefined,
): CascaderOption {
  const efforts = effortsOf(told)
  if (!efforts.length) return option
  return {
    ...option,
    children: [
      { value: '', label: t('settings.effort.default') },
      ...efforts.map((effort) => ({ value: effort, label: t(`settings.effort.${effort}`) })),
    ],
  }
}

const projectThreadOptions = computed(() =>
  activeModels.value.map((model) =>
    modelBranch({ value: model.id, label: model.label }, model.efforts ?? null),
  ),
)
const projectCoordinatorOptions = computed(() => [
  modelBranch({ value: '', label: t('settings.sameAsThreads') }, effectiveThreadTold.value),
  ...projectThreadOptions.value,
])
const projectResearchOptions = computed(() => [
  modelBranch({ value: '', label: t('settings.sameAsAsking') }, askingTold.value),
  ...projectThreadOptions.value,
])

function formatModelEffort(label: string, effort: Effort | null): string {
  return effort ? `${label} · ${t(`settings.effort.${effort}`)}` : label
}

function inheritPlaceholder(summary: string | null): string {
  return summary
    ? t('projectSettings.models.inheritWith', { label: summary })
    : t('projectSettings.models.inherit')
}

const threadPlaceholder = computed(() => {
  const model = activeModelById(globalThreadRole.value.model)
  if (!model) return inheritPlaceholder(null)
  const effort = globalThreadRole.value.effort
    ? effortWithin(globalThreadRole.value.effort, effortsOf(model.efforts))
    : null
  return inheritPlaceholder(formatModelEffort(model.label, effort))
})

const coordinatorPlaceholder = computed(() => {
  const globalCoord = globalModelSettings.value?.roles.coordinator
  if (!globalCoord) return inheritPlaceholder(null)
  if (globalCoord.model) {
    const pinned = activeModelById(globalCoord.model)
    if (!pinned) return inheritPlaceholder(null)
    const effort =
      globalCoord.effort && globalCoord.effort !== 'default'
        ? effortWithin(globalCoord.effort, effortsOf(pinned.efforts))
        : null
    return inheritPlaceholder(formatModelEffort(pinned.label, effort))
  }
  const inherited = effectiveThreadModel.value
  if (!inherited) return inheritPlaceholder(null)
  const effort =
    globalCoord.effort === 'default'
      ? null
      : globalCoord.effort
        ? effortWithin(globalCoord.effort, effortsOf(inherited.efforts))
        : coordinatorDefaultEffort(inherited.efforts)
  return inheritPlaceholder(formatModelEffort(inherited.label, effort))
})

const researchPlaceholder = computed(() => {
  const globalResearch = globalModelSettings.value?.roles.research
  if (!globalResearch) return inheritPlaceholder(null)
  if (globalResearch.model) {
    const pinned = activeModelById(globalResearch.model)
    if (!pinned) return inheritPlaceholder(null)
    const effort = globalResearch.effort
      ? effortWithin(globalResearch.effort, effortsOf(pinned.efforts))
      : null
    return inheritPlaceholder(formatModelEffort(pinned.label, effort))
  }
  const effort = globalResearch.effort
    ? effortWithin(globalResearch.effort, askingTold.value)
    : null
  return inheritPlaceholder(formatModelEffort(t('settings.sameAsAsking'), effort))
})

const projectThreadPath = computed<CascaderValue[] | null>({
  get() {
    const role = settings.value?.models.thread
    if (!role?.model) return null
    const option = projectThreadOptions.value.find((item) => item.value === role.model)
    if (!option) return null
    if (!option.children?.length) return [option.value]
    const wanted = role.effort
      ? (effortWithin(role.effort, effortsOf(toldOf(role.model))) ?? '')
      : ''
    const child = option.children.find((item) => item.value === wanted) ?? option.children[0]
    return child ? [option.value, child.value] : [option.value]
  },
  set(path: CascaderValue[] | CascaderValue[][] | null) {
    if (!settings.value) return
    if (!path || !path.length) {
      settings.value.models.thread = null
      return
    }
    const [id, depth] = path
    if (typeof id !== 'string' || !id) {
      settings.value.models.thread = null
      return
    }
    const effort = EFFORTS.find((value) => value === depth) ?? null
    settings.value.models.thread = { model: id, effort }
  },
})

const projectCoordinatorPath = computed<CascaderValue[] | null>({
  get() {
    const role = settings.value?.models.coordinator
    if (!role) return null
    const target = role.model ?? ''
    const option = projectCoordinatorOptions.value.find((item) => item.value === target)
    if (!option) return null
    if (!option.children?.length) return [option.value]
    const wanted =
      option.value === ''
        ? role.effort === 'default'
          ? ''
          : role.effort
            ? (effortWithin(role.effort, effortsOf(effectiveThreadTold.value)) ?? '')
            : (coordinatorDefaultEffort(effectiveThreadTold.value) ?? '')
        : !role.effort || role.effort === 'default'
          ? ''
          : (effortWithin(role.effort, effortsOf(toldOf(String(option.value)))) ?? '')
    const child = option.children.find((item) => item.value === wanted) ?? option.children[0]
    return child ? [option.value, child.value] : [option.value]
  },
  set(path: CascaderValue[] | CascaderValue[][] | null) {
    if (!settings.value) return
    if (!path || !path.length) {
      settings.value.models.coordinator = null
      return
    }
    const [id, depth] = path
    if (typeof id !== 'string') {
      settings.value.models.coordinator = null
      return
    }
    const explicit = EFFORTS.find((value) => value === depth) ?? null
    if (id === '') {
      if (explicit) {
        settings.value.models.coordinator = { model: null, effort: explicit }
      } else if (depth === '') {
        settings.value.models.coordinator = { model: null, effort: 'default' }
      } else {
        settings.value.models.coordinator = { model: null, effort: null }
      }
    } else {
      settings.value.models.coordinator = { model: id, effort: explicit }
    }
  },
})

const projectResearchPath = computed<CascaderValue[] | null>({
  get() {
    const role = settings.value?.models.research
    if (!role) return null
    const target = role.model ?? ''
    const option = projectResearchOptions.value.find((item) => item.value === target)
    if (!option) return null
    if (!option.children?.length) return [option.value]
    const told = option.value === '' ? askingTold.value : toldOf(String(option.value))
    const wanted = role.effort ? (effortWithin(role.effort, effortsOf(told)) ?? '') : ''
    const child = option.children.find((item) => item.value === wanted) ?? option.children[0]
    return child ? [option.value, child.value] : [option.value]
  },
  set(path: CascaderValue[] | CascaderValue[][] | null) {
    if (!settings.value) return
    if (!path || !path.length) {
      settings.value.models.research = null
      return
    }
    const [id, depth] = path
    if (typeof id !== 'string') {
      settings.value.models.research = null
      return
    }
    const effort = EFFORTS.find((value) => value === depth) ?? null
    settings.value.models.research = { model: id || null, effort }
  },
})

const mcpOptions = computed(() =>
  mcpServers.value.map((server) => ({ value: server.id, label: server.name })),
)

const now = ref(Date.now())
const loadedProjectId = ref<string | null>(null)
const savedSnapshot = ref('')
let modelSettingsLoadSeq = 0

async function refreshGlobalModelSettings(projectId: string) {
  const requestId = ++modelSettingsLoadSeq
  const loadedModels = await api.getModelSettings().catch(() => null)
  if (
    requestId !== modelSettingsLoadSeq ||
    !state.projectSettingsOpen ||
    loadedProjectId.value !== projectId
  ) {
    return
  }
  if (loadedModels) {
    applyModelSettings(loadedModels)
    globalModelSettings.value = loadedModels
  } else {
    globalModelSettings.value = null
  }
}

function currentSnapshot() {
  return JSON.stringify({
    settings: settings.value,
    setup: setup.value,
    check: check.value,
    env: env.value,
    mcp: mcp.value,
  })
}

const hasUnsavedChanges = computed(
  () =>
    (!!settings.value && savedSnapshot.value !== '' && currentSnapshot() !== savedSnapshot.value) ||
    !!source.value?.dirty,
)
const showSaveFooter = computed(() => tab.value !== 'usage' || hasUnsavedChanges.value)

function discardAndClose() {
  loadedProjectId.value = null
  savedSnapshot.value = ''
  settings.value = null
  error.value = null
  state.projectSettingsOpen = false
}

watch(
  () => state.projectSettingsOpen,
  async (open) => {
    if (!open) {
      if (!hasUnsavedChanges.value) {
        loadedProjectId.value = null
        savedSnapshot.value = ''
        settings.value = null
        error.value = null
      }
      return
    }
    const project = state.detail?.project
    if (!project) return
    if (loadedProjectId.value === project.id && settings.value && hasUnsavedChanges.value) {
      now.value = Date.now()
      void refreshGlobalModelSettings(project.id)
      return
    }
    error.value = null
    settings.value = null
    savedSnapshot.value = ''
    now.value = Date.now()
    mcp.value = [...project.mcp]
    const requestId = ++modelSettingsLoadSeq
    try {
      const [view, servers, concurrency, loadedModels] = await Promise.all([
        api.getProjectSettings(project.id),
        api.getMcpSettings().catch(() => ({ servers: [] as McpServerView[] })),
        api.getConcurrency().catch(() => null),
        api.getModelSettings().catch(() => null),
      ])
      if (concurrency) totalSlots.value = concurrency.slots
      if (requestId === modelSettingsLoadSeq) {
        if (loadedModels) {
          applyModelSettings(loadedModels)
          globalModelSettings.value = loadedModels
        } else {
          globalModelSettings.value = null
        }
      }
      const nextSettings = structuredClone(view.settings)
      if (!nextSettings.models) {
        nextSettings.models = { thread: null, coordinator: null, research: null }
      }
      settings.value = nextSettings
      hostedMerge.value = view.hostedMerge
      loadedRoutines = JSON.stringify(view.settings.routines)
      const { auto } = view.settings.workspace
      found = {
        setup: view.detected.setup?.command ?? '',
        check: view.detected.check?.command ?? '',
      }
      setup.value = auto.setup ? found.setup : view.settings.workspace.setup
      check.value = auto.check ? found.check : view.settings.workspace.check
      env.value = storedPairs(view.env)
      mcpServers.value = servers.servers
      loadedProjectId.value = project.id
      savedSnapshot.value = currentSnapshot()
    } catch (cause) {
      error.value = cause instanceof ApiError ? tm(cause.localized) : String(cause)
    }
  },
  { immediate: true },
)

// A routine's days as tags, Monday first; its next run as it would be once saved.
const dayOptions = computed(() =>
  WEEK.map((day) => ({ value: day, label: t(`routine.day.${day}`) })),
)
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
  }
  error.value = await saveProjectSettings(
    { settings: next, env: pairsInput(env.value) },
    null,
    mcp.value,
  )
  saving.value = false
  if (!error.value) {
    loadedProjectId.value = null
    savedSnapshot.value = ''
    state.projectSettingsOpen = false
  }
}
</script>

<template>
  <DouDialog
    v-model:open="state.projectSettingsOpen"
    :title="t('projectSettings.title', { name: state.detail?.project.name ?? '' })"
    :width="760"
    :fullscreen="fullscreen"
    :class="{
      'project-settings-dialog-fullscreen': fullscreen,
      'project-settings-dialog-narrow': isNarrow,
    }"
  >
    <div :class="['project-settings-body', { 'is-fullscreen': fullscreen }]">
      <DouTabs
        v-if="settings"
        v-model="tab"
        :placement="tabsPlacement"
        :height="tabsHeight"
        class="project-settings-tabs"
      >
        <DouTabPanel value="source" :label="t('projectSettings.tab.source')" :icon="FolderGitIcon">
          <div class="project-stack">
            <FileSourcesForm
              v-if="state.detail"
              :key="state.detail.project.id"
              :project-id="state.detail.project.id"
              ref="source"
            />
            <SourceNotes />
          </div>
        </DouTabPanel>
        <DouTabPanel
          value="instructions"
          :label="t('projectSettings.tab.instructions')"
          :icon="ScrollTextIcon"
        >
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
        <DouTabPanel value="models" :label="t('projectSettings.tab.models')" :icon="CpuIcon">
          <DouForm v-if="activeModels.length" layout="vertical" size="sm">
            <DouFormItem :label="t('settings.threadModel')">
              <DouCascader
                v-model="projectThreadPath"
                :options="projectThreadOptions"
                :placeholder="threadPlaceholder"
                separator=" · "
                clearable
              />
            </DouFormItem>
            <DouFormItem :label="t('settings.coordinatorModel')">
              <DouCascader
                v-model="projectCoordinatorPath"
                :options="projectCoordinatorOptions"
                :placeholder="coordinatorPlaceholder"
                separator=" · "
                clearable
              />
            </DouFormItem>
            <DouFormItem>
              <template #label>
                <SettingsLabelHelp
                  :label="t('settings.researchModel')"
                  :content="t('settings.researchModelHelp')"
                />
              </template>
              <DouCascader
                v-model="projectResearchPath"
                :options="projectResearchOptions"
                :placeholder="researchPlaceholder"
                separator=" · "
                clearable
              />
            </DouFormItem>
          </DouForm>
          <div v-else class="project-models-empty">
            <DouEmpty size="sm" :description="t('projectSettings.models.empty')" />
            <DouButton size="sm" variant="outline" @click="openModels">
              {{ t('projectSettings.models.openGlobal') }}
            </DouButton>
          </div>
        </DouTabPanel>
        <DouTabPanel
          value="workspace"
          :label="t('projectSettings.tab.workspace')"
          :icon="WrenchIcon"
        >
          <DouForm layout="vertical" size="sm">
            <DouFormItem :label="t('projectSettings.retention')">
              <DouSelect v-model="settings.workspace.retention" :options="retentionOptions" />
            </DouFormItem>
            <DouFormItem :label="t('projectSettings.setup')">
              <DouTextarea
                v-model="setup"
                :autosize="{ minRows: 2, maxRows: 8 }"
                :maxlength="8000"
                placeholder="bun install"
                class="project-code"
              />
            </DouFormItem>
            <DouFormItem :label="t('projectSettings.check')">
              <template #label>
                <SettingsLabelHelp
                  :label="t('projectSettings.check')"
                  :content="t('projectSettings.checkHelp', { placeholder: '{file}' })"
                />
              </template>
              <DouInput
                v-model="check"
                :maxlength="2000"
                placeholder="bunx tsc --noEmit"
                class="project-code"
              />
            </DouFormItem>
            <DouFormItem :label="t('projectSettings.env')">
              <SecretPairs v-model="env" />
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
        <DouTabPanel
          value="autopilot"
          :label="t('projectSettings.tab.autopilot')"
          :icon="RocketIcon"
        >
          <DouForm layout="vertical" size="sm">
            <DouFormItem
              :label="t('projectSettings.delivery')"
              :help="
                settings.delivery.mode === 'external'
                  ? t('projectSettings.deliveryExternalHelp')
                  : undefined
              "
            >
              <DouSelect v-model="settings.delivery.mode" :options="deliveryOptions" />
            </DouFormItem>
            <DouFormItem
              v-if="settings.delivery.mode === 'external'"
              :label="t('projectSettings.deliveryInstructions')"
            >
              <DouTextarea
                v-model="settings.delivery.instructions"
                :autosize="{ minRows: 3, maxRows: 8 }"
                :maxlength="16000"
                :placeholder="t('projectSettings.deliveryPlaceholder')"
              />
            </DouFormItem>
            <div class="project-switch">
              <span>{{ t('projectSettings.review') }}</span>
              <DouSwitch
                :model-value="settings.review.enabled"
                :aria-label="t('projectSettings.review')"
                @update:model-value="setReview"
              />
            </div>
            <div class="project-switch">
              <SettingsLabelHelp
                :label="t('projectSettings.autopilot')"
                :content="
                  t(
                    settings.delivery.mode === 'external'
                      ? 'projectSettings.autopilotExternalHelp'
                      : 'projectSettings.autopilotHelp',
                  )
                "
              />
              <DouSwitch
                :model-value="settings.autopilot.enabled"
                :aria-label="t('projectSettings.autopilot')"
                @update:model-value="setAutopilot"
              />
            </div>
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
              v-if="hostedMerge && settings.autopilot.enabled && settings.delivery.mode === 'merge'"
              :label="t('projectSettings.autopilotMethod')"
            >
              <DouSelect v-model="settings.autopilot.method" :options="methodOptions" />
            </DouFormItem>
          </DouForm>
          <div class="project-routines">
            <DouText weight="medium" size="sm">{{ t('projectSettings.tab.routines') }}</DouText>
            <DouEmpty
              v-if="!settings.routines.length"
              size="sm"
              :description="t('routine.empty')"
            />
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
                <DouTimePicker
                  v-model="routine.time as string"
                  class="project-routine-time"
                  format="HH:mm"
                  size="sm"
                  :minute-step="5"
                />
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
        <DouTabPanel value="usage" :label="t('usage.title')" :icon="CoinsIcon">
          <ProjectUsage />
        </DouTabPanel>
      </DouTabs>
      <DouAlert v-if="error" tone="danger" class="project-error">{{ error }}</DouAlert>
    </div>
    <template #footer="{ close }">
      <DouButton v-if="!showSaveFooter" @click="close">{{ t('view.close') }}</DouButton>
      <template v-else>
        <DouButton variant="ghost" @click="discardAndClose">{{ t('settings.cancel') }}</DouButton>
        <DouButton tone="primary" :loading="saving" :disabled="!settings" @click="save">
          {{ t('settings.save') }}
        </DouButton>
      </template>
    </template>
  </DouDialog>
</template>

<style scoped>
.project-settings-dialog-fullscreen {
  padding-top: var(--ariadne-safe-top);
  padding-right: var(--ariadne-safe-right);
  padding-bottom: var(--ariadne-safe-bottom);
  padding-left: var(--ariadne-safe-left);
}

.project-settings-dialog-narrow {
  --dou-dialog-padding: 16px;
}

.project-settings-body.is-fullscreen {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.project-settings-body.is-fullscreen .project-settings-tabs {
  flex: 1;
  min-height: 0;
}

.project-stack {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.project-models-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
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
