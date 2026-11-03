import type { PlanView } from '@ariadne/core'
import { reactive } from 'vue'
import type { Positions } from './layout'

/**
 * What the plan views share in this browser: the change being previewed on the graph, the
 * revision the user last acknowledged, and where they moved nodes. Kept per project.
 */
export const planUi = reactive({
  /** The open proposal shown on the graph instead of the plan as it is. */
  previewId: null as string | null,
  /** Per project: the newest revision when the user last said "got it". */
  seen: {} as Record<string, string | null>,
  /** Bumped when saved positions are cleared, so graphs lay out again. */
  layoutVersion: 0,
})

const SEEN_KEY = (projectId: string) => `ariadne.planSeen.${projectId}`
const LAYOUT_KEY = (projectId: string) => `ariadne.planLayout.${projectId}`

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

export function acknowledge(plan: PlanView): void {
  const newest = plan.revisions[0]?.id ?? null
  planUi.seen[plan.projectId] = newest
  write(SEEN_KEY(plan.projectId), newest ?? '')
}

/**
 * Where the user moved nodes. Not reactive on purpose: moving a node must not rebuild the
 * document under the pointer, so graphs read it only when they rebuild for another reason.
 */
const positions = new Map<string, Positions>()

export function savedPositions(projectId: string): Positions {
  let saved = positions.get(projectId)
  if (!saved) {
    try {
      saved = JSON.parse(read(LAYOUT_KEY(projectId)) ?? '{}') as Positions
    } catch {
      saved = {}
    }
    positions.set(projectId, saved)
  }
  return saved
}

export function savePositions(projectId: string, moved: Positions): void {
  const saved = savedPositions(projectId)
  Object.assign(saved, moved)
  write(LAYOUT_KEY(projectId), JSON.stringify(saved))
}

export function clearPositions(projectId: string): void {
  positions.set(projectId, {})
  write(LAYOUT_KEY(projectId), null)
  planUi.layoutVersion += 1
}
