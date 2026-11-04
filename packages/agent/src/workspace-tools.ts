import { DomainError } from '@ariadne/core'
import type { Workspace } from '@ariadne/plugin-sdk'
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
function workspaceTool(
  spec: Tool['spec'],
  run: (
    args: Record<string, unknown>,
    workspace: Workspace,
    ctx: ToolContext,
  ) => Promise<ToolResult>,
): Tool {
  return {
    spec,
    async run(args, ctx) {
      const workspace = await ctx.workspace()
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

const listDir = workspaceTool(
  {
    name: 'list_dir',
    description: 'List a directory of your workspace. Paths are relative to the workspace root.',
    parameters: {
      type: 'object',
      properties: { path: { type: 'string', description: 'Default ".".' } },
    },
  },
  async (args, workspace) => {
    const entries = await workspace.list(optionalStr(args, 'path') || '.')
    return {
      content:
        entries.map((e) => (e.kind === 'directory' ? `${e.name}/` : e.name)).join('\n') ||
        '(empty)',
    }
  },
)

const readFile = workspaceTool(
  {
    name: 'read_file',
    description: `Read a text file of your workspace, with line numbers. Reads ${DEFAULT_LINES} lines from "offset" (1-based) unless "limit" says otherwise.`,
    parameters: {
      type: 'object',
      properties: {
        path: { type: 'string' },
        offset: { type: 'integer' },
        limit: { type: 'integer' },
      },
      required: ['path'],
    },
  },
  async (args, workspace) => {
    const lines = (await workspace.readFile(str(args, 'path'))).split('\n')
    const offset = Math.max(1, optionalInt(args, 'offset') ?? 1)
    const limit = Math.max(1, optionalInt(args, 'limit') ?? DEFAULT_LINES)
    const shown = lines.slice(offset - 1, offset - 1 + limit)
    const body = shown.map((line, i) => `${String(offset + i).padStart(5)}\t${line}`).join('\n')
    const end = offset - 1 + shown.length
    const more = end < lines.length ? `\n[lines ${offset}-${end} of ${lines.length}]` : ''
    return { content: body + more }
  },
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
