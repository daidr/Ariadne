import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import type { OptionNodeData } from './option-node'

export interface DecisionGroupData {
  [key: string]: unknown
  label?: string
  collapsed?: boolean
  note?: string
  pick?: 'one' | 'many'
  round?: number
  mode?: 'choose' | 'verify'
  confirmed?: boolean
}

export interface DecisionGroupSummary {
  /** Number of active (unconfirmed) decision groups on the board. */
  totalGroups: number
  /** Active groups where the user manually picked at least one option. */
  pickedGroups: number
  /** Active unpicked groups that have at least one recommended option. */
  recommendedFallbackGroups: number
  /** Active unpicked groups that have no recommended option. */
  remainingGroups: number
  /** True when the board has at least one active decision group. */
  hasActiveDecisionGroups: boolean
}

export function optionGroupId(node: FlowNode, byId: ReadonlyMap<string, FlowNode>): string {
  const data = node.data as OptionNodeData | undefined
  if (typeof data?.group === 'string' && data.group) return data.group
  if (node.parent && byId.get(node.parent)?.type === 'group') return node.parent
  return ''
}

export function optionPickMode(
  node: FlowNode,
  byId: ReadonlyMap<string, FlowNode>,
): 'one' | 'many' {
  const data = node.data as OptionNodeData | undefined
  if (data?.pick === 'many' || data?.pick === 'one') return data.pick
  const groupId = optionGroupId(node, byId)
  if (groupId) {
    const groupData = byId.get(groupId)?.data as DecisionGroupData | undefined
    if (groupData?.pick === 'many' || groupData?.pick === 'one') return groupData.pick
  }
  return 'one'
}

export function isDecisionGroupNode(
  node: FlowNode,
  optionsByGroup?: ReadonlyMap<string, FlowNode[]>,
): boolean {
  if (node.type !== 'group') return false
  const data = node.data as DecisionGroupData | undefined
  if (
    data?.mode === 'choose' ||
    data?.mode === 'verify' ||
    (typeof data?.round === 'number' && data.round > 0) ||
    data?.pick === 'one' ||
    data?.pick === 'many' ||
    data?.confirmed === true
  ) {
    return true
  }
  return (optionsByGroup?.get(node.id)?.length ?? 0) > 0
}

export function groupOptionsMap(doc: FlowDocument): {
  byId: Map<string, FlowNode>
  optionsByGroup: Map<string, FlowNode[]>
} {
  const byId = new Map(doc.nodes.map((node) => [node.id, node]))
  const optionsByGroup = new Map<string, FlowNode[]>()
  for (const node of doc.nodes) {
    if (node.type !== 'option') continue
    const gid = optionGroupId(node, byId)
    if (!gid) continue
    const list = optionsByGroup.get(gid)
    if (list) list.push(node)
    else optionsByGroup.set(gid, [node])
  }
  return { byId, optionsByGroup }
}

export function summarizeDecisionGroups(doc: FlowDocument): DecisionGroupSummary {
  const { optionsByGroup } = groupOptionsMap(doc)
  let totalGroups = 0
  let pickedGroups = 0
  let recommendedFallbackGroups = 0
  let remainingGroups = 0
  for (const node of doc.nodes) {
    if (!isDecisionGroupNode(node, optionsByGroup)) continue
    const data = node.data as DecisionGroupData | undefined
    if (data?.confirmed) continue
    totalGroups++
    const options = optionsByGroup.get(node.id) ?? []
    const hasChosen = options.some((opt) => (opt.data as OptionNodeData | undefined)?.chosen)
    if (hasChosen) {
      pickedGroups++
      continue
    }
    const hasRecommended = options.some(
      (opt) => (opt.data as OptionNodeData | undefined)?.recommended,
    )
    if (hasRecommended) recommendedFallbackGroups++
    else remainingGroups++
  }
  return {
    totalGroups,
    pickedGroups,
    recommendedFallbackGroups,
    remainingGroups,
    hasActiveDecisionGroups: totalGroups > 0,
  }
}

/**
 * Returns option node IDs that should be auto-selected from `recommended: true` when the
 * user submits an active decision round without manually clicking those groups.
 */
export function collectAutoRecommendedOptionIds(doc: FlowDocument): string[] {
  const { byId, optionsByGroup } = groupOptionsMap(doc)
  const ids: string[] = []
  for (const node of doc.nodes) {
    if (!isDecisionGroupNode(node, optionsByGroup)) continue
    const data = node.data as DecisionGroupData | undefined
    if (data?.confirmed) continue
    const options = optionsByGroup.get(node.id) ?? []
    if (options.some((opt) => (opt.data as OptionNodeData | undefined)?.chosen)) continue
    const recommended = options.filter(
      (opt) => (opt.data as OptionNodeData | undefined)?.recommended,
    )
    if (!recommended.length) continue
    const pick = optionPickMode(recommended[0]!, byId)
    if (pick === 'many') {
      for (const opt of recommended) ids.push(opt.id)
    } else {
      ids.push(recommended[0]!.id)
    }
  }
  return ids
}

export interface ViewportRect {
  x: number
  y: number
  zoom: number
}

/**
 * Checks whether the bounding box of `nodes` lies outside the currently visible canvas
 * rectangle (or has less than 25% area visible), so we can offer a non-intrusive locate pill.
 */
export function areNodesOutsideViewport(
  nodes: readonly FlowNode[],
  viewport: ViewportRect,
  canvasSize: { width: number; height: number },
): boolean {
  if (!nodes.length || canvasSize.width <= 0 || canvasSize.height <= 0 || viewport.zoom <= 0) {
    return false
  }
  let minX = Number.POSITIVE_INFINITY
  let minY = Number.POSITIVE_INFINITY
  let maxX = Number.NEGATIVE_INFINITY
  let maxY = Number.NEGATIVE_INFINITY
  for (const node of nodes) {
    const w = node.width ?? 160
    const h = node.height ?? 90
    minX = Math.min(minX, node.x)
    minY = Math.min(minY, node.y)
    maxX = Math.max(maxX, node.x + w)
    maxY = Math.max(maxY, node.y + h)
  }
  const viewLeft = -viewport.x / viewport.zoom
  const viewTop = -viewport.y / viewport.zoom
  const viewRight = viewLeft + canvasSize.width / viewport.zoom
  const viewBottom = viewTop + canvasSize.height / viewport.zoom

  const overlapW = Math.max(0, Math.min(maxX, viewRight) - Math.max(minX, viewLeft))
  const overlapH = Math.max(0, Math.min(maxY, viewBottom) - Math.max(minY, viewTop))
  const totalArea = Math.max(1, (maxX - minX) * (maxY - minY))
  return (overlapW * overlapH) / totalArea < 0.25
}
