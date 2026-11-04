import { DomainError, MAX_MODEL_IMAGE_BYTES } from '@ariadne/core'
import { FileTooLargeError, type SourceView, type Workspace } from '@ariadne/plugin-sdk'
import { imageType } from './message-images'
import {
  json,
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
  open: (ctx: ToolContext) => Promise<W> = (ctx) => ctx.workspace() as Promise<W>,
): Tool {
  return {
    spec,
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
type Readable = Pick<Workspace, 'readFile' | 'readBytes' | 'list'>

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

async function readText(
  args: Record<string, unknown>,
  files: Readable,
  ctx: ToolContext,
): Promise<ToolResult> {
  const path = str(args, 'path')
  const image = await readImage(path, files, ctx)
  if (image) return image
  const text = await files.readFile(path)
  if (text.includes('\u0000')) {
    return { content: `${path} is a binary file, not text; it is not shown.`, isError: true }
  }
  const lines = text.split('\n')
  const offset = Math.max(1, optionalInt(args, 'offset') ?? 1)
  const limit = Math.max(1, optionalInt(args, 'limit') ?? DEFAULT_LINES)
  const shown = lines.slice(offset - 1, offset - 1 + limit)
  const body = shown.map((line, i) => `${String(offset + i).padStart(5)}\t${line}`).join('\n')
  const end = offset - 1 + shown.length
  const more = end < lines.length ? `\n[lines ${offset}-${end} of ${lines.length}]` : ''
  return { content: body + more }
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
  readText,
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
)

const runCommand = workspaceTool(
  {
    name: 'run_command',
    description:
      'Run a shell command in your workspace (bash), e.g. tests, a build, or git. It needs no approval inside the workspace; anything that reaches outside it and cannot be undone (pushing, deploying, deleting outside the workspace) must be confirmed by the user first (post_ui with a confirm block).',
    parameters: {
      type: 'object',
      properties: {
        command: { type: 'string' },
        cwd: { type: 'string', description: 'Relative to the workspace root.' },
        timeoutSeconds: { type: 'integer', description: 'Default 120, at most 600.' },
      },
      required: ['command'],
    },
  },
  async (args, workspace, ctx) => {
    const seconds = Math.min(600, Math.max(1, optionalInt(args, 'timeoutSeconds') ?? 120))
    const cwd = optionalStr(args, 'cwd')
    const result = await workspace.exec(str(args, 'command'), {
      timeoutMs: seconds * 1000,
      signal: ctx.signal,
      ...(cwd ? { cwd } : {}),
    })
    return {
      ...json({ exitCode: result.exitCode, stdout: result.stdout, stderr: result.stderr }),
      isError: result.exitCode !== 0,
    }
  },
)

export const workspaceTools: Tool[] = [listDir, readFile, writeFile, editFile, search, runCommand]

const openSource = (ctx: ToolContext): Promise<SourceView> => {
  if (!ctx.source) return Promise.reject(new DomainError('error.noSource'))
  return ctx.source()
}

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
    listEntries,
    openSource,
  ),
  workspaceTool(
    {
      name: 'read_file',
      description: `Read a file of the project's source (read-only): text with line numbers, ${DEFAULT_LINES} lines from "offset" (1-based) unless "limit" says otherwise; a PNG, JPEG, GIF or WebP image as a picture you see.`,
      parameters: readParameters,
    },
    readText,
    openSource,
  ),
]
