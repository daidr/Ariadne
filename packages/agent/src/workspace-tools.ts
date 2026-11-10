import { posix } from 'node:path'
import {
  checkCommand,
  confirmBlock,
  DomainError,
  MAX_CHECK_COMMAND,
  MAX_MODEL_IMAGE_BYTES,
  MAX_SETUP_SCRIPT,
  setupCommand,
} from '@ariadne/core'
import { createTranslator } from '@ariadne/i18n'
import { FileTooLargeError, type SourceView, type Workspace } from '@ariadne/plugin-sdk'
import type { CommandOutput } from './background'
import { createHash } from 'node:crypto'
import { applyEdit } from './edit-match'
import { commandVerdict, DEFAULT_LIMITS, type Limits } from './limits'
import { imageType } from './message-images'
import { changedSince, noteFile } from './file-state'
import {
  findFiles,
  findViewFiles,
  MAX_FOUND,
  MAX_MATCH_LINES,
  searchLines,
  searchView,
} from './workspace-search'
import {
  optionalBool,
  optionalInt,
  optionalStr,
  str,
  ToolInputError,
  type Tool,
  type ToolContext,
  type ToolResult,
} from './tool-helpers'

/** Quote one argument for `bash -c`. */
export function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`
}

/**
 * Workspace failures (a missing file, a path outside the workspace) go back to the model
 * as error results it can act on, never fail the turn.
 */
function workspaceTool<W = Workspace>(
  spec: Tool['spec'],
  run: (args: Record<string, unknown>, workspace: W, ctx: ToolContext) => Promise<ToolResult>,
  options: { open?: (ctx: ToolContext) => Promise<W>; readOnly?: boolean } = {},
): Tool {
  const open = options.open ?? ((ctx: ToolContext) => ctx.workspace() as Promise<W>)
  return {
    spec,
    ...(options.readOnly ? { readOnly: true } : {}),
    async run(args, ctx) {
      const workspace = await open(ctx)
      try {
        return await run(args, workspace, ctx)
      } catch (error) {
        if (error instanceof ToolInputError || error instanceof DomainError) throw error
        return { content: error instanceof Error ? error.message : String(error), isError: true }
      }
    },
  }
}

const DEFAULT_LINES = 400

/** Files to read: a thread's workspace, or the coordinator's view of the source. */
type Readable = Pick<Workspace, 'readBytes' | 'list'>

const listEntries = async (args: Record<string, unknown>, files: Readable) => {
  const entries = await files.list(optionalStr(args, 'path') || '.')
  return {
    content:
      entries.map((e) => (e.kind === 'directory' ? `${e.name}/` : e.name)).join('\n') || '(empty)',
  }
}

const listDir = workspaceTool(
  {
    name: 'list_dir',
    description: 'List a directory of your workspace. Paths are relative to the workspace root.',
    parameters: {
      type: 'object',
      properties: { path: { type: 'string', description: 'Default ".".' } },
    },
  },
  listEntries,
  { readOnly: true },
)

/** An image of the workspace, kept in the project's files and handed to the model. */
async function readImage(
  path: string,
  workspace: Readable,
  ctx: ToolContext,
): Promise<ToolResult | null> {
  const type = imageType(path)
  if (!type) return null
  let data: Uint8Array
  try {
    data = await workspace.readBytes(path, MAX_MODEL_IMAGE_BYTES)
  } catch (error) {
    if (error instanceof FileTooLargeError) {
      return {
        content: `${path} is ${Math.round(error.size / 1024)} KB, larger than the ${MAX_MODEL_IMAGE_BYTES / 1024 / 1024} MB an image can be to look at. Make a smaller copy first (e.g. with an image tool in run_command) and read that.`,
        isError: true,
      }
    }
    throw error
  }
  if (!type.magic(data)) return null
  const file = ctx.app.saveFile(ctx.projectId, { mime: type.mime, data })
  return {
    content: `${path} is an image (${type.mime}, ${Math.max(1, Math.round(file.size / 1024))} KB); it is attached.`,
    files: [{ fileId: file.id, name: path, mime: file.mime, size: file.size }],
  }
}

/** read_file decodes at most this much of a text file. */
export const MAX_READ_BYTES = 1024 * 1024
/** A longer line is cut, e.g. minified code or JSON on one line. */
const MAX_LINE_CHARS = 2000
/** A result stops at a line boundary past this, and says where to go on. */
const MAX_RESULT_CHARS = 60_000

/**
 * A text file with line numbers, `limit` lines from `offset`, with long lines cut and the
 * whole result kept short. `tooLarge` says what to do with a file above `MAX_READ_BYTES`.
 */
async function readText(
  args: Record<string, unknown>,
  files: Readable,
  ctx: ToolContext,
  tooLarge: string,
  /** The file is the thread's own, to change: what it read is noted for its next edit. */
  own = true,
): Promise<ToolResult> {
  const path = str(args, 'path')
  const image = await readImage(path, files, ctx)
  if (image) return image
  let data: Uint8Array
  try {
    data = await files.readBytes(path, MAX_READ_BYTES)
  } catch (error) {
    if (error instanceof FileTooLargeError) {
      return {
        content: `${path} is ${Math.round(error.size / 1024)} KB, more than the ${MAX_READ_BYTES / 1024} KB read_file reads. ${tooLarge}`,
        isError: true,
      }
    }
    throw error
  }
  if (data.includes(0)) {
    return { content: `${path} is a binary file, not text; it is not shown.`, isError: true }
  }
  const whole = new TextDecoder().decode(data)
  if (own && ctx.role === 'thread' && !ctx.sub) noteFile(ctx.conversationId, path, whole)
  const lines = whole.split('\n')
  const offset = Math.max(1, optionalInt(args, 'offset') ?? 1)
  const limit = Math.max(1, optionalInt(args, 'limit') ?? DEFAULT_LINES)
  const shown: string[] = []
  let size = 0
  for (const [i, line] of lines.slice(offset - 1, offset - 1 + limit).entries()) {
    if (shown.length && size >= MAX_RESULT_CHARS) break
    const cut =
      line.length > MAX_LINE_CHARS
        ? `${line.slice(0, MAX_LINE_CHARS)} [… ${line.length - MAX_LINE_CHARS} characters of this line cut]`
        : line
    const numbered = `${String(offset + i).padStart(5)}\t${cut}`
    shown.push(numbered)
    size += numbered.length + 1
  }
  const end = offset - 1 + shown.length
  const more =
    end < lines.length
      ? `\n[lines ${offset}-${end} of ${lines.length}; read on with offset ${end + 1}]`
      : ''
  return { content: shown.join('\n') + more }
}

const readParameters = {
  type: 'object',
  properties: {
    path: { type: 'string' },
    offset: { type: 'integer' },
    limit: { type: 'integer' },
  },
  required: ['path'],
}

const readFile = workspaceTool(
  {
    name: 'read_file',
    description: `Read a file of your workspace: text with line numbers, ${DEFAULT_LINES} lines from "offset" (1-based) unless "limit" says otherwise; a PNG, JPEG, GIF or WebP image as a picture you see.`,
    parameters: readParameters,
  },
  (args, workspace, ctx) =>
    readText(
      args,
      workspace,
      ctx,
      'Look at the part you need with run_command instead (e.g. grep -n, sed -n or head).',
    ),
  { readOnly: true },
)

/** `value` as one argument of a command line in the shell `shell`. */
export function quoteFor(shell: string, value: string): string {
  if (shell === 'pwsh' || shell === 'powershell') return `'${value.replaceAll("'", "''")}'`
  if (shell === 'cmd') return `"${value.replaceAll('"', '""')}"`
  return shellQuote(value)
}

/** What the project's check after edits shows the model at most. */
const MAX_CHECK_OUTPUT = 6000

/**
 * Run the user's check after edits, with `{file}` standing for the changed files; what
 * the model reads after the edit's own result. `''` when the project has none.
 */
async function checkAfterEdit(
  ctx: ToolContext,
  workspace: Workspace,
  paths: string[],
): Promise<string> {
  const check = ctx.commands?.check?.trim()
  if (!check) return ''
  // Before the dependencies are in, the check would fail for want of them, not of the edit.
  const setup = ctx.setupRunning?.()
  if (setup) {
    return `\n[The project's check after edits did not run: ${setup}, the setup of your new workspace, still runs.]`
  }
  const { command, result } = await runCheck(ctx, workspace, check, paths)
  if (result.exitCode === 0) return `\n[The project's check after edits passed: ${command}]`
  return `\n[The project's check after edits failed (exit code ${result.exitCode}): ${command}]\n${checkOutput(result)}`
}

/** Run a check after edits on `paths`, which `{file}` in it stands for. */
async function runCheck(ctx: ToolContext, workspace: Workspace, check: string, paths: string[]) {
  const shell = ctx.commands?.shellName ?? 'bash'
  const command = check.replaceAll('{file}', paths.map((p) => quoteFor(shell, p)).join(' '))
  const limits = ctx.limits ?? DEFAULT_LIMITS
  const env = ctx.commands?.env ?? {}
  const result = await workspace
    .exec(command, {
      timeoutMs: limits.commandTimeout * 1000,
      signal: ctx.signal,
      ...(ctx.commands?.shell ? { shell: ctx.commands.shell } : {}),
      ...(Object.keys(env).length ? { env } : {}),
    })
    .catch((error: unknown) => ({
      exitCode: -1,
      stdout: '',
      stderr: error instanceof Error ? error.message : String(error),
    }))
  return { command, result }
}

const checkOutput = (result: { stdout: string; stderr: string }) =>
  headAndTail(
    [result.stdout, result.stderr].filter((part) => part.trim()).join('\n'),
    MAX_CHECK_OUTPUT,
    'run the check with run_command to see all of it',
  ) || '(no output)'

/**
 * For a project whose user set no check after edits: the first thread to know the
 * repository notes one, and every thread's edits are checked with it from then on. It is
 * run once here, so a command that does not work is found out now and not after each edit.
 */
export const noteCheck = workspaceTool(
  {
    name: 'note_check',
    description:
      'Note the check Ariadne runs after every edit_file and write_file in this project, for all its threads; its failures come back with the edit. The project has none yet, and you are the first thread that can tell: look at how the repository checks its code (package scripts, AGENTS.md or CLAUDE.md, CI config), then note one quick command, a type check or a linter, never the tests or a build. `{file}` in it stands for the files just changed; use it when the tool can check single files, so the check stays fast. The command is run once now on `files` and you get its result: call again with a corrected command if it did not run as meant. Pass an empty command when the repository has nothing fit. The user sees what you noted in the project settings.',
    parameters: {
      type: 'object',
      properties: {
        command: { type: 'string' },
        files: {
          type: 'array',
          items: { type: 'string' },
          description: 'Files of the workspace to try the command on; needed when it has {file}.',
        },
      },
      required: ['command'],
    },
  },
  async (args, workspace, ctx) => {
    if (typeof args.command !== 'string') throw new ToolInputError('"command" must be a string')
    const command = args.command.trim()
    if (command.length > MAX_CHECK_COMMAND) {
      throw new ToolInputError(`"command" must be at most ${MAX_CHECK_COMMAND} characters`)
    }
    const files = Array.isArray(args.files)
      ? args.files.filter((file): file is string => typeof file === 'string' && !!file.trim())
      : []
    if (!ctx.app.canNoteCheck(ctx.conversationId)) {
      const current = checkCommand(ctx.app.getProject(ctx.projectId))
      return {
        content: current
          ? `The project already has its check after edits: ${current}`
          : 'The check after edits of this project is not yours to note.',
        isError: true,
      }
    }
    if (!command) {
      ctx.app.noteCheck(ctx.conversationId, '')
      return { content: 'Noted: this project has no check after edits.' }
    }
    if (commandVerdict(command, ctx.commands?.rules ?? []).action !== 'allow') {
      return {
        content:
          "The user's command rules do not let this command run unasked, so it cannot be the check after edits. Note another, or none.",
        isError: true,
      }
    }
    if (command.includes('{file}') && !files.length) {
      throw new ToolInputError(
        '"files" must name a file to try the command on, since it has {file}',
      )
    }
    // Tried on a workspace whose dependencies are in, or it fails for want of them.
    const setup = (await ctx.setupDone?.(ctx.signal)) ?? null
    if (ctx.signal.aborted) return { content: 'Stopped; nothing was noted.', isError: true }
    const { command: ran, result } = await runCheck(ctx, workspace, command, files)
    if (ctx.signal.aborted) return { content: 'Stopped; nothing was noted.', isError: true }
    ctx.app.noteCheck(ctx.conversationId, command)
    return {
      content:
        (setup ? `${setup}\n\n` : '') +
        (result.exitCode === 0
          ? `Noted. It passed just now: ${ran}`
          : `Noted. It failed just now (exit code ${result.exitCode}): ${ran}\n${checkOutput(result)}\nIf the command itself is wrong (not found, bad arguments), note the corrected one now; if it found real problems in the code, it works.`),
    }
  },
)

/**
 * For a project whose user set no setup for new workspaces: the first thread to install
 * what the repository's work needs does it with this, and once it succeeded, every later
 * thread's new workspace runs the same first. Nothing is guessed from the files.
 */
export const noteSetup = workspaceTool(
  {
    name: 'note_setup',
    description:
      "Install what work in this repository needs (its dependencies, generated code) in your workspace, and note how, so every later thread's new workspace is set up the same way first. Find out how the repository itself says to (README, CONTRIBUTING, AGENTS.md or CLAUDE.md, CI config, the package manager and version it names); never guess from a lockfile's name. The command runs now in your workspace, and is noted only once it succeeded: call again with a corrected one if it failed. Pass an empty command when the repository needs nothing installed. The user sees what you noted in the project settings.",
    parameters: {
      type: 'object',
      properties: { command: { type: 'string' } },
      required: ['command'],
    },
  },
  async (args, workspace, ctx) => {
    if (typeof args.command !== 'string') throw new ToolInputError('"command" must be a string')
    const command = args.command.trim()
    if (command.length > MAX_SETUP_SCRIPT) {
      throw new ToolInputError(`"command" must be at most ${MAX_SETUP_SCRIPT} characters`)
    }
    if (!ctx.app.canNoteSetup(ctx.conversationId)) {
      const current = setupCommand(ctx.app.getProject(ctx.projectId))
      return {
        content: current
          ? `The project already has its setup for new workspaces: ${current}`
          : 'The setup for new workspaces of this project is not yours to note.',
        isError: true,
      }
    }
    if (!command) {
      ctx.app.noteSetup(ctx.conversationId, '')
      return { content: 'Noted: new workspaces of this project need nothing installed.' }
    }
    if (commandVerdict(command, ctx.commands?.rules ?? []).action !== 'allow') {
      return {
        content:
          "The user's command rules do not let this command run unasked, so it cannot set up new workspaces. Install with run_command, which asks the user where the rules say so, and note none.",
        isError: true,
      }
    }
    // Not side by side with the setup already running in this workspace: both would write
    // the same directories.
    await ctx.setupDone?.(ctx.signal)
    if (ctx.signal.aborted) return { content: 'Stopped; nothing was noted.', isError: true }
    const limits = ctx.limits ?? DEFAULT_LIMITS
    const live = ctx.app.live.start({
      projectId: ctx.projectId,
      conversationId: ctx.conversationId,
      command,
      cwd: '.',
    })
    try {
      const result = await workspace.exec(command, {
        timeoutMs: limits.commandTimeoutMax * 1000,
        signal: ctx.signal,
        onOutput: (chunk) => live.output(chunk),
        ...execOptions(ctx, undefined),
      })
      if (ctx.signal.aborted) return { content: 'Stopped; nothing was noted.', isError: true }
      if (result.exitCode !== 0) {
        return {
          content: `It failed (exit code ${result.exitCode}), so nothing was noted. Fix the command and call again.\n${checkOutput(result)}`,
          isError: true,
        }
      }
      ctx.app.noteSetup(ctx.conversationId, command)
      return { content: `Done, and noted: later threads' new workspaces run it first.` }
    } finally {
      live.end()
    }
  },
)

/** The text of a file, or `null` when there is none (or it cannot be read). */
const currentText = (workspace: Workspace, path: string) =>
  workspace.readFile(path).catch(() => null)

const writeFile = workspaceTool(
  {
    name: 'write_file',
    description:
      'Create a file of your workspace, or overwrite one with the given content. Overwriting a file that changed since you last read or wrote it is refused: read it again first. For a change to part of a file, use edit_file.',
    parameters: {
      type: 'object',
      properties: { path: { type: 'string' }, content: { type: 'string' } },
      required: ['path', 'content'],
    },
  },
  async (args, workspace, ctx) => {
    const path = str(args, 'path')
    const content = typeof args.content === 'string' ? args.content : null
    if (content === null) throw new ToolInputError('"content" must be a string')
    const before = await currentText(workspace, path)
    if (before !== null && changedSince(ctx.conversationId, path, before)) {
      return {
        content: `${path} changed since you last read or wrote it (the user, a command or a tool changed it). Read it again and work from what it holds now; nothing was written.`,
        isError: true,
      }
    }
    await workspace.writeFile(path, content)
    noteFile(ctx.conversationId, path, content)
    return { content: `Written.${await checkAfterEdit(ctx, workspace, [path])}` }
  },
)

/** One replacement edit_file makes. */
interface Edit {
  old: string
  new: string
  all: boolean
}

function oneEdit(raw: Record<string, unknown>, at: string): Edit {
  if (typeof raw.old !== 'string' || !raw.old) {
    throw new ToolInputError(`${at}"old" must be a non-empty string`)
  }
  if (typeof raw.new !== 'string') throw new ToolInputError(`${at}"new" must be a string`)
  if (raw.all !== undefined && typeof raw.all !== 'boolean') {
    throw new ToolInputError(`${at}"all" must be true or false`)
  }
  return { old: raw.old, new: raw.new, all: raw.all === true }
}

function editsOf(args: Record<string, unknown>): Edit[] {
  if (args.edits !== undefined) {
    if (!Array.isArray(args.edits) || !args.edits.length) {
      throw new ToolInputError('"edits" must be a non-empty array')
    }
    if (args.old !== undefined) throw new ToolInputError('pass either "edits" or "old", not both')
    return args.edits.map((raw, i) =>
      oneEdit((raw ?? {}) as Record<string, unknown>, `edits[${i}]: `),
    )
  }
  return [oneEdit(args, '')]
}

const editFile = workspaceTool(
  {
    name: 'edit_file',
    description:
      'Change part of a file: replace "old" with "new", or make several replacements at once with "edits", applied in order (all or none). "old" must occur once unless "all" is true; copy it from the file, with enough of the lines around it to make it unique. Text that differs only in whitespace (indentation, trailing spaces, line endings) is found too. When "old" is not found, the result shows the nearest lines of the file.',
    parameters: {
      type: 'object',
      properties: {
        path: { type: 'string' },
        old: { type: 'string' },
        new: { type: 'string' },
        all: { type: 'boolean' },
        edits: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              old: { type: 'string' },
              new: { type: 'string' },
              all: { type: 'boolean' },
            },
            required: ['old', 'new'],
          },
        },
      },
      required: ['path'],
    },
  },
  async (args, workspace, ctx) => {
    const path = str(args, 'path')
    const edits = editsOf(args)
    let text = await workspace.readFile(path)
    const notes: string[] = []
    let replaced = 0
    for (const [i, edit] of edits.entries()) {
      const outcome = applyEdit(text, edit.old, edit.new, edit.all)
      const which = edits.length > 1 ? `Edit ${i + 1} of ${edits.length}: ` : ''
      if (!outcome.ok) {
        const none = edits.length > 1 ? ' None of the edits was made.' : ''
        return { content: `${which}${outcome.reason} in ${path}.${none}`, isError: true }
      }
      text = outcome.text
      replaced += outcome.count
      if (outcome.loose.length) {
        const where = outcome.loose.map((r) => `${r.from}-${r.to}`).join(', ')
        notes.push(`${which}matched with other whitespace at lines ${where}`)
      }
    }
    await workspace.writeFile(path, text)
    noteFile(ctx.conversationId, path, text)
    const done = `Replaced ${replaced === 1 ? 'one occurrence' : `${replaced} occurrences`}.`
    const loose = notes.length ? ` (${notes.join('; ')}; check the indentation)` : ''
    return { content: `${done}${loose}${await checkAfterEdit(ctx, workspace, [path])}` }
  },
)

const searchParameters = {
  type: 'object',
  properties: {
    pattern: { type: 'string' },
    path: { type: 'string', description: 'Directory or file to search, default ".".' },
  },
  required: ['pattern'],
}

/** What a search found, as the model reads it. */
function matches(found: { lines: string[]; error: string | null }): ToolResult {
  if (found.error) return { content: found.error, isError: true }
  if (!found.lines.length) return { content: 'No matches.' }
  const more = found.lines.length >= MAX_MATCH_LINES ? '\n[more matches; narrow the search]' : ''
  return { content: found.lines.join('\n') + more }
}

const findParameters = {
  type: 'object',
  properties: {
    pattern: { type: 'string' },
    path: { type: 'string', description: 'Directory to look in, default ".".' },
  },
  required: ['pattern'],
}

/** The files a glob found, as the model reads them. */
function foundFiles({
  found,
  total,
  partial,
}: {
  found: string[]
  total: number
  partial: boolean
}): ToolResult {
  if (!found.length)
    return { content: partial ? 'No files found in the part looked at.' : 'No files found.' }
  const notes = [
    ...(total > found.length ? [`${total - found.length} more; narrow the pattern`] : []),
    ...(partial ? ['the directory is large and was only looked at in part'] : []),
  ]
  return { content: found.join('\n') + (notes.length ? `\n[${notes.join('; ')}]` : '') }
}

const search = workspaceTool(
  {
    name: 'search',
    description: `Search the workspace's files for a regular expression; prints matching lines as file:line:text, at most ${MAX_MATCH_LINES}. Files git ignores are left out.`,
    parameters: searchParameters,
  },
  async (args, workspace, ctx) =>
    matches(
      await searchLines(
        workspace,
        str(args, 'pattern'),
        optionalStr(args, 'path') || '.',
        ctx.signal,
      ),
    ),
  { readOnly: true },
)

const findFilesTool = workspaceTool(
  {
    name: 'find_files',
    description: `Find files of the workspace by a glob pattern, e.g. "**/*.test.ts" or "src/**/index.{ts,vue}"; a pattern without "/" matches file names at any depth. Files git ignores are left out. Lists at most ${MAX_FOUND} paths, sorted.`,
    parameters: findParameters,
  },
  async (args, workspace, ctx) =>
    foundFiles(
      await findFiles(
        workspace,
        str(args, 'pattern'),
        optionalStr(args, 'path') || '.',
        ctx.signal,
      ),
    ),
  { readOnly: true },
)

/** What run_command hands the model of a command's output by default, stdout and stderr together. */
export const MAX_COMMAND_CHARS = DEFAULT_LIMITS.commandOutput
/** Where a cut output is kept whole, inside the workspace; the directory ignores itself. */
export const OUTPUT_DIR = '.ariadne/output'

/**
 * `text` cut to about `budget` characters: its beginning (what ran) and, larger, its end
 * (where errors and summaries are), at line boundaries when one is near.
 */
export function headAndTail(text: string, budget: number, where: string): string {
  if (text.length <= budget) return text
  let head = Math.floor(budget * 0.4)
  let tail = text.length - (budget - head)
  const headLine = text.lastIndexOf('\n', head)
  if (headLine > 0 && headLine > head - 400) head = headLine + 1
  const tailLine = text.indexOf('\n', tail)
  if (tailLine !== -1 && tailLine < tail + 400) tail = tailLine + 1
  return `${text.slice(0, head)}[… ${tail - head} characters cut; ${where} …]\n${text.slice(tail)}`
}

/** How much of each stream fits: a short one is kept whole and the other gets the rest. */
function budgets(stdout: number, stderr: number, max: number): [number, number] {
  if (stdout + stderr <= max) return [stdout, stderr]
  const half = max / 2
  if (stderr <= half) return [max - stderr, stderr]
  if (stdout <= half) return [stdout, max - stdout]
  return [half, half]
}

/**
 * A command's result as the model reads it: the exit code, then each stream as plain text.
 * Output longer than `max` characters is cut in the middle, and kept whole in the workspace
 * where read_file and search can look into it.
 */
export async function commandResult(
  result: { exitCode: number; stdout: string; stderr: string },
  workspace: Pick<Workspace, 'writeFile'>,
  name: string,
  max: number = MAX_COMMAND_CHARS,
): Promise<string> {
  const { stdout, stderr } = result
  const [outBudget, errBudget] = budgets(stdout.length, stderr.length, max)
  let where = 'the whole output could not be kept'
  if (outBudget < stdout.length || errBudget < stderr.length) {
    const path = `${OUTPUT_DIR}/${name}.log`
    try {
      await workspace.writeFile('.ariadne/.gitignore', '*\n')
      await workspace.writeFile(
        path,
        `exit code ${result.exitCode}\n--- stdout ---\n${stdout}\n--- stderr ---\n${stderr}`,
      )
      where = `the whole output is in ${path}`
    } catch {
      // The cut output still says what it can.
    }
  }
  const parts = [`exit code ${result.exitCode}`]
  if (stdout) parts.push(`--- stdout ---\n${headAndTail(stdout, outBudget, where)}`)
  if (stderr) parts.push(`--- stderr ---\n${headAndTail(stderr, errBudget, where)}`)
  if (!stdout && !stderr) parts.push('(no output)')
  return parts.join('\n')
}

let outputs = 0
/** A name for a kept output that sorts by time and does not repeat within a process. */
export const outputName = () =>
  `${new Date().toISOString().replace(/[-:]/g, '').replace(/\..*/, '')}-${(++outputs).toString(36)}`

/** run_command as the model reads it, with the user's limits and shell. */
export function runCommandSpec(limits: Limits = DEFAULT_LIMITS, shell = 'bash'): Tool['spec'] {
  return {
    name: 'run_command',
    description: `Run a shell command in your workspace (${shell}), e.g. tests, a build, or git. It needs no approval inside the workspace, unless the user's rules ask for it: then the user is asked first, and the result says so. Anything that reaches outside the workspace and cannot be undone (pushing, deploying, deleting outside the workspace) must be confirmed by the user first (post_ui with a confirm block). Output beyond about ${Math.round(limits.commandOutput / 1000)}k characters is cut in the middle and kept whole in a file under ${OUTPUT_DIR}/, which the result names. With "background", the command keeps running (a dev server, a watcher, a long build) and you get a handle at once: read what it printed with read_command_output, end it with stop_command. Background commands end by themselves when you report or are stopped. Only when the user said they want to try the result themselves, start it with "keepForUser": it then stays up after you report ready, until they are done looking. Otherwise show what changed with your report.`,
    parameters: {
      type: 'object',
      properties: {
        command: { type: 'string' },
        cwd: { type: 'string', description: 'Relative to the workspace root.' },
        timeoutSeconds: {
          type: 'integer',
          description: `Default ${limits.commandTimeout}, at most ${limits.commandTimeoutMax}. Not for background commands.`,
        },
        background: { type: 'boolean', description: 'Run it in the background.' },
        keepForUser: {
          type: 'boolean',
          description: 'With background, and only when the user asked to try it themselves.',
        },
      },
      required: ['command'],
    },
  }
}

/** Names the command a yes is for: where it runs and what it is, hashed. */
export function commandPurpose(command: string, cwd: string) {
  return `command:${createHash('sha256')
    .update(JSON.stringify([cwd, command]))
    .digest('hex')}`
}

/** The longest command the user is asked about; they could not check a longer one. */
const MAX_ASKED_COMMAND = 10_000

/**
 * Whether the user's rules let `command` run now: `null` when it may, else the result for
 * the model. A command they must agree to is put to them once; their yes is for that one
 * run of it in that directory.
 */
function commandGate(command: string, cwd: string, ctx: ToolContext): ToolResult | null {
  const rules = ctx.commands?.rules ?? []
  const verdict = commandVerdict(command, rules)
  if (verdict.action === 'allow') return null
  if (verdict.action === 'deny') {
    return {
      content: `The user's command rules do not allow this command (it matches "${verdict.pattern}"). Do not run it another way; do without it, or tell the user what you would need.`,
      isError: true,
    }
  }
  if (command.length > MAX_ASKED_COMMAND) {
    return {
      content: `This command needs the user's yes, and at over ${MAX_ASKED_COMMAND} characters it is too long for them to check. Run less at once.`,
      isError: true,
    }
  }
  const t = createTranslator(ctx.locale)
  const approval = ctx.app.callApproval(
    ctx.projectId,
    ctx.conversationId,
    commandPurpose(command, cwd),
    () =>
      confirmBlock({
        title: t('command.ask.question'),
        detail: cwd && cwd !== '.' ? `cd ${cwd}\n${command}` : command,
        description: t('command.ask.rule', { pattern: verdict.pattern ?? '' }),
        approveText: t('command.ask.yes'),
        denyText: t('command.ask.no'),
      }),
  )
  if (approval.state === 'approved') {
    ctx.app.useApproval(ctx.projectId, {
      threadId: ctx.conversationId,
      attentionId: approval.attentionId,
      what: 'command',
    })
    return null
  }
  if (approval.state === 'declined') {
    return {
      content:
        'The user declined this command. Do not run it, nor something to the same effect; go on without it, or tell the user what you would need.',
      isError: true,
    }
  }
  return {
    content:
      approval.state === 'asked'
        ? "The user's rules ask for their yes on this command, and they were asked. Their answer arrives as a new turn; run it again, the same way, only if they agree."
        : 'The user is already being asked about this command. Their answer arrives as a new turn; do not run it until then.',
  }
}

/** How long run_command waits for a background command's first output before it answers. */
const FIRST_OUTPUT_MS = 2000
/** The longest read_command_output waits for something new. */
const MAX_WAIT_SECONDS = 60

/** The options a thread's command runs with: where, in what, with which environment. */
function execOptions(ctx: ToolContext, cwd: string | undefined) {
  return {
    ...(cwd ? { cwd } : {}),
    ...(ctx.commands?.shell ? { shell: ctx.commands.shell } : {}),
    ...(ctx.commands && Object.keys(ctx.commands.env).length ? { env: ctx.commands.env } : {}),
  }
}

/** One spelling for a directory in the workspace, so `docs`, `./docs/` and `docs/.` compare equal. */
const sameDir = (cwd: string | undefined) =>
  posix.normalize(`${cwd || '.'}/`).replace(/\/+$/, '') || '.'

/** A background command's state and what it printed since the last look, as the model reads it. */
function outputText(read: CommandOutput, max: number, how: 'started' | 'read' | 'stopped'): string {
  const { command, text, missed } = read
  const state = command.result
    ? command.result.stopped
      ? `${command.id} was stopped (exit code ${command.result.exitCode})`
      : `${command.id} ended with exit code ${command.result.exitCode}`
    : how === 'started'
      ? `${command.id} runs in the background`
      : `${command.id} is still running`
  const lost = missed ? `[… ${missed} characters of output came and went unread …]\n` : ''
  const body = text
    ? headAndTail(text, max, 'read on with read_command_output for what comes next')
    : how === 'started'
      ? '(no output yet)'
      : '(no new output)'
  return `${state}.\n--- output${how === 'started' ? '' : ' since the last read'} ---\n${lost}${body}`
}

const runCommand: Tool = {
  spec: runCommandSpec(),
  async run(args, ctx) {
    const limits = ctx.limits ?? DEFAULT_LIMITS
    const asked = optionalInt(args, 'timeoutSeconds') ?? limits.commandTimeout
    const seconds = Math.min(limits.commandTimeoutMax, Math.max(1, asked))
    const cwd = optionalStr(args, 'cwd')
    const command = str(args, 'command')
    const background = optionalBool(args, 'background')
    const gate = commandGate(command, cwd || '.', ctx)
    if (gate) return gate
    const workspace = await ctx.workspace()
    // The new workspace's setup comes first: a command now would find it half done.
    const setup = (await ctx.setupDone?.(ctx.signal)) ?? null
    if (ctx.signal.aborted) return { content: 'Stopped before it ran.', isError: true }
    const told = (result: ToolResult): ToolResult =>
      setup ? { ...result, content: `${setup}\n\n${result.content}` } : result
    try {
      if (background) {
        if (!ctx.background) {
          return { content: 'Commands cannot run in the background here.', isError: true }
        }
        // Started again, e.g. a dev server after more changes: the one before makes way,
        // so they do not pile up or fight over a port.
        const replaced: string[] = []
        const where = sameDir(cwd)
        // What the user asked to try stays theirs to try after a restart.
        let keep = optionalBool(args, 'keepForUser')
        for (const old of ctx.background.list(ctx.conversationId)) {
          if (old.result || old.command !== command || old.cwd !== where) continue
          const ended = await ctx.background.stop(ctx.conversationId, old.id)
          // Still going after the wait: a second copy would fight it over its port.
          if (!ended?.result) {
            return {
              content: `${old.id} runs the same command and has not ended yet after being told to stop. Check it with read_command_output, then start this again.`,
              isError: true,
            }
          }
          replaced.push(old.id)
          keep ||= old.keep
        }
        const started = ctx.background.start(
          ctx.conversationId,
          command,
          (options) => workspace.exec(command, { ...execOptions(ctx, cwd), ...options }),
          { local: workspace.local, keep, cwd: where },
        )
        const first = await ctx.background.read(
          ctx.conversationId,
          started.id,
          FIRST_OUTPUT_MS,
          ctx.signal,
        )
        const note = replaced.length
          ? `\n[${replaced.join(', ')} ran the same command and was ended first]`
          : ''
        return told({
          content: (first ? outputText(first, limits.commandOutput, 'started') : started.id) + note,
          isError: !!first?.command.result && first.command.result.exitCode !== 0,
        })
      }
    } catch (error) {
      if (error instanceof ToolInputError || error instanceof DomainError) throw error
      return { content: error instanceof Error ? error.message : String(error), isError: true }
    }
    // The user watches a command the turn waits for; the log gets its result once it ends.
    // One left in the background outlives the call and is not shown there.
    const live = ctx.app.live.start({
      projectId: ctx.projectId,
      conversationId: ctx.conversationId,
      command,
      cwd: cwd || '.',
    })
    try {
      const result = await workspace.exec(command, {
        timeoutMs: seconds * 1000,
        signal: ctx.signal,
        onOutput: (chunk) => live.output(chunk),
        ...execOptions(ctx, cwd),
      })
      return told({
        content: await commandResult(result, workspace, outputName(), limits.commandOutput),
        isError: result.exitCode !== 0,
      })
    } catch (error) {
      if (error instanceof ToolInputError || error instanceof DomainError) throw error
      return { content: error instanceof Error ? error.message : String(error), isError: true }
    } finally {
      live.end()
    }
  },
}

/** Says which background commands there are, when the model named one that is not. */
function unknownCommand(ctx: ToolContext, id: string): ToolResult {
  const known = ctx.background?.list(ctx.conversationId) ?? []
  return {
    content: `You have no background command ${id}.${known.length ? ` Yours: ${known.map((c) => `${c.id} (${c.result ? 'ended' : 'running'}) ${c.command.slice(0, 80)}`).join('; ')}` : ''}`,
    isError: true,
  }
}

const readCommandOutput: Tool = {
  spec: {
    name: 'read_command_output',
    description: `Read what a background command printed since you last read it, and whether it still runs. With "waitSeconds" (at most ${MAX_WAIT_SECONDS}) it waits until something new comes or the command ends.`,
    parameters: {
      type: 'object',
      properties: {
        handle: { type: 'string', description: 'What run_command gave, e.g. "cmd-1".' },
        waitSeconds: { type: 'integer' },
      },
      required: ['handle'],
    },
  },
  async run(args, ctx) {
    const id = str(args, 'handle')
    const wait = Math.min(MAX_WAIT_SECONDS, Math.max(0, optionalInt(args, 'waitSeconds') ?? 0))
    const read = await ctx.background?.read(ctx.conversationId, id, wait * 1000, ctx.signal)
    if (!read) return unknownCommand(ctx, id)
    const limits = ctx.limits ?? DEFAULT_LIMITS
    return { content: outputText(read, limits.commandOutput, 'read') }
  },
}

const stopCommand: Tool = {
  spec: {
    name: 'stop_command',
    description:
      'End a background command, and whatever it started; the result has what it printed since you last read it.',
    parameters: {
      type: 'object',
      properties: { handle: { type: 'string' } },
      required: ['handle'],
    },
  },
  async run(args, ctx) {
    const id = str(args, 'handle')
    const stopped = await ctx.background?.stop(ctx.conversationId, id)
    if (!stopped) return unknownCommand(ctx, id)
    const read = await ctx.background!.read(ctx.conversationId, id)
    const limits = ctx.limits ?? DEFAULT_LIMITS
    return { content: read ? outputText(read, limits.commandOutput, 'stopped') : 'Stopped.' }
  },
}

/** The workspace tools that only read, for a thread that reviews another's workspace. */
export const readWorkspaceTools: Tool[] = [listDir, readFile, findFilesTool, search]

export const workspaceTools: Tool[] = [
  listDir,
  readFile,
  findFilesTool,
  writeFile,
  editFile,
  search,
  runCommand,
  readCommandOutput,
  stopCommand,
]

const openSource = (ctx: ToolContext): Promise<SourceView> => {
  if (!ctx.source) return Promise.reject(new DomainError('error.noSource'))
  return ctx.source()
}

/** Says what a view shows when it is not the directory as it is now. */
const fromRef = (view: SourceView, result: ToolResult): ToolResult =>
  view.ref && !result.isError
    ? { ...result, content: `${result.content}\n[from ${view.ref}]` }
    : result

/**
 * The coordinator's look at the project's source, as it is now and read-only: enough to
 * know what the project is and brief threads well. Changing it is a thread's work.
 */
export const sourceTools: Tool[] = [
  workspaceTool(
    {
      name: 'list_dir',
      description:
        'List a directory of the project\'s source (read-only). Paths are relative to its root; "." is the root.',
      parameters: {
        type: 'object',
        properties: { path: { type: 'string', description: 'Default ".".' } },
      },
    },
    async (args, view) => fromRef(view, await listEntries(args, view)),
    { open: openSource, readOnly: true },
  ),
  workspaceTool(
    {
      name: 'read_file',
      description: `Read a file of the project's source (read-only): text with line numbers, ${DEFAULT_LINES} lines from "offset" (1-based) unless "limit" says otherwise; a PNG, JPEG, GIF or WebP image as a picture you see.`,
      parameters: readParameters,
    },
    async (args, view, ctx) =>
      fromRef(
        view,
        await readText(
          args,
          view,
          ctx,
          'Read another file, or leave a closer look at this one to a thread.',
        ),
      ),
    { open: openSource, readOnly: true },
  ),
  workspaceTool(
    {
      name: 'search',
      description: `Search the files of the project's source (read-only) for a regular expression; prints matching lines as file:line:text, at most ${MAX_MATCH_LINES}.`,
      parameters: searchParameters,
    },
    async (args, view, ctx) => fromRef(view, await searchIn(view, args, ctx)),
    { open: openSource, readOnly: true },
  ),
  workspaceTool(
    {
      name: 'find_files',
      description: `Find files of the project's source (read-only) by a glob pattern, e.g. "**/*.test.ts" or "src/**/index.{ts,vue}"; a pattern without "/" matches file names at any depth. Lists at most ${MAX_FOUND} paths, sorted.`,
      parameters: findParameters,
    },
    async (args, view, ctx) => fromRef(view, await findIn(view, args, ctx)),
    { open: openSource, readOnly: true },
  ),
]

async function searchIn(view: SourceView, args: Record<string, unknown>, ctx: ToolContext) {
  return matches(
    await searchView(view, str(args, 'pattern'), optionalStr(args, 'path') || '.', ctx.signal),
  )
}

async function findIn(view: SourceView, args: Record<string, unknown>, ctx: ToolContext) {
  return foundFiles(
    await findViewFiles(view, str(args, 'pattern'), optionalStr(args, 'path') || '.', ctx.signal),
  )
}

/** How each looking tool reads a reference source rather than the conversation's own files. */
const REFERENCE_READS: Record<
  string,
  (args: Record<string, unknown>, view: SourceView, ctx: ToolContext) => Promise<ToolResult>
> = {
  list_dir: (args, view) => listEntries(args, view),
  read_file: (args, view, ctx) =>
    readText(args, view, ctx, 'Read the part you need with "offset" and "limit".', false),
  search: (args, view, ctx) => searchIn(view, args, ctx),
  find_files: (args, view, ctx) => findIn(view, args, ctx),
}

/** The looking tools a project with reference sources can point at one of them. */
export const REFERENCE_TOOLS = Object.keys(REFERENCE_READS)

/**
 * The same tools for a project with reference sources: the looking ones take a `source`,
 * the name of the reference to read in place of the conversation's own files. Nothing
 * that writes or runs takes one, which is what keeps a reference read-only.
 */
export function withReferences(tools: Tool[]): Tool[] {
  return tools.map((tool) => {
    const read = REFERENCE_READS[tool.spec.name]
    if (!read) return tool
    const parameters = tool.spec.parameters as { properties?: Record<string, unknown> }
    return {
      ...tool,
      spec: {
        ...tool.spec,
        parameters: {
          ...parameters,
          properties: {
            ...parameters.properties,
            source: {
              type: 'string',
              description:
                'The name of a reference source to read instead (see "Reference sources"), with paths relative to its root. Leave out for your own files.',
            },
          },
        },
      },
      async run(args, ctx) {
        const name = optionalStr(args, 'source')
        if (!name) return tool.run(args, ctx)
        if (!ctx.reference) throw new DomainError('error.noReference', { name })
        const view = await ctx.reference(name)
        try {
          return fromRef(view, await read(args, view, ctx))
        } catch (error) {
          if (error instanceof ToolInputError || error instanceof DomainError) throw error
          return { content: error instanceof Error ? error.message : String(error), isError: true }
        }
      },
    }
  })
}
