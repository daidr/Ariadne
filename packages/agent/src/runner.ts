import type { SharedArtifacts } from './shared-artifacts'
import {
  askTitle,
  DomainError,
  type Ariadne,
  type Attachment,
  type ConversationView,
  type ToolCallRecord,
  type TranscriptEntry,
  type TurnOutcome,
} from '@ariadne/core'
import type { Locale, LocalizedMessage } from '@ariadne/i18n'
import type { DashboardSource, SearchProvider } from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import {
  coordinatorSections,
  joinSections,
  reviewerSections,
  threadSections,
  type PromptSection,
  type ThreadContext,
} from './prompts'
import { besideText } from './review-tools'
import { loadSkills, SkillSet, type SkillFiles } from './skills'
import { checkCommand, MAX_REVIEW_ROUNDS } from '@ariadne/core'
import {
  reviewTools,
  sendMessage,
  switchedOff,
  toolsFor,
  ToolInputError,
  type ModelChoice,
  type Tool,
  type ToolContext,
} from './tools'
import {
  compactAt,
  compactionCut,
  estimateTokens,
  requestExtras,
  summarise,
  summaryRequest,
} from './compaction'
import {
  attachedText,
  currentSections,
  keptUserTokens,
  latestBase,
  removedTools,
  toChatMessages,
  withImageData,
  writtenBy,
} from './transcript'
import { repoTools } from './repo-tools'
import { editsByPatch } from './apply-patch'
import { applyPatch, runCommandSpec, workspaceTools } from './workspace-tools'
import { forgetReads } from './file-state'
import { commandVerdict, DEFAULT_LIMITS, defaultTurnSettings, type TurnSettings } from './limits'
import type { McpHub } from './mcp'
import { mcpTools, offeredMcpTools } from './mcp-tools'
import type { WorkspaceManager } from './workspaces'
import type { EverywhereMemory } from './tool-helpers'
import type { BrowserPages } from './browser-page'
import { STOPPED_CHANGES_MS, turnChanges } from './turn-changes'
import { callOf, withCost, type ResolvedModel } from './model'
import { researchTools } from './research'
import { liveCalls } from './live-calls'
import { searchingModels } from './web-tools'
import { requestStep, TurnError } from './model-step'

export {
  callOf,
  withCost,
  type ModelPrice,
  type ModelRequestOptions,
  type ResolvedModel,
} from './model'

export interface TurnOptions {
  app: Ariadne
  conversation: ConversationView
  model: ResolvedModel
  locale: Locale
  signal: AbortSignal
  tools?: Tool[]
  /** Opens threads' workspaces; without it threads get no file or command tools. */
  workspaces?: WorkspaceManager
  /** Input size, in tokens, at which older history is summarised. */
  compactAt?: number
  /** First wait before a model request is retried; it doubles each time. */
  retryBaseMs?: number
  dashboardSources?: ReadonlyMap<string, DashboardSource<never>>
  /** Documents and whiteboards edited together. */
  shared?: SharedArtifacts
  /** The plugins' web search services. */
  searchProviders?: ReadonlyMap<string, SearchProvider>
  /** The other models the user set up: one whose service searches the web may be asked to. */
  searchModels?: readonly ResolvedModel[]
  /** The search service the user saved a key for. */
  searchService?: SearchProvider | null
  /** The models the user set up; the coordinator may give one to each thread it opens. */
  models?: ModelChoice[]
  /** The user's external tool servers; the project's enabled ones bring their tools. */
  mcp?: McpHub
  /** The user's limits, instructions, command rules and the project's environment. */
  settings?: TurnSettings
  /** The model a research runs on; the conversation's own without it. */
  researchModel?: ResolvedModel | null
  /** The user's own skill folders for every project: Ariadne's own first, then shared ones. */
  skillsDirs?: readonly string[]
  /** The user's instructions for every project, for the coordinator to add a line to. */
  everywhere?: EverywhereMemory
  /** The conversations' headless browsers; without them there is no browser tool. */
  browser?: BrowserPages
}

function threadContext(
  app: Ariadne,
  conversation: ConversationView,
  locale: Locale,
  hasWorkspace: boolean,
  hasRepo: boolean,
  remote: boolean,
  userInstructions: string,
  skills: SkillSet,
): ThreadContext {
  const project = app.getProject(conversation.projectId)
  const thread = app.getThread(conversation.id)
  const plan = thread.nodeId ? app.getPlan(project.id) : null
  const node = plan?.nodes.find((n) => n.id === thread.nodeId) ?? null
  const threads = node ? app.listThreads(project.id) : []
  const upstream = (node?.dependsOn ?? []).flatMap((id) => {
    const title = plan?.nodes.find((n) => n.id === id)?.title ?? id
    return threads
      .filter((t) => t.nodeId === id && t.lastReport === 'done' && t.lastSummary)
      .map((t) => ({ node: title, summary: t.lastSummary as string }))
  })
  return {
    projectName: project.name,
    title: thread.title,
    locale,
    node: node
      ? {
          title: node.title,
          description: node.description,
          acceptance: node.acceptance,
          issues: node.issues.map((issue) => issue.number),
        }
      : null,
    upstream,
    workspace: !hasWorkspace ? 'none' : (thread.workspace ?? 'not-open'),
    repo: hasRepo,
    remote,
    memories: app.listMemories(project.id),
    instructions: recordedInstructions(app, conversation.id),
    userInstructions: { global: userInstructions, project: project.settings.instructions },
    skills: skills.list(),
    review: project.settings.review.enabled && hasWorkspace,
  }
}

function recordedInstructions(app: Ariadne, conversationId: string) {
  const recorded = app.getInstructions(conversationId)
  return recorded?.content ? { path: recorded.path, content: recorded.content } : null
}

function systemPrompt(
  app: Ariadne,
  conversation: ConversationView,
  locale: Locale,
  hasWorkspace: boolean,
  hasRepo: boolean,
  remote: boolean,
  models: ModelChoice[],
  source: { line: string; readable: boolean } | null,
  userInstructions: string,
  skills: SkillSet,
  references: { name: string; line: string; own?: true }[],
  /** What the review skill has beside it, when it is a folder: told to a reviewer. */
  reviewBeside: string,
): PromptSection[] {
  if (conversation.role === 'coordinator') {
    const project = app.getProject(conversation.projectId)
    return coordinatorSections({
      projectName: project.name,
      unnamed: project.unnamed,
      locale,
      memories: app.listMemories(conversation.projectId),
      models,
      source,
      references,
      userInstructions: { global: userInstructions, project: project.settings.instructions },
      instructions: recordedInstructions(app, conversation.id),
      skills: skills.list(),
      autopilot: project.settings.autopilot.enabled ? { paused: project.autopilotPaused } : null,
    })
  }
  const thread = app.getThread(conversation.id)
  if (thread.reviewOf) {
    const project = app.getProject(conversation.projectId)
    const author = app.getThread(thread.reviewOf)
    const node = author.nodeId
      ? (app.getPlan(project.id).nodes.find((n) => n.id === author.nodeId) ?? null)
      : null
    const skill = skills.review()
    return reviewerSections({
      projectName: project.name,
      locale,
      author: {
        title: author.title,
        branch: author.workspace?.branch ?? null,
        base: author.workspace?.base ?? null,
      },
      node: node
        ? { title: node.title, description: node.description, acceptance: node.acceptance }
        : null,
      memories: app.listMemories(project.id),
      instructions: recordedInstructions(app, conversation.id),
      userInstructions: {
        global: userInstructions,
        project: project.settings.instructions,
      },
      reviewSkill: skill
        ? { source: skill.source, text: skill.text, beside: reviewBeside, name: skill.name }
        : null,
      skills: skills.list(),
      maxRounds: MAX_REVIEW_ROUNDS,
      references,
    })
  }
  return threadSections({
    ...threadContext(
      app,
      conversation,
      locale,
      hasWorkspace,
      hasRepo,
      remote,
      userInstructions,
      skills,
    ),
    references,
  })
}

/**
 * The skills a conversation can read: the repository's from the workspace it reads, when
 * that is open (a thread's own, a reviewer's author's), else from the project's source;
 * and the user's own. Reading them never opens a worktree.
 */
export async function conversationSkills(
  app: Ariadne,
  conversation: ConversationView,
  owner: string,
  workspaces: WorkspaceManager | undefined,
  localDirs: readonly string[],
): Promise<SkillSet> {
  let repo: SkillFiles | null = null
  if (workspaces?.available(conversation.projectId)) {
    const known = conversation.role === 'thread' ? app.getThread(owner).workspace : null
    if (known && !known.removed) {
      repo = await workspaces.forThread(conversation.projectId, owner).catch(() => null)
    }
    if (!repo && (await workspaces.viewable(conversation.projectId).catch(() => false))) {
      repo = await workspaces.view(conversation.projectId).catch(() => null)
    }
  }
  return loadSkills({ repo, localDirs })
}

async function runTool(tool: Tool | undefined, call: ToolCallRecord, turnCtx: ToolContext) {
  const ctx = { ...turnCtx, toolCallId: call.id }
  if (!tool) return { content: `Unknown tool "${call.name}".`, isError: true }
  let args: unknown
  try {
    args = JSON.parse(call.arguments || '{}')
  } catch {
    return { content: 'The arguments are not valid JSON.', isError: true }
  }
  if (!args || typeof args !== 'object' || Array.isArray(args)) {
    return { content: 'The arguments must be a JSON object.', isError: true }
  }
  try {
    return await tool.run(args as Record<string, unknown>, ctx)
  } catch (error) {
    if (error instanceof ToolInputError) return { content: error.message, isError: true }
    if (error instanceof DomainError) {
      return { content: `${error.code} ${JSON.stringify(error.params ?? {})}`, isError: true }
    }
    throw error
  }
}

/**
 * Run one turn: ask the model, run the tools it calls, and repeat until it answers without
 * calls. Every step is an event, and the model's input is rebuilt from the log each time,
 * so a turn can be replayed or resumed from what is stored.
 */
export async function runTurn(options: TurnOptions): Promise<void> {
  const { app, conversation, signal } = options
  // The main model, until it keeps failing for load and the turn goes on with its fallback.
  let model = options.model
  const { workspaces } = options
  const settings = options.settings ?? defaultTurnSettings()
  const { limits } = settings
  const project = app.getProject(conversation.projectId)
  const isThread = conversation.role === 'thread'
  // A reviewing thread reads its author's workspace and changes nothing.
  const reviewOf = isThread ? app.getThread(conversation.id).reviewOf : null
  const owner = reviewOf ?? conversation.id
  const hasWorkspace = isThread && !!workspaces?.available(conversation.projectId)
  const hasRepo = hasWorkspace && !reviewOf && !!workspaces?.hasRepo(conversation.projectId)
  const remote = hasWorkspace && !!workspaces?.isRemote(conversation.projectId)
  const hasTracker = !!workspaces?.hasTracker(conversation.projectId)
  const conversationId = conversation.id
  // Input newer than this arrived for this turn.
  const handledBefore = lastHandled(app.listTranscript(conversationId))
  const turnId = app.startTurn(conversationId, model.label)
  // The coordinator looks at the source only when its provider offers a view.
  const hasSource = !isThread && !!(await workspaces?.viewable(conversation.projectId))
  // Sources the project only reads: the looking tools take one by name.
  // A thread also has the ones the user allowed it alone.
  const ownReferences =
    isThread && !reviewOf ? app.getThread(conversation.id).references.length > 0 : false
  const hasReferences = !!workspaces && (project.references.length > 0 || ownReferences)
  // The project's switches turn built-in tools off; the user's limits and shell show in
  // run_command's description.
  const off = switchedOff(project.settings.tools)
  const shell = isThread && workspaces ? workspaces.shell(conversation.projectId) : null
  const shellName = shell?.name ?? 'bash'
  // An agent program has an apply_patch of its own, which a tool of the same name would clash with.
  const patching = !model.provider.program && editsByPatch(model.model)
  const builtIn = (
    options.tools ??
    (reviewOf
      ? reviewTools(hasReferences)
      : toolsFor(conversation.role, {
          workspace: isThread ? hasWorkspace : hasSource,
          references: hasReferences,
          ...(workspaces ? { referenceKinds: workspaces.referenceKinds(options.locale) } : {}),
          repo: hasRepo,
          tracker: hasTracker,
          models: options.models ?? [],
          // The conversation's own model searches first, then the research's, then the rest.
          search: {
            ...(options.searchProviders ? { services: options.searchProviders } : {}),
            models: searchingModels([
              model,
              options.researchModel,
              ...(options.searchModels ?? []),
            ]),
            keyed: options.searchService ?? null,
          },
          research: true,
          noteCheck: isThread && app.canNoteCheck(conversationId),
          noteSetup: isThread && app.canNoteSetup(conversationId),
          unnamed: project.unnamed,
          browser: !!options.browser,
          remote,
        }))
  )
    .filter((tool) => !off.has(tool.spec.name))
    .flatMap((tool) =>
      tool.spec.name === 'run_command'
        ? [{ ...tool, spec: runCommandSpec(limits, shellName) }]
        : // Models trained on patches edit files with one; the others replace text.
          patching && tool.spec.name === 'edit_file'
          ? [applyPatch]
          : patching && tool.spec.name === 'write_file'
            ? []
            : [tool],
    )
  // The project's external tools, as their servers list them now; one that cannot be
  // reached brings none, and the turn goes on without it.
  const external =
    options.mcp && !options.tools
      ? offeredMcpTools(
          await mcpTools(
            options.mcp,
            app.getProject(conversation.projectId).mcp,
            // A reviewer changes nothing: like the coordinator, it gets the read-only ones.
            reviewOf ? 'coordinator' : conversation.role,
            signal,
          ),
        )
      : []
  // Sending is how a conversation speaks to the user: every set of tools has it.
  const speaking = builtIn.some((tool) => tool.spec.name === 'send_message') ? [] : [sendMessage]
  const tools = [...speaking, ...builtIn, ...external]
  // What the turn changes in the thread's workspace, shown under its reply.
  const changes =
    isThread && hasWorkspace && workspaces && !reviewOf
      ? turnChanges(workspaces, conversation.projectId, conversationId)
      : null
  const finish = async (...args: TurnEnd) => {
    // A stopped turn may not have kept every result it got.
    if (signal.aborted) forgetReads(conversationId)
    // However the turn ended, nothing it was writing stays shown.
    app.drafts.drop(conversationId)
    // A stopped turn waits for its changes only briefly, so it does not stay working.
    await changes?.record(app, turnId, signal.aborted ? STOPPED_CHANGES_MS : undefined)
    app.finishTurn(conversationId, turnId, ...args)
  }
  const byName = new Map(tools.map((tool) => [tool.spec.name, tool]))
  const localSkills = options.skillsDirs ?? []
  let skills = SkillSet.empty()
  let reviewBeside = ''
  const reloadSkills = async () => {
    skills = await conversationSkills(app, conversation, owner, workspaces, localSkills)
    const review = reviewOf ? skills.review() : null
    reviewBeside = review ? await besideText(skills, review) : ''
  }
  const ctx: ToolContext = {
    app,
    projectId: conversation.projectId,
    conversationId,
    role: conversation.role,
    locale: options.locale,
    signal,
    turnId,
    keepsResults: () => !model.provider.program,
    // A reviewer's workspace is its author's, and it changes nothing there.
    workspaceOpen: () => {
      if (!isThread || reviewOf || !hasWorkspace) return false
      const known = app.getThread(owner).workspace
      return !!known && !known.removed
    },
    workspace: async () => {
      if (!workspaces || conversation.role !== 'thread') {
        throw new DomainError('error.noSource')
      }
      const workspace = await workspaces.forThread(conversation.projectId, owner)
      await changes?.begin()
      return workspace
    },
    source: () => {
      if (!workspaces) return Promise.reject(new DomainError('error.noSource'))
      return workspaces.view(conversation.projectId)
    },
    reference: (name) => {
      if (!workspaces) return Promise.reject(new DomainError('error.noReference', { name }))
      return workspaces.referenceView(
        conversation.projectId,
        name,
        isThread ? conversation.id : undefined,
      )
    },
    ...(workspaces
      ? {
          // What a conversation asks for is held to more than what the user adds by hand.
          checkReference: async (reference) => {
            const refusal = workspaces.referenceRefusal(reference)
            if (refusal) throw new DomainError('error.workspace', { detail: refusal })
            return workspaces.checkReference(conversation.projectId, reference)
          },
        }
      : {}),
    repo: () => {
      if (!workspaces || !isThread) return Promise.reject(new DomainError('error.noRepo'))
      return workspaces.repo(conversation.projectId)
    },
    tracker: () => {
      if (!workspaces) return Promise.reject(new DomainError('error.noTracker'))
      return workspaces.tracker(conversation.projectId)
    },
    branches: async () => (workspaces ? workspaces.branches(conversation.projectId) : null),
    userSpoke: () => userSpokeSince(app.listTranscript(conversationId), handledBefore),
    skills: () => skills,
    ...(options.everywhere ? { everywhere: options.everywhere } : {}),
    ...(options.browser ? { browser: options.browser } : {}),
    // The browser reaches a remote workspace's own localhost through the workspace.
    ...(options.browser && remote
      ? {
          tunnel: async (port: number) => {
            const workspace = await ctx.workspace()
            if (!workspace.connect) throw new Error('the workspace cannot reach its ports')
            return workspace.connect(port)
          },
        }
      : {}),
    ...(options.dashboardSources ? { dashboardSources: options.dashboardSources } : {}),
    ...(options.shared ? { shared: options.shared } : {}),
    models: options.models ?? [],
    limits,
    commands: {
      rules: settings.commandRules,
      shell: shell?.argv ?? null,
      shellName,
      env: settings.env,
      // Read anew each time: a check noted in this turn holds for its later edits. One a
      // thread noted runs only while the project's threads run commands and the user's
      // command rules let it run unasked.
      get check() {
        const now = app.getProject(conversation.projectId)
        const check = checkCommand(now)
        if (!now.settings.workspace.auto.check || !check) return check
        if (now.settings.tools.includes('commands')) return ''
        return commandVerdict(check, settings.commandRules).action === 'allow' ? check : ''
      },
    },
    ...(workspaces
      ? {
          background: workspaces.background,
          setupDone: (stop: AbortSignal) => workspaces.setupDone(owner, stop),
          setupRunning: () => workspaces.setupRunning(owner),
        }
      : {}),
  }
  // A research looks with the conversation's own looking tools, in a context of its own.
  if (byName.has('research')) {
    ctx.research = {
      model: options.researchModel ?? model,
      tools: researchTools(tools),
      limits,
    }
  }
  const readText = attachedText(app)
  let images = model.images !== false
  const chatFor = (seesImages: boolean) => ({
    readText,
    images: seesImages,
    keepReasoning: writtenBy(app, conversation.projectId, JSON.parse(callOf(model, null))),
  })
  let chat = chatFor(images)
  const loadImage = (file: Attachment) => {
    try {
      return {
        mime: file.mime,
        data: Buffer.from(app.getFile(file.fileId).data).toString('base64'),
      }
    } catch {
      return null
    }
  }
  const titleOf = (threadId: string) => {
    try {
      return app.getThread(threadId).title
    } catch {
      return threadId
    }
  }

  const live = liveCalls(byName, ctx)
  try {
    let threshold = options.compactAt ?? compactAt(model.contextTokens, limits.compactShare)
    // One try per turn: a failed summary would fail the same way on every step.
    let compactionFailed = false
    let lastInput = 0
    const state = () => currentState(app, conversation)
    const system = () => {
      const line = isThread
        ? null
        : (workspaces?.describe(conversation.projectId, options.locale) ?? null)
      return systemPrompt(
        app,
        conversation,
        options.locale,
        hasWorkspace,
        hasRepo,
        remote,
        options.models ?? [],
        line ? { line, readable: hasSource } : null,
        settings.instructions,
        skills,
        workspaces?.references(
          conversation.projectId,
          options.locale,
          isThread ? conversation.id : undefined,
        ) ?? [],
        reviewBeside,
      )
    }
    const specs = tools.map((tool) => tool.spec)
    const toolsJson = JSON.stringify(specs)
    /**
     * The instructions and tools this step's request carries. They stay as the newest base
     * recorded them, so the request begins the same as the one before and the provider's
     * cache holds; what changed since is recorded here and told where it happened (see
     * `promptUpdate`). They start over when the beginning changes anyway (a summary or
     * cleared results), when a tool is added or changed (a request cannot declare one
     * later), or when the changes told add up to more than starting over would cost. An
     * agent program is given them whole each time: it keeps its own history.
     */
    const settlePrompt = () => {
      const sections = system()
      if (model.provider.program) {
        return {
          system: joinSections(sections),
          tools: toolsJson,
          specs,
          chat: { ...chat, promptUpdates: false },
        }
      }
      const read = reader.read()
      const base = latestBase(read)
      const baseTools = base?.tools ? app.getPrompt(conversation.projectId, base.tools) : null
      const declared = new Map(
        (baseTools ? (JSON.parse(baseTools) as typeof specs) : []).map((spec) => [
          spec.name,
          JSON.stringify(spec),
        ]),
      )
      const removed = removedTools(read)
      const startOver =
        !base ||
        !baseTools ||
        read.some(
          (entry) => (entry.kind === 'summary' || entry.kind === 'cleared') && entry.seq > base.seq,
        ) ||
        specs.some(
          (spec) => declared.get(spec.name) !== JSON.stringify(spec) || removed.has(spec.name),
        ) ||
        updatesSince(read, base.seq) > MAX_PROMPT_UPDATES
      if (startOver) {
        app.updatePrompt(conversationId, turnId, { base: true, sections, tools: toolsJson })
        return { system: joinSections(sections), tools: toolsJson, specs, chat }
      }
      const now = new Map(currentSections(read) ?? [])
      const offered = new Set(specs.map((spec) => spec.name))
      const changed: PromptSection[] = sections.filter(([name, text]) => now.get(name) !== text)
      for (const [name, text] of now) {
        if (text && !sections.some(([other]) => other === name)) changed.push([name, ''])
      }
      const gone = [...declared.keys()].filter((name) => !offered.has(name) && !removed.has(name))
      if (changed.length || gone.length) {
        app.updatePrompt(conversationId, turnId, {
          base: false,
          sections: changed,
          ...(gone.length ? { removedTools: gone } : {}),
        })
      }
      return {
        system: joinSections(base.sections),
        tools: baseTools,
        specs: JSON.parse(baseTools) as typeof specs,
        chat,
      }
    }
    const reader = transcriptReader(app, conversationId)
    // What can change the workspace's files, and so its instruction file: its own writing
    // tools, and the external ones, which may do anything.
    const changesFiles = new Set([
      ...[...workspaceTools, applyPatch, ...repoTools]
        .filter((tool) => !tool.readOnly)
        .map((t) => t.spec.name),
      ...external.map((tool) => tool.spec.name),
    ])
    let instructionsStale = true
    // Steps in a row whose output hit the length limit.
    let cuts = 0
    // Whether the turn was told its text outside a call is not sent.
    let unsentHinted = false
    // A turn has no step limit: it ends when the model answers, the user stops it, or it
    // goes in circles (see `MAX_REPEATS`). Only the latest steps count, so a long turn may
    // read an unchanged file again now and then.
    const recent: string[] = []
    // The user follows the work by the todo list: a turn that works on without one, or
    // leaves it standing for long, is reminded once each (a reviewer's rounds are short).
    const todoWatched = !reviewOf && tools.some((t) => t.spec.name === 'update_todo')
    let workSteps = 0
    let sinceTodo = 0
    // A list carried over from an earlier turn, all done, is not this turn's list.
    let todoThisTurn = false
    const todoHinted = { missing: false, stale: false }
    // The coordinator looks only as far as briefing a thread takes: past a few looks in a
    // turn without handing anything to a thread, it is reminded, and later turned away.
    let looks = 0
    // The coordinator reads the source's instruction file once a turn: its source changes
    // under it only by the user's hand or a merge.
    if (!isThread && hasSource && workspaces) {
      await syncSourceInstructions(app, conversation, turnId, workspaces)
    }
    if (!isThread) await reloadSkills()
    for (;;) {
      if (instructionsStale && isThread && workspaces && hasWorkspace) {
        await syncInstructions(app, conversation, turnId, workspaces, owner)
      }
      // A thread's skills move with its workspace, as its instruction file does.
      if (instructionsStale && isThread) await reloadSkills()
      instructionsStale = false
      // Over the budget the turn stops before it spends more; the user raises it or not.
      const over = overBudget(app, conversation.projectId, conversationId)
      if (over) throw new TurnError(over)
      // Everything the request is built from is read in one go, so the input is exactly
      // the log up to `sawSeq` plus the pieces recorded with the step: the trace rebuilds
      // it from those.
      let prompt = settlePrompt()
      let sawSeq = app.lastSeq()
      let transcript = reader.read()
      let systemText = prompt.system
      let history = toChatMessages(transcript, titleOf, prompt.chat)
      // A long conversation makes room before it outgrows the model's context. Between
      // steps every call has its result, so any cut is whole.
      const estimate = estimateTokens([{ role: 'system', content: systemText }, ...history])
      const size = Math.max(lastInput, estimate)
      if (size >= threshold) {
        const upTo = compactionCut(transcript, threshold / 4)
        let changed = false
        let enough = false
        // Old tool results go first: the model has acted on them, and they are most of
        // what a working thread reads. Either way the request's beginning changes and the
        // provider's cache starts over, so this happens only when it frees enough.
        if (upTo !== null && upTo > clearedUpTo(transcript)) {
          const cleared = toChatMessages(transcript, titleOf, { ...chat, clearUpTo: upTo })
          const after = estimateTokens([{ role: 'system', content: systemText }, ...cleared])
          // Not enough means a summary anyway, and it is better written from the results
          // themselves than from their placeholders.
          enough =
            after < estimate * (1 - MIN_CLEARED_SHARE) &&
            (size * after) / estimate < threshold * CLEARED_ENOUGH
          if (enough) {
            app.clearToolResults(conversationId, turnId, { upTo, sawSeq })
            forgetReads(conversationId)
            changed = true
          }
        }
        if (!enough && !compactionFailed) {
          const readAt = app.lastSeq()
          const read = reader.read()
          const cut = compactionCut(read, threshold / 4)
          if (cut !== null) {
            const userTokens = keptUserTokens(threshold)
            const request = summaryRequest(
              read,
              cut,
              titleOf,
              conversation.role,
              readText,
              chat.keepReasoning,
              userTokens,
            )
            const summary = await summarise(model, withImageData(request, loadImage), signal)
            if (signal.aborted) break
            if (summary.ok) {
              forgetReads(conversationId)
              app.compactConversation(conversationId, turnId, {
                summary: summary.text,
                upTo: cut,
                sawSeq: readAt,
                usage: withCost(summary.usage, model),
                call: callOf(model, null),
                timing: summary.timing,
                reasoning: summary.reasoning,
                state: state(),
                userTokens,
              })
              changed = true
            } else {
              compactionFailed = true
              app.recordCompactionFailure(conversationId, turnId, summary.detail.slice(0, 500))
            }
          }
        }
        if (changed) {
          // The beginning changed anyway: the instructions start over from here as well.
          prompt = settlePrompt()
          sawSeq = app.lastSeq()
          transcript = reader.read()
          systemText = prompt.system
          history = toChatMessages(transcript, titleOf, prompt.chat)
        }
      }
      const input = { system: systemText, tools: prompt.tools }
      const messages = withImageData(
        [{ role: 'system' as const, content: input.system }, ...history],
        loadImage,
      )
      let answer: Awaited<ReturnType<typeof requestStep>>
      // Calls that show their work while written; what never ran is taken back.
      live.reset()
      try {
        answer = await requestStep(
          model,
          {
            model: model.model,
            messages,
            tools: prompt.specs,
            ...(model.effort ? { effort: model.effort } : {}),
            ...requestExtras(model),
            conversation: conversationId,
            signal,
          },
          (retry) => app.recordModelRetry(conversationId, turnId, retry),
          options.retryBaseMs ?? 1000,
          limits,
          model.request?.timeoutSeconds,
          live,
        )
      } catch (error) {
        live.reset()
        // The main model keeps being turned away: the turn goes on with the fallback, from
        // this step on. The request is built again for it (its context, images, thinking).
        const fallback = model.fallback
        if (!(error instanceof TurnError) || !error.exhausted || !fallback) throw error
        app.recordModelFallback(conversationId, turnId, {
          from: model.label,
          to: fallback.label,
          detail: error.exhausted.detail,
        })
        model = fallback
        // A research without a model of its own goes on with the conversation's.
        if (ctx.research && !options.researchModel) ctx.research.model = model
        images = model.images !== false
        chat = chatFor(images)
        if (!options.compactAt) threshold = compactAt(model.contextTokens, limits.compactShare)
        lastInput = 0
        compactionFailed = false
        continue
      }
      if (signal.aborted || !answer) break
      const { text, reasoning, toolCalls, usage, timing, cut } = answer
      lastInput = usage?.input ?? 0
      app.recordStep(conversationId, turnId, {
        text,
        reasoning,
        toolCalls,
        sawSeq,
        usage: withCost(usage, model),
        input: {
          system: input.system,
          tools: input.tools,
          call: callOf(model, model.effort ?? null),
          images,
        },
        timing,
        ...(cut ? { cut: true } : {}),
      })
      // A cut-off reply is not an answer: the model is told, and goes on in smaller pieces.
      cuts = cut ? cuts + 1 : 0
      if (cuts >= MAX_CUTS) throw new TurnError({ key: 'error.truncated', params: { count: cuts } })

      if (!toolCalls.length) {
        if (cut) continue
        // Text outside send_message reaches no one. It may have been meant for the user: the
        // model is told once a turn (see UNSENT_HINT) and goes on.
        if (text.trim() && !unsentHinted) {
          unsentHinted = true
          continue
        }
        await finish('completed')
        return
      }
      workSteps += 1
      const wroteTodo = toolCalls.some((call) => call.name === 'update_todo')
      sinceTodo = wroteTodo ? 0 : sinceTodo + 1
      if (wroteTodo) todoThisTurn = true
      const todoHint = () => {
        const items = app.getTodo(conversationId).items
        const finished = items.every((item) => item.status === 'done')
        if (!items.length || (finished && !todoThisTurn)) {
          if (todoHinted.missing || workSteps < TODO_MISSING_AFTER) return null
          todoHinted.missing = true
          return items.length ? TODO_FINISHED_HINT : TODO_MISSING_HINT
        }
        if (todoHinted.stale || sinceTodo < TODO_STALE_AFTER || finished) return null
        todoHinted.stale = true
        return TODO_STALE_HINT
      }
      // The same calls with the same results again: the model is not getting anywhere.
      // It is told once, with the last result of the step before the turn is stopped.
      const results: { content: string; isError: boolean }[] = []
      const record = (
        index: number,
        result: Awaited<ReturnType<typeof runTool>>,
        ran: { startedAt: number; finishedAt: number },
      ) => {
        const call = toolCalls[index]!
        results.push({ content: result.content, isError: result.isError ?? false })
        const files = result.files ?? []
        let { content } = result
        // The call that opened a new workspace hears what its setup script did.
        const setup = isThread ? workspaces?.takeSetup(conversationId) : null
        if (setup) content = `${content}\n\n${setup}`
        let count = 0
        if (index === toolCalls.length - 1) {
          const step = fingerprint(toolCalls, results)
          recent.push(step)
          if (recent.length > REPEAT_WINDOW) recent.shift()
          count = recent.filter((seen) => seen === step).length
          if (count === limits.loopRepeats - 1) content = `${content}\n\n${REPEAT_HINT}`
          const hint = todoWatched ? todoHint() : null
          if (hint) content = `${content}\n\n${hint}`
        }
        app.recordToolResult(conversationId, turnId, {
          toolCallId: call.id,
          name: call.name,
          content,
          isError: result.isError ?? false,
          ...(files.length ? { files } : {}),
          timing: ran,
        })
        if (count >= limits.loopRepeats) {
          throw new TurnError({ key: 'error.repeating', params: { count } })
        }
      }
      const workspaceBefore = isThread
        ? JSON.stringify(app.getThread(conversationId).workspace)
        : ''
      if (toolCalls.some((call) => HANDS_OVER.has(call.name))) looks = 0
      // Calls that only look run at the same time; the rest one by one, in order. Results
      // are recorded in the model's order either way.
      for (const group of callGroups(toolCalls, byName)) {
        if (signal.aborted) break
        const settled = await Promise.allSettled(
          group.map(async (index) => {
            const startedAt = Date.now()
            const call = toolCalls[index]!
            const look = !isThread && LOOKS.has(call.name) ? ++looks : 0
            if (look > COORDINATOR_LOOK_LIMIT) {
              return {
                result: { content: LOOK_REFUSED, isError: true },
                ran: { startedAt, finishedAt: startedAt },
              }
            }
            const result = await runTool(byName.get(call.name), call, ctx)
            if (look === COORDINATOR_LOOK_HINT) {
              return {
                result: { ...result, content: `${result.content}\n\n${LOOK_HINT}` },
                ran: { startedAt, finishedAt: Date.now() },
              }
            }
            return { result, ran: { startedAt, finishedAt: Date.now() } }
          }),
        )
        for (const [i, outcome] of settled.entries()) {
          if (outcome.status === 'rejected') throw outcome.reason
          record(group[i]!, outcome.value.result, outcome.value.ran)
        }
      }
      // A workspace opened (or opened again) just now has its file to read too.
      instructionsStale =
        toolCalls.some((call) => changesFiles.has(call.name)) ||
        (isThread && JSON.stringify(app.getThread(conversationId).workspace) !== workspaceBefore)
      if (signal.aborted) break
    }
    live.reset()
    await finish(abortOutcome(signal))
  } catch (error) {
    live.reset()
    if (signal.aborted) {
      await finish(abortOutcome(signal))
      return
    }
    const localized =
      error instanceof TurnError
        ? error.localized
        : error instanceof DomainError
          ? error.toLocalized()
          : { key: 'error.modelRequest' as const, params: { detail: String(error) } }
    await finish('failed', localized)
  }
}

/** How a turn ends, as `finishTurn` takes it after the ids. */
type TurnEnd = [outcome: TurnOutcome, error?: LocalizedMessage | null]

/** The repository's instruction file, read from the workspace root. */
export const INSTRUCTIONS_FILE = 'AGENTS.md'
/** Longer instruction files are cut, so they cannot crowd out the conversation. */
const MAX_INSTRUCTIONS = 16_000

/**
 * Record AGENTS.md when it appears or changes in an open workspace. A workspace not
 * opened yet is left alone: reading the file must not create a checkout.
 */
async function syncInstructions(
  app: Ariadne,
  conversation: ConversationView,
  turnId: string,
  workspaces: WorkspaceManager,
  owner: string = conversation.id,
): Promise<void> {
  // Reading the file must not bring back a worktree that is not there.
  const known = app.getThread(owner).workspace
  if (!known || known.removed) return
  let content: string | null = null
  let workspace: Awaited<ReturnType<WorkspaceManager['forThread']>> | null = null
  try {
    workspace = await workspaces.forThread(conversation.projectId, owner)
    content = await workspace.readFile(INSTRUCTIONS_FILE)
  } catch {
    content = null
  }
  if (content !== null && content.length > MAX_INSTRUCTIONS) {
    content = `${content.slice(0, MAX_INSTRUCTIONS)}\n[cut: the file is longer]`
  }
  const hash = content === null ? 'none' : createHash('sha256').update(content).digest('hex')
  if (app.getInstructions(conversation.id)?.hash === hash) return
  if (!app.getInstructions(conversation.id) && content === null) return
  // Whether the change is the checkout's own or was made in the workspace (by the thread):
  // the user should know which one the thread follows.
  let uncommitted = false
  if (content !== null && workspace?.branch) {
    const status = await workspace
      .exec(`git status --porcelain -- ${INSTRUCTIONS_FILE}`, { timeoutMs: 10_000 })
      .catch(() => null)
    uncommitted = status?.exitCode === 0 && !!status.stdout.trim()
  }
  app.recordInstructions(conversation.id, turnId, {
    path: INSTRUCTIONS_FILE,
    hash,
    content,
    uncommitted,
  })
}

/**
 * How often the same step (the same tool calls, returning the same results) may come
 * back within the last `REPEAT_WINDOW` steps before the turn is stopped, unless the user
 * set another limit. Steps need not follow each other, so a loop between two or three
 * calls is caught too.
 */
export const MAX_REPEATS = DEFAULT_LIMITS.loopRepeats
export const REPEAT_WINDOW = 12

/**
 * Read the source's instruction file for the coordinator, through its read-only view, and
 * record it when it changed. A source that cannot be read leaves what was recorded.
 */
async function syncSourceInstructions(
  app: Ariadne,
  conversation: ConversationView,
  turnId: string,
  workspaces: WorkspaceManager,
): Promise<void> {
  let content: string | null = null
  try {
    const view = await workspaces.view(conversation.projectId)
    // Not there is a fact to record; a source that cannot be read now is not.
    const top = await view.list('.')
    if (top.some((entry) => entry.name === INSTRUCTIONS_FILE && entry.kind === 'file')) {
      content = new TextDecoder().decode(await view.readBytes(INSTRUCTIONS_FILE, 1024 * 1024))
    }
  } catch {
    return
  }
  if (content !== null && content.length > MAX_INSTRUCTIONS) {
    content = `${content.slice(0, MAX_INSTRUCTIONS)}\n[cut: the file is longer]`
  }
  const hash = content === null ? 'none' : createHash('sha256').update(content).digest('hex')
  app.recordInstructions(conversation.id, turnId, { path: INSTRUCTIONS_FILE, hash, content })
}

const spent = (usage: { input: number; output: number }) => usage.input + usage.output
/** Tokens in thousands, as the settings take them. */
const k = (tokens: number) => Math.round(tokens / 1000).toLocaleString('en')

/**
 * Why the conversation may not spend more: its thread, or its whole project, used the
 * tokens the user allowed it. `null` while within them.
 */
export function overBudget(
  app: Ariadne,
  projectId: string,
  conversationId: string,
): LocalizedMessage | null {
  const project = app.getProject(projectId)
  const { budget } = project.settings
  if (budget.project !== null) {
    const used = spent(project.usage)
    if (used >= budget.project) {
      return { key: 'error.budgetProject', params: { used: k(used), limit: k(budget.project) } }
    }
  }
  const conversation = app.getConversation(conversationId)
  if (budget.thread !== null && conversation.role === 'thread') {
    const used = spent(conversation.usage)
    if (used >= budget.thread) {
      return { key: 'error.budgetThread', params: { used: k(used), limit: k(budget.thread) } }
    }
  }
  return null
}

/** What the coordinator reads and searches with, the project's and the web's. */
const LOOKS = new Set([
  'list_dir',
  'read_file',
  'search',
  'find_files',
  'research',
  'fetch_url',
  'web_search',
])
/** Calls that hand work to a thread: after one, the coordinator may look again for the next. */
const HANDS_OVER = new Set(['create_thread', 'message_thread'])
/** The coordinator's look in a turn that is reminded the work is a thread's. */
export const COORDINATOR_LOOK_HINT = 6
/** Looks in a turn past which the coordinator is turned away. */
export const COORDINATOR_LOOK_LIMIT = 12
export const LOOK_HINT =
  "[Ariadne: you have looked at a lot this turn. Looking further is doing the work yourself, and work is a thread's: put what you found into a brief and open a thread with create_thread (or pass it to the thread that has it with message_thread). Answer yourself only if what you have already answers the user.]"
export const LOOK_REFUSED =
  '[Ariadne: not run. You have looked as far as a coordinator does in one turn without handing anything to a thread. Open a thread with create_thread and put what you found into its brief, or message the thread that has the work; after that you may look again for the next one.]'

/** Steps with tool calls a turn may take with an empty todo list before it is reminded. */
export const TODO_MISSING_AFTER = 3
/** Steps in a row without update_todo, while steps are open, before a reminder. */
export const TODO_STALE_AFTER = 15
export const TODO_MISSING_HINT =
  '[Ariadne: you are working without a todo list, so the user cannot see what you are doing or what comes next. Write your steps with update_todo now, before your next step, and keep it current as you go. A rough outline is enough; change it as you learn more.]'
export const TODO_FINISHED_HINT =
  '[Ariadne: your todo list is the finished one of earlier work, so the user cannot see what you are doing now. Write the steps of this work with update_todo now, before your next step (keep the earlier ones as done if they still matter), and keep it current as you go. A rough outline is enough; change it as you learn more.]'
export const TODO_STALE_HINT =
  '[Ariadne: your todo list has not changed for many steps. If it no longer shows where you are, bring it up to date with update_todo: mark finished steps done and the one you are on as doing.]'

const REPEAT_HINT =
  '[Ariadne: this exact call has returned this exact result several times in this turn. Doing it again will stop the turn. Try something else, or answer with what you have. If you are waiting for something (checks, a reply), end the turn: you will be woken.]'

function fingerprint(
  calls: ToolCallRecord[],
  results: { content: string; isError: boolean }[],
): string {
  const hash = createHash('sha256')
  calls.forEach((call, i) => {
    hash.update(
      JSON.stringify([call.name, call.arguments, results[i]?.content, results[i]?.isError]),
    )
  })
  return hash.digest('hex')
}

/** Steps in a row whose output may hit the length limit before the turn stops. */
export const MAX_CUTS = 3
/** Clearing old tool results must free at least this share of the input to be worth it. */
const MIN_CLEARED_SHARE = 0.15
/**
 * Below this share of the threshold after clearing, no summary is needed: the conversation
 * grows a good while before the cache has to start over again.
 */
const CLEARED_ENOUGH = 0.7

/** Up to where a conversation's long tool results were cleared; `-1` when none were. */
function clearedUpTo(transcript: TranscriptEntry[]): number {
  let upTo = -1
  for (const entry of transcript) if (entry.kind === 'cleared') upTo = Math.max(upTo, entry.upTo)
  return upTo
}

/**
 * A step's calls by how they run: a call that only looks goes with the looking calls next
 * to it, any other call alone. Indexes into `calls`, in order.
 */
export function callGroups(
  calls: ToolCallRecord[],
  byName: ReadonlyMap<string, Pick<Tool, 'readOnly'>>,
): number[][] {
  const groups: number[][] = []
  let looking: number[] | null = null
  calls.forEach((call, index) => {
    if (byName.get(call.name)?.readOnly) {
      if (!looking) groups.push((looking = []))
      looking.push(index)
    } else {
      looking = null
      groups.push([index])
    }
  })
  return groups
}

/**
 * The conversation's transcript, read whole once and then only what was added since, as
 * long as no message it holds was edited meanwhile.
 */
function transcriptReader(app: Ariadne, conversationId: string) {
  let entries: TranscriptEntry[] | null = null
  let readAt = 0
  return {
    read(): TranscriptEntry[] {
      const now = app.lastSeq()
      if (!entries || app.messagesChangedSince(conversationId, readAt)) {
        entries = app.listTranscript(conversationId)
      } else if (now > readAt) {
        entries = [...entries, ...app.listTranscript(conversationId, { after: readAt })]
      }
      readAt = now
      return entries
    },
  }
}

/** How often a passing model error (rate limit, overload, broken connection) is retried. */
export const MAX_MODEL_RETRIES = DEFAULT_LIMITS.modelRetries

/** The newest event a step of this conversation had seen. */
function lastHandled(transcript: TranscriptEntry[]): number {
  let seen = 0
  for (const entry of transcript) if (entry.kind === 'step') seen = Math.max(seen, entry.sawSeq)
  return seen
}

/** Whether the user's own words reached the conversation after `since`. */
function userSpokeSince(transcript: TranscriptEntry[], since: number): boolean {
  return transcript.some(
    (entry) =>
      entry.seq > since &&
      ((entry.kind === 'message' && entry.message.author === 'user') ||
        (entry.kind === 'note' &&
          (entry.note.type === 'user-in-thread' || entry.note.type === 'answer'))),
  )
}

/**
 * What stored state says when history is summarised, kept with the notes: they may blur
 * the todo list, and `update_todo` replaces it whole. It is not brought up to date later:
 * the calls after the notes say what changed, and the request keeps its beginning.
 */
function currentState(app: Ariadne, conversation: ConversationView): string {
  const todo = app.getTodo(conversation.id).items
  const asking = app
    .listOpenAttentions(conversation.projectId)
    .filter((a) =>
      conversation.role === 'thread' ? a.threadId === conversation.id : a.threadId === null,
    )
  const todoText = todo.length
    ? todo.map((item) => `- [${item.status}] ${item.label}`).join('\n')
    : '(empty)'
  const askingText = asking.length
    ? asking.map((a) => `- [${a.id}] ${askTitle(a.block)}`).join('\n')
    : '(none)'
  return `<state_at_summary>\nYour todo list when these notes were written (update_todo replaces it whole, so keep what still applies; your update_todo calls after the notes are newer):\n${todoText}\nYour questions that were still waiting for the user's answer then (answers and withdrawals since come later in the conversation; do not ask them again; withdraw one with withdraw_ask once the user answered it in words or it no longer matters):\n${askingText}\n</state_at_summary>`
}

/** The scheduler aborts with `interrupted` on shutdown and `cancelled` when the user stops. */
function abortOutcome(signal: AbortSignal): 'cancelled' | 'interrupted' {
  return signal.reason === 'interrupted' ? 'interrupted' : 'cancelled'
}

/**
 * Past this many characters of changed instructions told since the newest base, they start
 * over instead: each change stays in the conversation, and many outweigh one fresh start.
 */
const MAX_PROMPT_UPDATES = 40_000

/** How much changed instructions the conversation was told since the base at `seq`. */
function updatesSince(transcript: TranscriptEntry[], seq: number): number {
  let size = 0
  for (const entry of transcript) {
    if (entry.kind !== 'prompt' || entry.base || entry.seq < seq) continue
    for (const [, text] of entry.sections) size += text.length
  }
  return size
}
