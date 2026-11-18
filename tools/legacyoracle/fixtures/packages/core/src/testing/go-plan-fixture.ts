import {
  applyOps,
  emptyGraph,
  needsConfirmation,
  parsePlanOps,
  previewPlanOps,
  whyUndoFails,
} from '../plan'
import { DomainError } from '../errors'
import type { PlanOp } from '../events'

const input = (await Bun.stdin.json()) as { initial: PlanOp[]; ops: unknown }[]
const graphJSON = (graph: ReturnType<typeof emptyGraph>) => ({
  nodes: [...graph.nodes],
  order: graph.order,
  edges: [...graph.edges],
})
console.info(
  JSON.stringify(
    input.map((test) => {
      try {
        const graph = applyOps(emptyGraph(), test.initial).graph
        const parsed = parsePlanOps(test.ops)
        const preview = previewPlanOps(graph, parsed)
        const confirmation = needsConfirmation(graph, parsed)
        const failure = whyUndoFails(graph, parsed, new Map([['gone', '原节点']]))
        const result = applyOps(graph, parsed)
        return {
          parsed,
          preview,
          confirmation,
          failure,
          graph: graphJSON(result.graph),
          inverse: result.inverse,
          restored: graphJSON(applyOps(result.graph, result.inverse).graph),
        }
      } catch (error) {
        return {
          error: error instanceof DomainError ? error.toLocalized() : { message: String(error) },
        }
      }
    }),
  ),
)
