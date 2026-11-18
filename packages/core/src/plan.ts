import type { PlanNodeFields } from './events'

/** What a change does to one node or dependency, for drawing it on the graph. */
export type PlanMark = 'added' | 'removed' | 'changed'

export interface PlanPreview {
  /** Every node before or after the change, in plan order, removed ones included. */
  nodes: ({ id: string; mark: PlanMark | null; dependsOn: string[] } & PlanNodeFields)[]
  edges: { from: string; to: string; mark: Exclude<PlanMark, 'changed'> | null }[]
}
