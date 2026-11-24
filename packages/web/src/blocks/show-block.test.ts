// @vitest-environment happy-dom
import type { ChartBlock, StepsBlock, TableBlock } from '@ariadne/core'
import { mount } from '@vue/test-utils'
import { describe, expect, test, vi } from 'vitest'
import ShowBlock from './ShowBlock.vue'

describe('ShowBlock', () => {
  test('renders chart blocks lazily through ShowChart', async () => {
    const block: ChartBlock = {
      type: 'chart',
      title: '首屏体积对比',
      kind: 'bar',
      x: 'stage',
      y: ['kb'],
      labels: { kb: '体积 (kB)' },
      rows: [
        { stage: '优化前', kb: 2019 },
        { stage: '优化后', kb: 1360 },
      ],
      stacked: false,
      horizontal: false,
    }

    const wrapper = mount(ShowBlock, { props: { block } })
    await vi.waitFor(() => {
      expect(wrapper.find('.dou-chart').exists()).toBe(true)
    })

    expect(wrapper.text()).toContain('首屏体积对比')
    expect(wrapper.find('.dou-chart').attributes('aria-label')).toBe('首屏体积对比')
    wrapper.unmount()
  })

  test('renders pie charts lazily through ShowChart', async () => {
    const block: ChartBlock = {
      type: 'chart',
      title: '资源分布',
      kind: 'pie',
      x: 'kind',
      y: ['size'],
      labels: { size: '大小' },
      rows: [
        { kind: 'JS', size: 1065 },
        { kind: 'CSS', size: 295 },
      ],
      stacked: false,
      horizontal: false,
    }

    const wrapper = mount(ShowBlock, { props: { block } })
    await vi.waitFor(() => {
      expect(wrapper.find('.dou-chart').exists()).toBe(true)
    })

    expect(wrapper.text()).toContain('资源分布')
    wrapper.unmount()
  })

  test('renders table blocks lazily through ShowTable', async () => {
    const block: TableBlock = {
      type: 'table',
      title: '构建产物',
      columns: [
        { key: 'name', title: '文件', align: 'start' },
        { key: 'size', title: '大小', align: 'end' },
      ],
      rows: [{ name: 'index.js', size: '50 kB' }],
    }

    const wrapper = mount(ShowBlock, { props: { block } })
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain('index.js')
    })

    expect(wrapper.text()).toContain('构建产物')
    expect(wrapper.text()).toContain('50 kB')
    wrapper.unmount()
  })

  test('renders steps blocks synchronously', () => {
    const block: StepsBlock = {
      type: 'steps',
      title: '发布进度',
      items: [
        { title: '构建', description: null, status: 'done' },
        { title: '验收', description: null, status: 'current' },
      ],
    }

    const wrapper = mount(ShowBlock, { props: { block } })
    expect(wrapper.text()).toContain('发布进度')
    expect(wrapper.text()).toContain('构建')
    expect(wrapper.text()).toContain('验收')
    wrapper.unmount()
  })
})
