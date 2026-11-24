import type { PlanPreview, PlanView } from '@ariadne/core'
import { reactive, type InjectionKey } from 'vue'
import { freshChanges } from './helpers'

/** Injection key allowing PlanGroupNode to toggle local collapse state inside PlanGraph. */
export const PLAN_GROUP_TOGGLE: InjectionKey<(groupId: string) => void> =
  Symbol('plan-group-toggle')

/**
 * What the plan views share in this browser: the change being previewed on the graph, the
 * revision the user last acknowledged, whether completed items are shown, and which visual
 * groups are folded locally. Kept per project.
 */
export const planUi = reactive({
  /** The open proposal shown on the graph instead of the plan as it is. */
  previewId: null as string | null,
  /** Per project: the newest revision when the user last caught up. */
  seen: {} as Record<string, string | null>,
  /** Per project: nodes looked at since, with the newest revision touching each then. */
  seenNodes: {} as Record<string, Record<string, string>>,
  /** Per project: whether auto-hidden completed nodes and groups are shown. Defaults to false. */
  showCompleted: {} as Record<string, boolean>,
  /** Per project: visual group container IDs folded locally by the user. */
  collapsedGroups: {} as Record<string, Record<string, boolean>>,
})

const SEEN_KEY = (projectId: string) => `ariadne.planSeen.${projectId}`
const SEEN_NODES_KEY = (projectId: string) => `ariadne.planSeenNodes.${projectId}`

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    // Only remembered for this page.
  }
}

/**
 * Load the revision the user last acknowledged. The first time a plan is opened in this
 * browser everything counts as seen, so only what the coordinator changes from then on
 * stands out. Called when a plan is shown, outside of computed values.
 */
export function loadSeen(plan: PlanView): void {
  if (plan.projectId in planUi.seen) return
  try {
    planUi.seenNodes[plan.projectId] = JSON.parse(read(SEEN_NODES_KEY(plan.projectId)) ?? '{}')
  } catch {
    planUi.seenNodes[plan.projectId] = {}
  }
  const saved = read(SEEN_KEY(plan.projectId))
  const newest = plan.revisions[0]?.id ?? null
  planUi.seen[plan.projectId] = saved ?? newest
  // An empty plan is remembered as '' (nothing seen), so its first changes stand out.
  if (saved === null) write(SEEN_KEY(plan.projectId), newest ?? '')
}

/** The revision the user last acknowledged; everything when it is not loaded yet. */
export function seenRevision(plan: PlanView): string | null {
  return plan.projectId in planUi.seen
    ? (planUi.seen[plan.projectId] ?? null)
    : (plan.revisions[0]?.id ?? null)
}

/**
 * Nodes the coordinator changed that the user has not looked at since. Looking at a node
 * (selecting it) is what clears its mark; there is nothing else to dismiss.
 */
export function unseenNodes(plan: PlanView): Set<string> {
  const looked = planUi.seenNodes[plan.projectId] ?? {}
  const nodes = new Set<string>()
  for (const [id, revision] of freshChanges(plan, seenRevision(plan)).nodes) {
    if (looked[id] !== revision) nodes.add(id)
  }
  return nodes
}

/** The user looked at a node: its mark goes until the coordinator changes it again. */
export function markNodeSeen(plan: PlanView, nodeId: string): void {
  const fresh = freshChanges(plan, seenRevision(plan)).nodes
  const revision = fresh.get(nodeId)
  if (!revision) return
  const looked = { ...planUi.seenNodes[plan.projectId], [nodeId]: revision }
  // Once every changed node was looked at, the user has caught up with the plan.
  if ([...fresh].every(([id, rev]) => looked[id] === rev)) {
    const newest = plan.revisions[0]?.id ?? null
    planUi.seen[plan.projectId] = newest
    write(SEEN_KEY(plan.projectId), newest ?? '')
    planUi.seenNodes[plan.projectId] = {}
    write(SEEN_NODES_KEY(plan.projectId), null)
    return
  }
  planUi.seenNodes[plan.projectId] = looked
  write(SEEN_NODES_KEY(plan.projectId), JSON.stringify(looked))
}

export function showingCompleted(projectId: string): boolean {
  return !!planUi.showCompleted[projectId]
}

export function setShowCompleted(projectId: string, show: boolean): void {
  planUi.showCompleted[projectId] = show
}

export function toggleShowCompleted(projectId: string): void {
  planUi.showCompleted[projectId] = !planUi.showCompleted[projectId]
}

export function collapsedGroupsOf(projectId: string): Set<string> {
  const map = planUi.collapsedGroups[projectId]
  const out = new Set<string>()
  if (!map) return out
  for (const [id, collapsed] of Object.entries(map)) {
    if (collapsed) out.add(id)
  }
  return out
}

export function toggleGroupCollapsed(projectId: string, groupId: string): void {
  const current = planUi.collapsedGroups[projectId] ?? {}
  planUi.collapsedGroups[projectId] = {
    ...current,
    [groupId]: !current[groupId],
  }
}

/** The active change preview matching `plan`, or `null` when none fits or its preview is stale. */
export function activePreview(plan: PlanView | null): PlanPreview | null {
  if (!plan || !planUi.previewId) return null
  const proposal = plan.proposals.find((p) => p.status === 'open' && p.id === planUi.previewId)
  return proposal?.preview ?? null
}
