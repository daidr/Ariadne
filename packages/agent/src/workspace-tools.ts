import { confirmBlock, DomainError, MAX_MODEL_IMAGE_BYTES } from '@ariadne/core'
import { createTranslator } from '@ariadne/i18n'
import { FileTooLargeError, type SourceView, type Workspace } from '@ariadne/plugin-sdk'
import { createHash } from 'node:crypto'
import { commandVerdict, DEFAULT_LIMITS, type Limits } from './limits'
import { imageType } from './message-images'
import {
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
  const lines = new TextDecoder().decode(data).split('\n')
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

const writeFile = workspaceTool(
  {
    name: 'write_file',
    description: 'Create or overwrite a file of your workspace with the given content.',
    parameters: {
      type: 'object',
      properties: { path: { type: 'string' }, content: { type: 'string' } },
      required: ['path', 'content'],
    },
  },
  async (args, workspace) => {
    const content = typeof args.content === 'string' ? args.content : null
    if (content === null) throw new ToolInputError('"content" must be a string')
    await workspace.writeFile(str(args, 'path'), content)
    return { content: 'Written.' }
  },
)

const editFile = workspaceTool(
  {
    name: 'edit_file',
    description:
      'Replace an exact piece of text in a file. "old" must occur exactly once unless "all" is true; include enough surrounding lines to make it unique.',
    parameters: {
      type: 'object',
      properties: {
        path: { type: 'string' },
        old: { type: 'string' },
        new: { type: 'string' },
        all: { type: 'boolean' },
      },
      required: ['path', 'old', 'new'],
    },
  },
  async (args, workspace) => {
    const path = str(args, 'path')
    const old = str(args, 'old')
    if (typeof args.new !== 'string') throw new ToolInputError('"new" must be a string')
    const text = await workspace.readFile(path)
    const count = text.split(old).length - 1
    if (count === 0) return { content: `"old" does not occur in ${path}.`, isError: true }
    if (count > 1 && args.all !== true) {
      return {
        content: `"old" occurs ${count} times in ${path}; make it unique or pass all.`,
        isError: true,
      }
    }
    await workspace.writeFile(path, text.split(old).join(args.new))
    return { content: `Replaced ${count === 1 ? 'one occurrence' : `${count} occurrences`}.` }
  },
)

const search = workspaceTool(
  {
    name: 'search',
    description:
      'Search the workspace for a regular expression; prints matching lines with their file and line number.',
    parameters: {
      type: 'object',
      properties: {
        pattern: { type: 'string' },
        path: { type: 'string', description: 'Directory or file to search, default ".".' },
      },
      required: ['pattern'],
    },
  },
  async (args, workspace, ctx) => {
    const pattern = shellQuote(str(args, 'pattern'))
    const path = shellQuote(optionalStr(args, 'path') || '.')
    const command = `if command -v rg >/dev/null; then rg -n --no-heading --max-count 50 -e ${pattern} -- ${path}; else grep -rnE --exclude-dir=.git -m 50 -e ${pattern} -- ${path}; fi | head -n 300`
    const result = await workspace.exec(command, { signal: ctx.signal, timeoutMs: 30_000 })
    if (result.stderr.trim() && !result.stdout.trim())
      return { content: result.stderr, isError: true }
    return { content: result.stdout || 'No matches.' }
  },
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
    description: `Run a shell command in your workspace (${shell}), e.g. tests, a build, or git. It needs no approval inside the workspace, unless the user's rules ask for it: then the user is asked first, and the result says so. Anything that reaches outside the workspace and cannot be undone (pushing, deploying, deleting outside the workspace) must be confirmed by the user first (post_ui with a confirm block). Output beyond about ${Math.round(limits.commandOutput / 1000)}k characters is cut in the middle and kept whole in a file under ${OUTPUT_DIR}/, which the result names.`,
    parameters: {
      type: 'object',
      properties: {
        command: { type: 'string' },
        cwd: { type: 'string', description: 'Relative to the workspace root.' },
        timeoutSeconds: {
          type: 'integer',
          description: `Default ${limits.commandTimeout}, at most ${limits.commandTimeoutMax}.`,
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

const runCommand: Tool = {
  spec: runCommandSpec(),
  async run(args, ctx) {
    const limits = ctx.limits ?? DEFAULT_LIMITS
    const asked = optionalInt(args, 'timeoutSeconds') ?? limits.commandTimeout
    const seconds = Math.min(limits.commandTimeoutMax, Math.max(1, asked))
    const cwd = optionalStr(args, 'cwd')
    const command = str(args, 'command')
    const gate = commandGate(command, cwd || '.', ctx)
    if (gate) return gate
    const workspace = await ctx.workspace()
    try {
      const result = await workspace.exec(command, {
        timeoutMs: seconds * 1000,
        signal: ctx.signal,
        ...(cwd ? { cwd } : {}),
        ...(ctx.commands?.shell ? { shell: ctx.commands.shell } : {}),
        ...(ctx.commands && Object.keys(ctx.commands.env).length ? { env: ctx.commands.env } : {}),
      })
      return {
        content: await commandResult(result, workspace, outputName(), limits.commandOutput),
        isError: result.exitCode !== 0,
      }
    } catch (error) {
      if (error instanceof ToolInputError || error instanceof DomainError) throw error
      return { content: error instanceof Error ? error.message : String(error), isError: true }
    }
  },
}

export const workspaceTools: Tool[] = [listDir, readFile, writeFile, editFile, search, runCommand]

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
]
