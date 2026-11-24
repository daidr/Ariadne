import type { PlanNodeView, PlanOp, PlanRevisionView, PlanView } from '@ariadne/core'

/** Prefix for visual group container node IDs in the flow document. */
export const GROUP_PREFIX = 'grp:'
export const isGroupId = (id: string) => id.startsWith(GROUP_PREFIX)
export const groupIdOf = (name: string) => `${GROUP_PREFIX}${name}`
export const groupNameOf = (node: Pick<PlanNodeView, 'group'>) => (node.group ?? '').trim()

/** The coordinator's changes that still stand, newest first. */
function standing(plan: PlanView | null): PlanRevisionView[] {
  return (plan?.revisions ?? []).filter(
    (r) => r.author === 'coordinator' && !r.revertedBy && !r.revertOf,
  )
}

/** The newest change the coordinator made that still stands. */
export function latestCoordinatorChange(plan: PlanView | null) {
  return standing(plan)[0]
}

/** The nodes a change touches, including both ends of the dependencies it adds or drops. */
export function touchedBy(ops: readonly PlanOp[]): Set<string> {
  const ids = new Set<string>()
  for (const op of ops) {
    if (op.op === 'link' || op.op === 'unlink') {
      ids.add(op.from)
      ids.add(op.to)
    } else ids.add(op.id)
  }
  return ids
}

/**
 * The coordinator's changes the user has not seen yet: those newer than `seen`, the
 * newest revision when the user last caught up. `nodes` maps each node they touch to the
 * newest such revision, so a node the user looked at since stops standing out. The plan
 * lists only its latest revisions; when `seen` is not among them, `truncated` says that
 * older ones are missing.
 */
export function freshChanges(plan: PlanView | null, seen: string | null) {
  const revisions: PlanRevisionView[] = []
  let found = false
  for (const revision of plan?.revisions ?? []) {
    if (revision.id === seen) {
      found = true
      break
    }
    if (revision.author === 'coordinator' && !revision.revertedBy && !revision.revertOf) {
      revisions.push(revision)
    }
  }
  // Only nodes still on the plan: one removed since cannot be looked at.
  const present = new Set(plan?.nodes.map((node) => node.id))
  const nodes = new Map<string, string>()
  for (const revision of revisions) {
    for (const id of touchedBy(revision.ops)) {
      if (present.has(id) && !nodes.has(id)) nodes.set(id, revision.id)
    }
  }
  return { revisions, nodes, truncated: !!seen && !found && revisions.length > 0 }
}

export interface PlanCompletionSummary {
  /** Nodes belonging to an active (not-all-done) group or connected subgraph. */
  active: PlanNodeView[]
  /** IDs of nodes in fully completed lone nodes, groups, or connected subgraphs. */
  completedIds: Set<string>
}

/**
 * Partition plan nodes into active work and auto-completed items.
 * Two nodes belong to the same completion unit if they share a non-empty `group` OR are
 * linked by a dependency edge. A unit is completed only when every node in it is `done`,
 * so partially completed groups and dependency chains keep their full context visible.
 */
export function partitionPlanNodes(
  nodes: PlanNodeView[],
  edges: { from: string; to: string }[] = nodes.flatMap((node) =>
    node.dependsOn.map((from) => ({ from, to: node.id })),
  ),
): PlanCompletionSummary {
  const root = new Map(nodes.map((node) => [node.id, node.id]))
  const find = (id: string): string => {
    let at = id
    while (root.get(at) !== at) at = root.get(at)!
    root.set(id, at)
    return at
  }
  const unite = (a: string, b: string) => {
    const ra = find(a)
    const rb = find(b)
    if (ra !== rb) root.set(ra, rb)
  }
  for (const { from, to } of edges) {
    if (root.has(from) && root.has(to)) unite(from, to)
  }
  const groupLeader = new Map<string, string>()
  for (const node of nodes) {
    const group = groupNameOf(node)
    if (!group) continue
    const leader = groupLeader.get(group)
    if (leader) unite(leader, node.id)
    else groupLeader.set(group, node.id)
  }
  const units = new Map<string, PlanNodeView[]>()
  for (const node of nodes) {
    const key = find(node.id)
    const list = units.get(key)
    if (list) list.push(node)
    else units.set(key, [node])
  }
  const completedIds = new Set<string>()
  for (const unit of units.values()) {
    if (unit.length && unit.every((node) => node.status === 'done')) {
      for (const node of unit) completedIds.add(node.id)
    }
  }
  const active = nodes.filter((node) => !completedIds.has(node.id))
  return { active, completedIds }
}

/**
 * For each node work started on, or that is done, its dependencies not done yet, by the
 * dependency they hold it up through: the plan meant it to wait for them. A milestone the
 * user let pass counts as done; one nobody worked on, like an open decision, is looked
 * through to what comes before it, as work after it says it was passed.
 */
export function aheadOf(nodes: PlanNodeView[]): Map<string, Map<string, string[]>> {
  const byId = new Map(nodes.map((node) => [node.id, node]))
  const open = (id: string, seen: Set<string>): string[] => {
    const dep = byId.get(id)
    if (!dep || dep.status === 'done' || seen.has(id)) return []
    seen.add(id)
    if (dep.kind === 'task' || dep.status !== 'none') return [dep.title]
    return dep.dependsOn.flatMap((from) => open(from, seen))
  }
  const ahead = new Map<string, Map<string, string[]>>()
  for (const node of nodes) {
    if (node.status === 'none') continue
    const through = new Map<string, string[]>()
    for (const id of node.dependsOn) {
      const titles = open(id, new Set())
      if (titles.length) through.set(id, titles)
    }
    if (through.size) ahead.set(node.id, through)
  }
  return ahead
}

/** For each node work started on too early, the titles of the dependencies not done. */
export function startedAhead(nodes: PlanNodeView[]): Map<string, string[]> {
  return new Map(
    [...aheadOf(nodes)].map(([id, through]) => [id, [...new Set([...through.values()].flat())]]),
  )
}
