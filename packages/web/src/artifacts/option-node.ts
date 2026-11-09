import { defineNodeType, drawCard } from '@dou-ui/flow'
import OptionNode from './OptionNode.vue'

/**
 * One option of several the agent lays out for the user to compare and pick, with what
 * shows it (screenshots, notes) inside. `pick: 'many'` lets more than one be picked;
 * by default picking one unpicks the others.
 */
export interface OptionNodeData {
  [key: string]: unknown
  title: string
  note?: string
  chosen?: boolean
  pick?: 'one' | 'many'
}

export const optionNode = defineNodeType<OptionNodeData>({
  type: 'option',
  component: OptionNode,
  container: true,
  resizable: { minWidth: 200, minHeight: 120 },
  size: { width: 360, height: 280 },
  defaults: () => ({ title: '' }),
  label: (node) => node.data?.title ?? '',
  draw(node, draw) {
    const title = draw.style('dou-flow-group-node__title')
    const chosen = node.data?.chosen === true
    return (
      drawCard(draw, ['is-container'].concat(chosen ? ['is-chosen'] : [])) +
      draw.text(
        `${chosen ? '✓ ' : ''}${node.data?.title ?? ''}`,
        { x: 12, y: 0, width: Math.max(0, draw.width - 24), height: 40 },
        {
          size: Number.parseFloat(title.fontSize) || 13,
          weight: 600,
          color: draw.color(title.color),
          valign: 'middle',
          nowrap: true,
          lineHeight: 40,
        },
      )
    )
  },
})
