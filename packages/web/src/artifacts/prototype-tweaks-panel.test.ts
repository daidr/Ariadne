// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, test } from 'vitest'
import PrototypeTweaksPanel from './PrototypeTweaksPanel.vue'
import type { PrototypeTweakSpec } from './prototype-tweaks'

describe('PrototypeTweaksPanel enum control selection', () => {
  const specs: PrototypeTweakSpec[] = [
    {
      key: 'density',
      label: '信息密度',
      kind: 'enum',
      default: 'standard',
      options: [
        { value: 'compact', label: '紧凑' },
        { value: 'standard', label: '标准' },
        { value: 'loose', label: '宽松' },
      ],
    },
    {
      key: 'entry',
      label: '页面入口位置',
      kind: 'enum',
      default: 'sidebar',
      options: [
        { value: 'sidebar', label: '侧栏顶部（推荐）' },
        { value: 'topbar', label: '顶栏常驻按钮' },
        { value: 'item', label: '单项目右侧 [更多]' },
      ],
    },
    {
      key: 'theme',
      label: '预设方案',
      kind: 'enum',
      default: 'a',
      options: [
        { value: 'a', label: 'A' },
        { value: 'b', label: 'B' },
        { value: 'c', label: 'C' },
        { value: 'd', label: 'D' },
        { value: 'e', label: 'E' },
      ],
    },
    {
      key: 'verbose',
      label: '超长说明',
      kind: 'enum',
      default: 'long1',
      options: [
        { value: 'long1', label: '这是一段超过整个面板单行标签宽度的非常长描述方案' },
        { value: 'long2', label: '常规选项' },
      ],
    },
  ]

  test('renders segmented control for short options, check-tag group for long 2–4 options, and select otherwise', async () => {
    const wrapper = mount(PrototypeTweaksPanel, {
      props: {
        specs,
        values: {
          density: 'standard',
          entry: 'sidebar',
          theme: 'a',
          verbose: 'long1',
        },
      },
    })

    const rows = wrapper.findAll('.tweak-row')
    expect(rows).toHaveLength(4)

    // Short 3-option enum stays a segmented control.
    expect(rows[0]!.find('.dou-segmented-control').exists()).toBe(true)
    expect(rows[0]!.find('.dou-check-tag-group').exists()).toBe(false)

    // Long 3-option enum uses wrapping single-select check tags instead of clipping in segments.
    expect(rows[1]!.find('.dou-check-tag-group').exists()).toBe(true)
    expect(rows[1]!.find('.dou-segmented-control').exists()).toBe(false)
    const tags = rows[1]!.findAll('button.dou-check-tag')
    expect(tags.map((b) => b.text())).toEqual([
      '侧栏顶部（推荐）',
      '顶栏常驻按钮',
      '单项目右侧 [更多]',
    ])
    expect(tags[0]!.attributes('aria-pressed')).toBe('true')
    expect(tags[1]!.attributes('aria-pressed')).toBe('false')

    // Clicking another tag emits both preview and commit immediately.
    await tags[1]!.trigger('click')
    expect(wrapper.emitted('preview')?.at(-1)?.[0]).toMatchObject({ entry: 'topbar' })
    expect(wrapper.emitted('commit')?.at(-1)?.[0]).toMatchObject({ entry: 'topbar' })

    // > 4 options and ultra-long single options fall back to DouSelect.
    expect(rows[2]!.find('.dou-select').exists()).toBe(true)
    expect(rows[3]!.find('.dou-select').exists()).toBe(true)

    wrapper.unmount()
  })

  test('disables check-tag group when readonly', async () => {
    const wrapper = mount(PrototypeTweaksPanel, {
      props: {
        specs: [specs[1]!],
        values: { entry: 'sidebar' },
        readonly: true,
      },
    })

    const tags = wrapper.findAll('button.dou-check-tag')
    expect(tags).toHaveLength(3)
    expect(tags.every((b) => b.attributes('disabled') !== undefined)).toBe(true)
    await tags[1]!.trigger('click')
    expect(wrapper.emitted('commit')).toBeUndefined()

    wrapper.unmount()
  })
})
