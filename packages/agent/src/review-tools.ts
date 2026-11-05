import { DomainError, type ReviewComment, type ReviewCommentKind } from '@ariadne/core'
import { createTranslator } from '@ariadne/i18n'
import type { BranchChanges } from '@ariadne/plugin-sdk'
import { fenced } from './prompts'
import { json, optionalStr, str, ToolInputError, type Tool, type ToolContext } from './tool-helpers'
import { readWorkspaceTools } from './workspace-tools'

/** Read a skill whole, from the list in the instructions. */
export const readSkill: Tool = {
  readOnly: true,
  spec: {
    name: 'read_skill',
    description:
      'Read a skill whole: a way of working from the list of skills in your instructions. Read it before work it fits, and follow it.',
    parameters: {
      type: 'object',
      properties: { name: { type: 'string', description: 'As the list names it.' } },
      required: ['name'],
    },
  },
  run(args, ctx) {
    const name = str(args, 'name').trim().replace(/^\//, '')
    const skill = ctx.skills?.().get(name) ?? null
    if (!skill) {
      const names =
        ctx
          .skills?.()
          .list()
          .map((s) => s.name) ?? []
      throw new ToolInputError(
        names.length
          ? `There is no skill "${name}". Skills: ${names.join(', ')}`
          : 'There are no skills in this project.',
      )
    }
    const block = fenced('skill', skill.text, `name="${skill.name}"`)
    return {
      content: `The skill "${skill.name}" (${skill.source === 'repo' ? "the repository's" : "the user's"}), inside <${block.tag}>:\n${block.text}`,
    }
  },
}

/** The author a reviewing thread reviews, with its branch. */
function authorOf(ctx: ToolContext) {
  const reviewer = ctx.app.getThread(ctx.conversationId)
  if (!reviewer.reviewOf) throw new DomainError('error.notReviewer')
  const author = ctx.app.getThread(reviewer.reviewOf)
  const branch = author.workspace?.branch
  const base = author.workspace?.base
  if (!branch || !base) throw new ToolInputError('The author has no branch to review.')
  return { author, branch, base }
}

async function branchChanges(ctx: ToolContext): Promise<BranchChanges> {
  const { author, branch, base } = authorOf(ctx)
  const branches = ctx.branches ? await ctx.branches() : null
  if (!branches) throw new ToolInputError('The changes cannot be read here.')
  const changes = await branches.changes({ threadId: author.id, branch }, base)
  if (!changes) throw new ToolInputError('The author has no branch to review.')
  return changes
}

/** Changed lines of one file at most; a larger change is read with read_file instead. */
const MAX_DIFF_LINES = 6000
const MAX_DIFF_CHARS = 60_000
const CONTEXT = 3

type Op = { mark: ' ' | '-' | '+'; text: string; line: number | null }

/**
 * A file's changes as hunks of lines, each numbered as the new version numbers it (removed
 * lines have no number), so a comment can name its line. `null` when it is too large.
 */
export function numberedDiff(before: string, after: string): string | null {
  const a = before === '' ? [] : before.split('\n')
  const b = after === '' ? [] : after.split('\n')
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--
    endB--
  }
  const midA = a.slice(start, endA)
  const midB = b.slice(start, endB)
  if (midA.length + midB.length > MAX_DIFF_LINES || midA.length * midB.length > 9_000_000) {
    return null
  }
  const n = midA.length
  const m = midB.length
  const table = new Uint32Array((n + 1) * (m + 1))
  const at = (i: number, j: number) => i * (m + 1) + j
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[at(i, j)] =
        midA[i] === midB[j]
          ? table[at(i + 1, j + 1)]! + 1
          : Math.max(table[at(i + 1, j)]!, table[at(i, j + 1)]!)
    }
  }
  const ops: Op[] = []
  for (let k = 0; k < start; k++) ops.push({ mark: ' ', text: b[k]!, line: k + 1 })
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && midA[i] === midB[j]) {
      ops.push({ mark: ' ', text: midB[j]!, line: start + j + 1 })
      i++
      j++
    } else if (i < n && (j === m || table[at(i + 1, j)]! >= table[at(i, j + 1)]!)) {
      ops.push({ mark: '-', text: midA[i]!, line: null })
      i++
    } else {
      ops.push({ mark: '+', text: midB[j]!, line: start + j + 1 })
      j++
    }
  }
  for (let k = endB; k < b.length; k++) ops.push({ mark: ' ', text: b[k]!, line: k + 1 })
  // Hunks: changed lines with a few unchanged ones around them.
  const keep = new Set<number>()
  ops.forEach((op, index) => {
    if (op.mark === ' ') return
    for (let k = index - CONTEXT; k <= index + CONTEXT; k++) keep.add(k)
  })
  const width = String(b.length).length
  const out: string[] = []
  let last = -2
  for (let index = 0; index < ops.length; index++) {
    if (!keep.has(index)) continue
    if (index !== last + 1 && out.length) out.push('…')
    const op = ops[index]!
    const number = op.line === null ? ''.padStart(width) : String(op.line).padStart(width)
    out.push(
      `${op.mark} ${number} │ ${op.text.length > 2000 ? `${op.text.slice(0, 2000)}…` : op.text}`,
    )
    last = index
  }
  const text = out.join('\n')
  return text.length > MAX_DIFF_CHARS ? `${text.slice(0, MAX_DIFF_CHARS)}\n[cut]` : text
}

export const readChanges: Tool = {
  readOnly: true,
  spec: {
    name: 'read_changes',
    description:
      "Read the changes under review. Without path: the branch's commits and changed files, with lines added and removed. With path: that file's changes, every line marked + (added), - (removed) or blank (unchanged) and numbered as the new version numbers it; comment on those numbers.",
    parameters: {
      type: 'object',
      properties: { path: { type: 'string', description: 'A changed file, as listed.' } },
    },
  },
  async run(args, ctx) {
    const changes = await branchChanges(ctx)
    const path = optionalStr(args, 'path')?.trim()
    if (!path) {
      return json({
        branch: changes.branch,
        base: changes.base,
        head: changes.head.slice(0, 12),
        commits: changes.commits.map((commit) => commit.subject),
        files: changes.files.map((file) => ({
          path: file.path,
          status: file.status,
          ...(file.previousPath ? { from: file.previousPath } : {}),
          added: file.added,
          removed: file.removed,
        })),
        ...(changes.uncommitted.length ? { uncommitted: changes.uncommitted.length } : {}),
      })
    }
    const file = changes.files.find((item) => item.path === path)
    if (!file) {
      throw new ToolInputError(`${path} is not among the changed files; read_changes lists them.`)
    }
    const branches = (await ctx.branches?.())!
    const versions = await branches.file({
      from: changes.from,
      head: changes.head,
      path: file.path,
      previousPath: file.previousPath,
    })
    if (versions.skipped) {
      return {
        content: `${path} is ${versions.skipped === 'binary' ? 'binary' : 'too large'}; it is not shown.`,
      }
    }
    const diff = numberedDiff(versions.before ?? '', versions.after ?? '')
    if (diff === null) {
      return {
        content: `${path} changed too much to show as a diff; read it with read_file (the new version).`,
      }
    }
    const head = file.previousPath ? `${file.previousPath} → ${path}` : path
    return { content: `${head} (${file.status})\n${diff || '(no line changed)'}` }
  },
}

const COMMENT_KINDS: readonly ReviewCommentKind[] = ['bug', 'acceptance', 'security']

export const submitReview: Tool = {
  spec: {
    name: 'submit_review',
    description:
      'Give your verdict on the round under review. "pass" when nothing needs fixing; "changes" with one comment per problem, on the line of the new version (as read_changes or read_file number it). Only bugs, missed acceptance criteria and security problems; never style.',
    parameters: {
      type: 'object',
      properties: {
        verdict: { type: 'string', enum: ['pass', 'changes'] },
        summary: {
          type: 'string',
          description:
            "One or two sentences in the user's language: what you checked, and the verdict.",
        },
        comments: {
          type: 'array',
          description: 'Required for "changes", empty for "pass".',
          items: {
            type: 'object',
            properties: {
              path: { type: 'string' },
              line: { type: 'integer', description: "The line's number in the new version." },
              kind: { type: 'string', enum: COMMENT_KINDS },
              body: {
                type: 'string',
                description:
                  "In the user's language: what goes wrong, when, and what would fix it.",
              },
            },
            required: ['path', 'line', 'kind', 'body'],
          },
        },
      },
      required: ['verdict', 'summary'],
    },
  },
  async run(args, ctx) {
    const verdict = args.verdict
    if (verdict !== 'pass' && verdict !== 'changes') {
      throw new ToolInputError('"verdict" must be "pass" or "changes"')
    }
    const summary = str(args, 'summary')
    const raw = args.comments ?? []
    if (!Array.isArray(raw)) throw new ToolInputError('"comments" must be an array')
    if (verdict === 'changes' && !raw.length) {
      throw new ToolInputError('"changes" needs at least one comment; with none, it is a "pass"')
    }
    if (verdict === 'pass' && raw.length) {
      throw new ToolInputError('A "pass" has no comments; with problems to fix, it is "changes"')
    }
    const workspace = await ctx.workspace()
    const comments: ReviewComment[] = []
    for (const [index, item] of raw.entries()) {
      const { path, line, kind, body } = (item ?? {}) as Record<string, unknown>
      if (typeof path !== 'string' || !path.trim()) {
        throw new ToolInputError(`comments[${index}] needs a path`)
      }
      if (!Number.isInteger(line) || (line as number) < 1) {
        throw new ToolInputError(`comments[${index}] needs a line number from 1`)
      }
      if (!COMMENT_KINDS.includes(kind as ReviewCommentKind)) {
        throw new ToolInputError(`comments[${index}].kind must be bug, acceptance or security`)
      }
      if (typeof body !== 'string' || !body.trim()) {
        throw new ToolInputError(`comments[${index}] needs a body`)
      }
      const file = path.trim().replace(/^\.\/+/, '')
      let text: string
      try {
        text = new TextDecoder().decode(await workspace.readBytes(file, 4 * 1024 * 1024))
      } catch {
        throw new ToolInputError(`comments[${index}]: ${file} cannot be read in the workspace`)
      }
      const lines = text.split('\n')
      if ((line as number) > lines.length) {
        throw new ToolInputError(
          `comments[${index}]: ${file} has ${lines.length} lines, not ${line as number}`,
        )
      }
      comments.push({
        path: file,
        line: line as number,
        quote: lines[(line as number) - 1]!.slice(0, 500),
        body: body.trim(),
        kind: kind as ReviewCommentKind,
      })
    }
    const t = createTranslator(ctx.locale)
    const round = ctx.app.submitReview(ctx.conversationId, {
      verdict,
      summary,
      comments,
      headline:
        verdict === 'pass'
          ? t('review.passHeadline')
          : t('review.changesHeadline', { count: comments.length }),
    })
    return {
      content:
        verdict === 'pass'
          ? `Submitted: round ${round.round} passed, and the changes go to the user. End your turn with no text.`
          : `Submitted: ${comments.length} comment(s) went to the author for round ${round.round}. End your turn with no text.`,
    }
  },
}

/**
 * A reviewing thread's tools: its todo list, the skills, reading the author's workspace
 * and changes, and its verdict. Nothing that writes or runs.
 */
export const reviewerTools = (updateTodo: Tool): Tool[] => [
  updateTodo,
  readSkill,
  ...readWorkspaceTools,
  readChanges,
  submitReview,
]
