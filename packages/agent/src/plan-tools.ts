import { newId, type PlanConfirmation, type PlanNodeKind, type PlanOp } from '@ariadne/core'
import { createTranslator, type Locale } from '@ariadne/i18n'
import { json, optionalStr, str, ToolInputError, type Tool } from './tool-helpers'

const KINDS: PlanNodeKind[] = ['task', 'milestone', 'decision']

const opSchema = {
  type: 'object',
  properties: {
    op: { type: 'string', enum: ['add', 'update', 'remove', 'link', 'unlink'] },
    ref: {
      type: 'string',
      description: 'add: a name for the new node, to refer to it elsewhere in this call.',
    },
    id: { type: 'string', description: 'update/remove: the node id.' },
    kind: { type: 'string', enum: KINDS, description: 'add: default "task".' },
    title: { type: 'string' },
    description: { type: 'string' },
    acceptance: { type: 'string', description: 'How the user will judge the node done.' },
    dependsOn: {
      type: 'array',
      items: { type: 'string' },
      description: 'add: ids or refs of nodes this one depends on.',
    },
    from: { type: 'string', description: 'link/unlink: the node depended on (id or ref).' },
    to: { type: 'string', description: 'link/unlink: the dependent node (id or ref).' },
  },
  required: ['op'],
}

/**
 * Turn the model's ops into stored ones: new nodes get ids, refs resolve to them, and the
 * dependencies of added nodes become links after every node exists.
 */
export function resolvePlanOps(raw: unknown): { ops: PlanOp[]; refs: Record<string, string> } {
  if (!Array.isArray(raw) || !raw.length)
    throw new ToolInputError('"ops" must be a non-empty array')
  const items = raw.map((item) => (item ?? {}) as Record<string, unknown>)
  const refs: Record<string, string> = {}
  for (const item of items) {
    if (item.op === 'add' && typeof item.ref === 'string' && item.ref) {
      if (refs[item.ref]) throw new ToolInputError(`ref "${item.ref}" is used twice`)
      refs[item.ref] = newId('nod')
    }
  }
  const resolve = (value: unknown, where: string) => {
    if (typeof value !== 'string' || !value) throw new ToolInputError(`${where} is missing`)
    return refs[value] ?? value
  }
  const ops: PlanOp[] = []
  const links: PlanOp[] = []
  items.forEach((item, index) => {
    const where = `ops[${index}]`
    const text = (key: string) => optionalStr(item, key)
    switch (item.op) {
      case 'add': {
        const kind = (item.kind ?? 'task') as PlanNodeKind
        if (!KINDS.includes(kind)) throw new ToolInputError(`${where}.kind is not a node kind`)
        const id =
          typeof item.ref === 'string' && item.ref ? (refs[item.ref] as string) : newId('nod')
        ops.push({
          op: 'add',
          id,
          kind,
          title: str(item, 'title'),
          description: text('description') ?? '',
          acceptance: text('acceptance') ?? '',
        })
        const deps = item.dependsOn ?? []
        if (!Array.isArray(deps)) throw new ToolInputError(`${where}.dependsOn must be an array`)
        for (const dep of deps)
          links.push({ op: 'link', from: resolve(dep, `${where}.dependsOn`), to: id })
        break
      }
      case 'update': {
        const op: PlanOp = { op: 'update', id: resolve(item.id, `${where}.id`) }
        for (const key of ['title', 'description', 'acceptance'] as const) {
          const value = text(key)
          if (value !== undefined) op[key] = value
        }
        ops.push(op)
        break
      }
      case 'remove':
        ops.push({ op: 'remove', id: resolve(item.id, `${where}.id`) })
        break
      case 'link':
      case 'unlink':
        ops.push({
          op: item.op,
          from: resolve(item.from, `${where}.from`),
          to: resolve(item.to, `${where}.to`),
        })
        break
      default:
        throw new ToolInputError(`${where}.op is not a plan op`)
    }
  })
  return { ops: [...ops, ...links], refs }
}

/**
 * The question the user answers before a big change takes effect: the reason, then
 * every change spelled out, in the user's language.
 */
export function confirmation(locale: Locale, reason: string): PlanConfirmation {
  const t = createTranslator(locale)
  return {
    question: (changes) =>
      [
        t('plan.confirm.question', { reason }),
        ...changes.map((change) => `• ${t(change.key, change.params)}`),
      ].join('\n'),
    apply: { label: t('plan.confirm.apply'), consequence: t('plan.confirm.applyConsequence') },
    keep: { label: t('plan.confirm.keep'), consequence: t('plan.confirm.keepConsequence') },
  }
}

export const readPlan: Tool = {
  spec: {
    name: 'read_plan',
    description:
      "Read the project's plan graph: nodes with their dependencies, acceptance, status and threads, plus changes waiting on the user.",
    parameters: { type: 'object', properties: {} },
  },
  run(_args, ctx) {
    const plan = ctx.app.getPlan(ctx.projectId)
    return json({
      nodes: plan.nodes.map((node) => ({
        id: node.id,
        kind: node.kind,
        title: node.title,
        description: node.description,
        acceptance: node.acceptance,
        dependsOn: node.dependsOn,
        status: node.status,
        threads: node.threads,
        // Numbers only: titles are written by whoever filed the issue; read_issue shows them.
        issues: node.issues.map((issue) => issue.number),
      })),
      waitingOnUser: plan.proposals
        .filter((p) => p.status === 'open')
        .map((p) => ({ proposalId: p.id, reason: p.reason })),
      agreedButNotApplied: plan.proposals
        .filter((p) => p.status === 'stale')
        .map((p) => ({ proposalId: p.id, reason: p.reason, because: p.staleReason })),
      recentChanges: plan.revisions.slice(0, 5).map((r) => ({
        revisionId: r.id,
        author: r.author,
        reason: r.reason,
        reverted: !!r.revertedBy,
      })),
    })
  },
}

const updatePlan: Tool = {
  spec: {
    name: 'update_plan',
    description:
      'Change the plan graph in one revision with a reason the user reads. Adding nodes, editing titles or descriptions and changing links apply at once and the user can revert them. Removing nodes, changing acceptance, or adding or changing a milestone waits for the user to agree; the answer reaches you as <user_answer>. Batch related changes into one call so the user confirms once.',
    parameters: {
      type: 'object',
      properties: {
        reason: { type: 'string', description: "Why, in one line, in the user's language." },
        ops: { type: 'array', items: opSchema },
      },
      required: ['reason', 'ops'],
    },
  },
  run(args, ctx) {
    const reason = str(args, 'reason')
    const { ops, refs } = resolvePlanOps(args.ops)
    const change = ctx.app.revisePlan(ctx.projectId, {
      author: 'coordinator',
      reason,
      ops,
      confirmation: confirmation(ctx.locale, reason),
    })
    return json(
      change.status === 'applied'
        ? { status: 'applied', revisionId: change.revisionId, ids: refs }
        : {
            status: 'waiting for the user to agree',
            proposalId: change.proposalId,
            ids: refs,
            note: 'Nodes added by this change do not exist until the user agrees.',
          },
    )
  },
}

const revertPlanChange: Tool = {
  spec: {
    name: 'revert_plan_change',
    description:
      'Undo one earlier revision of the plan, e.g. one the user objected to. Like update_plan, an undo that removes nodes or changes acceptance or milestones waits for the user to agree.',
    parameters: {
      type: 'object',
      properties: { revisionId: { type: 'string' }, reason: { type: 'string' } },
      required: ['revisionId', 'reason'],
    },
  },
  run(args, ctx) {
    const reason = str(args, 'reason')
    const change = ctx.app.revertPlanRevision(
      ctx.projectId,
      str(args, 'revisionId'),
      'coordinator',
      reason,
      confirmation(ctx.locale, reason),
    )
    return json(
      change.status === 'applied'
        ? { status: 'reverted', revisionId: change.revisionId }
        : { status: 'waiting for the user to agree', proposalId: change.proposalId },
    )
  },
}

export const planTools: Tool[] = [readPlan, updatePlan, revertPlanChange]
