// @vitest-environment happy-dom
import { WHITEBOARD_NODE_TYPES } from '@ariadne/core'
import { DouEditor, editorKit } from '@dou-ui/editor'
import { whiteboardBlock } from '@dou-ui/editor/flow'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import { DouFlowEditor, type FlowDocument, type FlowSelection } from '@dou-ui/flow'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, expect, test } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { whiteboardExtensions } from './whiteboard-extensions'

const descriptors = new Map<string, PropertyDescriptor | undefined>()

function stubBoxSizes() {
  for (const prop of ['offsetWidth', 'offsetHeight'] as const) {
    descriptors.set(prop, Object.getOwnPropertyDescriptor(HTMLElement.prototype, prop))
    const cssProp = prop === 'offsetWidth' ? 'width' : 'height'
    const fallback = prop === 'offsetWidth' ? 200 : 60
    Object.defineProperty(HTMLElement.prototype, prop, {
      configurable: true,
      get(this: HTMLElement) {
        return Number.parseFloat(this.style[cssProp]) || fallback
      },
    })
  }
}

function restoreBoxSizes() {
  for (const [prop, desc] of descriptors) {
    if (desc) Object.defineProperty(HTMLElement.prototype, prop, desc)
    else delete (HTMLElement.prototype as unknown as Record<string, unknown>)[prop]
  }
  descriptors.clear()
}

afterEach(() => {
  restoreBoxSizes()
  document.body.innerHTML = ''
})

test('every node the whiteboard editor can make passes the content check', () => {
  const extensions = whiteboardExtensions().flat()
  const types = extensions
    .flatMap((extension) => extension.nodeTypes ?? [])
    .map((spec) => spec.type)
  expect(types).toContain('option')
  expect(types).toContain('group')
  expect(types).toContain('prototype')
  for (const type of types) expect(WHITEBOARD_NODE_TYPES).toContain(type)

  const whiteboard = extensions.find((extension) => extension.name === 'whiteboard')
  expect(whiteboard?.toolbarItems).toBeTypeOf('function')
  expect(whiteboard?.edgeToolbarItems).toBeTypeOf('function')
})

test('whiteboardExtensions renders shape/connector color styles and borderless state without affecting option nodes', async () => {
  stubBoxSizes()
  const extensions = whiteboardExtensions()
  const doc = ref<FlowDocument>({
    version: 1,
    nodes: [
      {
        id: 'card-a',
        type: 'shape',
        x: 0,
        y: 0,
        width: 160,
        height: 80,
        data: {
          shape: 'rect',
          text: '无边框卡片',
          fill: 'blue',
          stroke: 'none',
          textColor: '#0369a1',
        },
      },
      {
        id: 'note-b',
        type: 'text',
        x: 240,
        y: 20,
        width: 120,
        height: 40,
        data: {
          text: '自定义文字',
          color: '#7c3aed',
        },
      },
      {
        id: 'sticky-c',
        type: 'sticky',
        x: 240,
        y: 90,
        width: 140,
        height: 90,
        data: {
          text: '透明便签',
          fill: 'none',
          textColor: 'green',
        },
      },
      {
        id: 'ink-d',
        type: 'ink',
        x: 420,
        y: 20,
        width: 80,
        height: 40,
        data: {
          points: [0, 0, 80, 40],
          box: [80, 40],
          color: '#ea580c',
        },
      },
      {
        id: 'opt-1',
        type: 'option',
        x: 0,
        y: 160,
        width: 240,
        height: 120,
        data: {
          title: '方案 A',
        },
      },
    ],
    edges: [
      {
        id: 'edge-ab',
        type: 'connector',
        source: { node: 'card-a', port: 'right' },
        target: { node: 'note-b', port: 'left-in' },
        data: { route: 'straight', arrow: 'end', color: '#dc2626' },
      },
    ],
  })
  const selection = ref<FlowSelection>({ nodes: ['card-a'], edges: [] })

  const Host = defineComponent({
    setup() {
      return () =>
        h(DouFlowEditor, {
          modelValue: doc.value,
          'onUpdate:modelValue': (next: FlowDocument) => {
            doc.value = next
          },
          selection: selection.value,
          'onUpdate:selection': (next: FlowSelection) => {
            selection.value = next
          },
          extensions,
        })
    },
  })

  const wrapper = mount(Host, { attachTo: document.body })

  try {
    await nextTick()
    await nextTick()

    const shapeNode = wrapper.get('.dou-whiteboard-node.is-shape')
    expect(shapeNode.classes()).toContain('is-borderless')
    expect(shapeNode.attributes('style')).toContain(
      '--dou-whiteboard-shape-fill: var(--dou-whiteboard-fill-blue)',
    )
    expect(shapeNode.attributes('style')).toContain('--dou-whiteboard-text-color: #0369a1')

    const textNode = wrapper.get('.dou-whiteboard-node.is-text')
    expect(textNode.attributes('style')).toContain('--dou-whiteboard-text-color: #7c3aed')

    const stickyNode = wrapper.get('.dou-whiteboard-node.is-sticky')
    expect(stickyNode.classes()).toContain('is-transparent')
    expect(stickyNode.attributes('style')).toContain('--dou-whiteboard-sticky-fill: transparent')
    expect(stickyNode.attributes('style')).toContain(
      '--dou-whiteboard-text-color: var(--dou-whiteboard-color-green)',
    )

    const inkNode = wrapper.get('.dou-whiteboard-node.is-ink')
    expect(inkNode.attributes('style')).toContain('--dou-whiteboard-ink-color: #ea580c')

    const connectorPath = wrapper.get('.dou-flow-editor__edge:not(.is-mark)')
    expect(connectorPath.attributes('style')).toContain('--dou-flow-connector-color: #dc2626')

    // Shape selection exposes color toolbar buttons (fill, stroke, text color).
    expect(wrapper.findAll('.dou-flow-toolbar .dou-flow-toolbar__color-bar')).toHaveLength(3)

    // Sticky selection exposes fill and text color controls; ink exposes line color control.
    selection.value = { nodes: ['sticky-c'], edges: [] }
    await nextTick()
    await nextTick()
    expect(wrapper.findAll('.dou-flow-toolbar .dou-flow-toolbar__color-bar')).toHaveLength(2)

    selection.value = { nodes: ['ink-d'], edges: [] }
    await nextTick()
    await nextTick()
    expect(wrapper.findAll('.dou-flow-toolbar .dou-flow-toolbar__color-bar')).toHaveLength(1)

    // Selecting a single connector exposes its color toolbar button and draggable endpoint handles.
    selection.value = { nodes: [], edges: ['edge-ab'] }
    await nextTick()
    await nextTick()
    expect(wrapper.findAll('.dou-flow-toolbar .dou-flow-toolbar__color-bar')).toHaveLength(1)
    expect(wrapper.findAll('.dou-flow-editor__edge-end')).toHaveLength(2)

    // Selecting an Ariadne option node does not show whiteboard shape color controls.
    selection.value = { nodes: ['opt-1'], edges: [] }
    await nextTick()
    await nextTick()
    expect(wrapper.findAll('.dou-flow-toolbar .dou-flow-toolbar__color-bar')).toHaveLength(0)
  } finally {
    wrapper.unmount()
  }
})

test('embedded document whiteboard block preserves and renders shape, text, and connector colors and borderless state', async () => {
  stubBoxSizes()
  const embedded = {
    height: 360,
    board: {
      version: 1,
      nodes: [
        {
          id: 'shape-1',
          type: 'shape',
          x: 40,
          y: 60,
          width: 160,
          height: 84,
          data: {
            shape: 'rect',
            text: '无边框卡片',
            fill: 'green',
            stroke: 'none',
            textColor: '#166534',
          },
        },
        {
          id: 'text-1',
          type: 'text',
          x: 260,
          y: 80,
          width: 140,
          height: 40,
          data: { text: '彩色说明', color: '#7c3aed' },
        },
      ],
      edges: [
        {
          id: 'edge-1',
          type: 'connector',
          source: { node: 'shape-1', port: 'right' },
          target: { node: 'text-1', port: 'left-in' },
          data: { route: 'curve', arrow: 'end', color: '#dc2626' },
        },
      ],
    },
  }
  const source = ['# 架构图', `\`\`\`dou-whiteboard\n${JSON.stringify(embedded)}\n\`\`\``].join(
    '\n\n',
  )
  const docExtensions = [editorKit(), whiteboardBlock({ commentable: true })]

  const blocks = markdownToBlocks(source, docExtensions)
  const markdown = blocksToMarkdown(blocks as never, docExtensions)
  const match = markdown.match(/```dou-whiteboard\r?\n([\s\S]*?)\r?\n```/)
  expect(match).not.toBeNull()
  const roundTripped = JSON.parse(match![1]!) as typeof embedded
  expect(roundTripped.board.nodes[0]?.data).toMatchObject({
    shape: 'rect',
    fill: 'green',
    stroke: 'none',
    textColor: '#166534',
  })
  expect(roundTripped.board.nodes[1]?.data).toMatchObject({
    text: '彩色说明',
    color: '#7c3aed',
  })
  expect(roundTripped.board.edges[0]?.data).toMatchObject({
    route: 'curve',
    arrow: 'end',
    color: '#dc2626',
  })

  const wrapper = mount(DouEditor, {
    props: {
      modelValue: { version: 1, blocks } as never,
      extensions: docExtensions,
    },
    attachTo: document.body,
  })
  try {
    await flushPromises()
    await nextTick()

    await wrapper.get('.dou-editor-board__open').trigger('click')
    await flushPromises()
    await nextTick()
    await nextTick()

    const shapeNode = document.body.querySelector('.dou-whiteboard-node.is-shape')
    expect(shapeNode).not.toBeNull()
    expect(shapeNode?.classList.contains('is-borderless')).toBe(true)
    expect(shapeNode?.getAttribute('style')).toContain(
      '--dou-whiteboard-shape-fill: var(--dou-whiteboard-fill-green)',
    )
    expect(shapeNode?.getAttribute('style')).toContain('--dou-whiteboard-text-color: #166534')

    const textNode = document.body.querySelector('.dou-whiteboard-node.is-text')
    expect(textNode?.getAttribute('style')).toContain('--dou-whiteboard-text-color: #7c3aed')

    const connectorPath = document.body.querySelector('.dou-flow-editor__edge:not(.is-mark)')
    expect(connectorPath?.getAttribute('style')).toContain('--dou-flow-connector-color: #dc2626')
  } finally {
    wrapper.unmount()
  }
})
