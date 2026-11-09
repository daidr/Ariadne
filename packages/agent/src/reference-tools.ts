import { createHash } from 'node:crypto'
import { confirmBlock, MAX_REFERENCES } from '@ariadne/core'
import { createTranslator } from '@ariadne/i18n'
import { ToolInputError, type Tool } from './tool-helpers'

/** A kind of source a reference can be, as the coordinator picks among them. */
export interface ReferenceKind {
  kind: string
  title: string
  /** Its settings that are not secrets, and which of them must be there. */
  settings: string[]
  required: string[]
}

/** Names the reference a yes is for: its kind and its settings, hashed. */
export function referencePurpose(kind: string, settings: Record<string, unknown>) {
  return `reference:${createHash('sha256')
    .update(JSON.stringify([kind, Object.entries(settings).toSorted()]))
    .digest('hex')}`
}

/**
 * What the card shows of the source: where it is, and every other setting as given (an
 * instance's address, a host), so nothing the user agrees to is out of sight.
 */
function cardDetail(title: string, where: string, settings: Record<string, unknown>): string {
  const more = Object.entries(settings)
    .filter(([, value]) => typeof value !== 'string' || !where.includes(value))
    .map(([name, value]) => `${name}: ${typeof value === 'string' ? value : JSON.stringify(value)}`)
  return [`${title} ${where}`, ...more].join('\n')
}

/**
 * The coordinator adds a source the project only reads, when the user asks for one in
 * words. It is connected first, so a card is never shown for one that cannot be read; the
 * user's yes on the card is what adds it. Secrets are never the model's to give: a source
 * that needs one of its own is added in the project's settings.
 */
export function addReferenceTool(kinds: ReferenceKind[]): Tool {
  const listed = kinds
    .map(
      (kind) =>
        `"${kind.kind}" (${kind.title}): ${
          kind.settings
            .map((name) => (kind.required.includes(name) ? name : `${name} (optional)`))
            .join(', ') || 'no settings'
        }`,
    )
    .join('; ')
  return {
    spec: {
      name: 'add_reference',
      description: `Add a reference source to the project: another repository or directory its conversations read (list_dir, read_file, search, find_files with "source") and never change. Use it when the user asks to look at, follow or compare with code that is not the project's own. The user confirms on a card; their answer arrives as a new turn, and you call this again the same way to add it. Kinds and their settings: ${listed}. A token is never passed here: one of the project's own source is reused for the same kind, and otherwise the user adds the source in the project's settings.`,
      parameters: {
        type: 'object',
        properties: {
          kind: { type: 'string', enum: kinds.map((kind) => kind.kind) },
          settings: { type: 'object', description: 'The settings of that kind, by name.' },
        },
        required: ['kind', 'settings'],
      },
    },
    async run(args, ctx) {
      const kind = kinds.find((known) => known.kind === args.kind)
      if (!kind)
        throw new ToolInputError(`"kind" must be one of: ${kinds.map((k) => k.kind).join(', ')}.`)
      const given = (
        args.settings && typeof args.settings === 'object' && !Array.isArray(args.settings)
          ? args.settings
          : {}
      ) as Record<string, unknown>
      // Only the settings the kind names, and none of its secrets.
      const settings: Record<string, unknown> = {}
      for (const name of kind.settings) {
        const value = typeof given[name] === 'string' ? given[name].trim() : given[name]
        if (value !== undefined && value !== null && value !== '') settings[name] = value
      }
      const missing = kind.required.filter((name) => settings[name] === undefined)
      if (missing.length) {
        throw new ToolInputError(`"settings" lacks: ${missing.join(', ')}.`)
      }
      const { references } = ctx.app.getProject(ctx.projectId)
      const same = (other: Record<string, unknown>) =>
        JSON.stringify(Object.entries(other).toSorted()) ===
        JSON.stringify(Object.entries(settings).toSorted())
      const known = references.find((ref) => ref.kind === kind.kind && same(ref.settings))
      if (known) {
        return { content: `Already a reference source: read it with source "${known.name}".` }
      }
      if (references.length >= MAX_REFERENCES) {
        return {
          content: `The project already has ${MAX_REFERENCES} reference sources, the most it takes. The user can remove one in the project's settings.`,
          isError: true,
        }
      }
      if (!ctx.checkReference) {
        return { content: 'Reference sources are not available here.', isError: true }
      }
      // Throws what went wrong for the model to read: a typo, or a token it does not have.
      const { where } = await ctx.checkReference({ kind: kind.kind, settings })
      const t = createTranslator(ctx.locale)
      const approval = ctx.app.callApproval(
        ctx.projectId,
        ctx.conversationId,
        referencePurpose(kind.kind, settings),
        () =>
          confirmBlock({
            title: t('reference.add.question'),
            detail: cardDetail(kind.title, where, settings),
            description: t('reference.add.note'),
            approveText: t('reference.add.yes'),
            denyText: t('reference.add.no'),
          }),
        { inChat: true },
      )
      if (approval.state === 'approved') {
        ctx.app.useApproval(ctx.projectId, {
          threadId: ctx.conversationId,
          attentionId: approval.attentionId,
          what: 'reference',
        })
        const added = ctx.app.addProjectReference(
          ctx.projectId,
          { kind: kind.kind, settings },
          where,
        )
        return {
          content: `Added. Read it with source "${added.name}" in list_dir, read_file, search and find_files; threads can too, from their next turn.`,
        }
      }
      if (approval.state === 'declined') {
        return {
          content: 'The user declined adding this reference source. Go on without it.',
          isError: true,
        }
      }
      return {
        content:
          approval.state === 'asked'
            ? 'It can be read. The user was asked whether to add it; their answer arrives as a new turn. Call this again, the same way, only if they agree.'
            : 'The user is already being asked about this reference source. Their answer arrives as a new turn.',
      }
    },
  }
}
