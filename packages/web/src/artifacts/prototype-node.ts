import { defineNodeType, drawCard } from '@dou-ui/flow'
import PrototypeNode from './PrototypeNode.vue'
import {
  PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT,
  PROTOTYPE_DEFAULT_VIEWPORT_WIDTH,
  PROTOTYPE_HEADER_HEIGHT,
  type PrototypeNodeData,
} from './prototype-tweaks'

export const prototypeNode = defineNodeType<PrototypeNodeData>({
  type: 'prototype',
  component: PrototypeNode,
  resizable: { minWidth: 240, minHeight: 180 },
  size: { width: 480, height: 336 },
  defaults: () => ({
    title: '',
    viewportWidth: PROTOTYPE_DEFAULT_VIEWPORT_WIDTH,
    viewportHeight: PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT,
  }),
  label: (node) => node.data?.title || 'Prototype',
  draw(node, draw) {
    const titleStyle = draw.style('dou-flow-group-node__title')
    const title = node.data?.title || 'Prototype'
    const vw = node.data?.viewportWidth ?? PROTOTYPE_DEFAULT_VIEWPORT_WIDTH
    const vh = node.data?.viewportHeight ?? PROTOTYPE_DEFAULT_VIEWPORT_HEIGHT
    const headH = Math.min(PROTOTYPE_HEADER_HEIGHT, draw.height)
    const bodyY = headH
    const bodyH = Math.max(0, draw.height - headH)
    const borderColor = draw.color('var(--dou-color-border)')
    const mutedColor = draw.color('var(--dou-color-text-tertiary)')
    const bgSubtle = draw.color('var(--dou-color-bg-subtle)')

    return (
      drawCard(draw, []) +
      `<line x1="0" y1="${headH}" x2="${draw.width}" y2="${headH}" stroke="${borderColor}" stroke-width="1"/>` +
      draw.text(
        title,
        { x: 10, y: 0, width: Math.max(0, draw.width - 90), height: headH },
        {
          size: Number.parseFloat(titleStyle.fontSize) || 12,
          weight: 600,
          color: draw.color(titleStyle.color),
          valign: 'middle',
          nowrap: true,
          lineHeight: headH,
        },
      ) +
      draw.text(
        `${vw}×${vh}`,
        { x: Math.max(10, draw.width - 80), y: 0, width: 70, height: headH },
        {
          size: 11,
          weight: 400,
          color: mutedColor,
          align: 'end',
          valign: 'middle',
          nowrap: true,
          lineHeight: headH,
        },
      ) +
      `<rect x="1" y="${bodyY + 1}" width="${Math.max(0, draw.width - 2)}" height="${Math.max(0, bodyH - 2)}" fill="${bgSubtle}"/>` +
      draw.text(
        'HTML Prototype',
        { x: 12, y: bodyY, width: Math.max(0, draw.width - 24), height: bodyH },
        {
          size: 12,
          weight: 500,
          color: mutedColor,
          align: 'center',
          valign: 'middle',
          nowrap: true,
        },
      )
    )
  },
})
