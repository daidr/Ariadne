// @vitest-environment happy-dom
import type { FlowDocument, FlowNode } from '@dou-ui/flow'
import { mount } from '@vue/test-utils'
import { describe, expect, test, vi } from 'vitest'
import { ref } from 'vue'
import type { OptionNodeData } from './option-node'
import type { PrototypeNodeData } from './prototype-tweaks'

const mockDoc = ref<FlowDocument>({ version: 1, nodes: [], edges: [] })
const mockTransact = vi.fn()

vi.mock('@dou-ui/flow', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('@dou-ui/flow')
  return {
    ...actual,
    useFlowEditor: () => ({
      doc: mockDoc,
      transact: mockTransact,
    }),
  }
})

import OptionNode from './OptionNode.vue'
import PrototypeNode from './PrototypeNode.vue'

function mountPrototype(width: number, height: number, data: PrototypeNodeData) {
  const node: FlowNode<PrototypeNodeData> = {
    id: 'proto-1',
    type: 'prototype',
    x: 0,
    y: 0,
    width,
    height,
    data,
  }
  return mount(PrototypeNode, {
    props: {
      node,
      data,
      ports: [],
      selected: false,
      readonly: false,
      updateData: vi.fn(),
    },
  })
}

function mountOption(option: FlowNode<OptionNodeData>, children: FlowNode[] = []) {
  mockDoc.value = { version: 1, nodes: [option, ...children], edges: [] }
  return mount(OptionNode, {
    props: {
      node: option,
      data: option.data!,
      ports: [],
      selected: false,
      readonly: false,
      updateData: vi.fn(),
    },
  })
}

describe('PrototypeNode header responsiveness', () => {
  const sampleHtml = `<!doctype html><html><head>
    <script type="application/json" data-ariadne-tweaks>
      [{"key":"accent","label":"主色","kind":"color","default":"#2563eb"}]
    </script>
  </head><body><div>Demo</div></body></html>`

  test('hides inline dimensions on narrow portrait cards and keeps full dimensions in tooltip', () => {
    const wrapper = mountPrototype(280, 640, {
      title: '移动端首页交互原型长标题验证',
      html: sampleHtml,
      viewportWidth: 390,
      viewportHeight: 844,
    })

    expect(wrapper.find('.proto-size').exists()).toBe(false)
    expect(wrapper.get('.proto-title-text').attributes('title')).toBe(
      '移动端首页交互原型长标题验证 (390×844)',
    )
    // At 280px, action buttons keep labels (not yet icon-only) and never collide with hidden size.
    const buttons = wrapper.findAll('.proto-action-btn')
    expect(buttons).toHaveLength(2)
    expect(buttons[0]?.text()).toBeTruthy()
    expect(buttons[1]?.text()).toBeTruthy()
    wrapper.unmount()
  })

  test('collapses action button labels to icon-only on very narrow resized cards', () => {
    const wrapper = mountPrototype(240, 520, {
      title: '超窄卡片',
      html: sampleHtml,
      viewportWidth: 390,
      viewportHeight: 844,
    })

    expect(wrapper.find('.proto-size').exists()).toBe(false)
    const buttons = wrapper.findAll('.proto-action-btn')
    expect(buttons).toHaveLength(2)
    expect(buttons[0]?.text()).toBe('')
    expect(buttons[1]?.text()).toBe('')
    wrapper.unmount()
  })

  test('renders inline dimensions alongside long titles on wide prototypes', async () => {
    const wrapper = mountPrototype(480, 336, {
      title: '桌面端工作台完整交互原型设计稿对比（带参数调节与长标题）',
      html: sampleHtml,
      viewportWidth: 1280,
      viewportHeight: 820,
    })

    expect(wrapper.get('.proto-size').text()).toBe('1280×820')
    expect(wrapper.get('.proto-title-text').classes()).toContain('is-truncate')

    // Entering interact mode shows mode tag and close action without breaking header structure.
    await wrapper.findAll('.proto-action-btn')[1]!.trigger('click')
    expect(wrapper.find('.proto-mode-tag').exists()).toBe(true)
    wrapper.unmount()
  })
})

describe('OptionNode layered header and note layout', () => {
  test('renders note in a separate wrapped block below the header when inset space is reserved', () => {
    const option: FlowNode<OptionNodeData> = {
      id: 'opt-a',
      type: 'option',
      x: 0,
      y: 0,
      width: 360,
      height: 320,
      data: {
        title: '方案 A：三栏工作台布局',
        note: '这是一段很长的方案说明，需要在标题行下方换行展示并限制在卡片宽度内，不会压到推荐徽章或选这个按钮',
        recommended: true,
      },
    }
    const child: FlowNode = {
      id: 'opt-a-text',
      type: 'text',
      parent: 'opt-a',
      x: 16,
      y: 88,
      width: 328,
      height: 120,
      data: { text: '正文内容' },
    }

    const wrapper = mountOption(option, [child])

    expect(wrapper.find('.option-head.has-block-note').exists()).toBe(true)
    expect(wrapper.find('.option-note').exists()).toBe(true)
    // Title row only holds title and recommended tag, never the note text.
    expect(wrapper.get('.option-title').text()).toContain('方案 A：三栏工作台布局')
    expect(wrapper.get('.option-title').text()).not.toContain('这是一段很长的方案说明')
    expect(wrapper.get('.option-note-text').text()).toContain('这是一段很长的方案说明')
    expect(wrapper.get('.option-note-text').classes()).toContain('is-clamp')
    expect(wrapper.find('.option-pick').exists()).toBe(true)
    wrapper.unmount()
  })

  test('falls back to compact stacked header on legacy boards with 40px child inset', () => {
    const option: FlowNode<OptionNodeData> = {
      id: 'opt-legacy',
      type: 'option',
      x: 0,
      y: 0,
      width: 360,
      height: 240,
      data: {
        title: '旧版卡片标题',
        note: '旧版画板里的长说明应当在紧凑模式下单行截断且不遮挡下方子节点',
        recommended: true,
      },
    }
    const child: FlowNode = {
      id: 'opt-legacy-text',
      type: 'text',
      parent: 'opt-legacy',
      x: 16,
      y: 40,
      width: 328,
      height: 100,
      data: { text: '正文' },
    }

    const wrapper = mountOption(option, [child])

    expect(wrapper.find('.option-head.is-compact-note').exists()).toBe(true)
    expect(wrapper.find('.option-note').exists()).toBe(false)
    expect(wrapper.get('.option-note-compact').classes()).toContain('is-truncate')
    expect(wrapper.get('.option-note-compact').text()).toContain('旧版画板里的长说明')
    wrapper.unmount()
  })

  test('omits note row when option has no note', () => {
    const option: FlowNode<OptionNodeData> = {
      id: 'opt-plain',
      type: 'option',
      x: 0,
      y: 0,
      width: 360,
      height: 200,
      data: { title: '纯文字选项' },
    }

    const wrapper = mountOption(option)
    expect(wrapper.find('.option-note').exists()).toBe(false)
    expect(wrapper.find('.option-head.has-block-note').exists()).toBe(false)
    expect(wrapper.find('.option-head.is-compact-note').exists()).toBe(false)
    wrapper.unmount()
  })
})
