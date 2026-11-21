// @vitest-environment happy-dom
import type { FormBlock } from '@ariadne/core'
import { mount } from '@vue/test-utils'
import { describe, expect, test } from 'vitest'
import { t } from '../i18n'
import AskBlock from './AskBlock.vue'

function sampleForm(overrides: Partial<FormBlock> = {}): FormBlock {
  return {
    type: 'form',
    title: '确认聊天输入框草稿被清空的具体触发场景',
    description: '补充上下文帮助定位问题。',
    submitText: '提交反馈',
    fields: [
      {
        key: 'where',
        label: '当时是在哪个输入框里输入',
        kind: 'select',
        required: true,
        placeholder: null,
        options: [
          { label: '左侧协调器输入框', value: 'coordinator' },
          { label: '右侧讨论串输入框', value: 'thread' },
        ],
        default: 'coordinator',
        min: null,
        max: null,
      },
      {
        key: 'scopes',
        label: '涉及的范围',
        kind: 'multiselect',
        required: false,
        placeholder: null,
        options: [
          { label: '草稿缓存', value: 'draft' },
          { label: '追踪视图', value: 'trace' },
        ],
        default: [],
        min: null,
        max: null,
      },
      {
        key: 'clues',
        label: '补充线索（可选，比如当时正在做什么、浏览器、是否开着 dev 模式或刚合并过 PR 等）',
        kind: 'textarea',
        required: false,
        placeholder: null,
        options: [],
        default: '',
        min: null,
        max: null,
      },
      {
        key: 'reproducible',
        label: '是否可稳定复现',
        kind: 'switch',
        required: false,
        placeholder: null,
        options: [],
        default: false,
        min: null,
        max: null,
      },
      {
        key: 'optional',
        label: '额外备注',
        kind: 'text',
        required: false,
        placeholder: null,
        options: [],
        default: '',
        min: null,
        max: null,
      },
      {
        key: 'literalBlank',
        label: '用户输入字面量测试',
        kind: 'text',
        required: false,
        placeholder: null,
        options: [],
        default: '',
        min: null,
        max: null,
      },
    ],
    ...overrides,
  }
}

describe('AskBlock form states', () => {
  test('renders a submitted form as a stacked definition list inside a card', () => {
    const form = sampleForm()
    const wrapper = mount(AskBlock, {
      props: {
        block: form,
        answer: {
          type: 'form',
          values: {
            where: 'coordinator',
            scopes: ['draft', 'trace'],
            clues: '第一行线索\n第二行线索',
            reproducible: true,
            optional: '',
            literalBlank: t('block.blank'),
          },
        },
      },
    })

    const card = wrapper.get('section.ask-form.is-submitted')
    const titleId = card.attributes('aria-labelledby')
    expect(titleId).toBeTruthy()
    expect(wrapper.get(`#${titleId}`).text()).toBe(form.title)
    expect(wrapper.get('.ask-form-status').text()).toContain(t('block.submitted'))
    expect(wrapper.find('.dou-descriptions').exists()).toBe(false)

    const fields = wrapper.findAll('.ask-form-field')
    expect(fields).toHaveLength(form.fields.length)
    expect(fields.map((field) => field.get('dt.ask-form-label').text())).toEqual(
      form.fields.map((field) => field.label),
    )

    const values = fields.map((field) => field.get('dd.ask-form-value'))
    expect(values.map((value) => value.text())).toEqual([
      '左侧协调器输入框',
      '草稿缓存、追踪视图',
      '第一行线索\n第二行线索',
      t('block.yes'),
      t('block.blank'),
      t('block.blank'),
    ])
    // Empty values are muted, while a literal text answer matching the placeholder is kept normal.
    expect(values[4]!.classes()).toContain('is-blank')
    expect(values[5]!.classes()).not.toContain('is-blank')
  })

  test('renders a waiting form inside a card with header, inputs and actions', () => {
    const form = sampleForm()
    const wrapper = mount(AskBlock, {
      props: {
        attentionId: 'att_1',
        block: form,
        answer: null,
      },
    })

    expect(wrapper.find('section.ask-form.is-waiting').exists()).toBe(true)
    expect(wrapper.get('.ask-form-title').text()).toBe(form.title)
    expect(wrapper.get('.ask-form-description').text()).toBe(form.description)
    expect(wrapper.findAll('.ask-form-inputs .dou-form-item')).toHaveLength(form.fields.length)
    expect(wrapper.get('.ask-form-actions button[type="submit"]').text()).toContain('提交反馈')
  })

  test('renders a withdrawn form in its muted card shell', () => {
    const form = sampleForm()
    const wrapper = mount(AskBlock, {
      props: {
        block: form,
        answer: null,
        withdrawn: '已在对话中直接确认',
      },
    })

    expect(wrapper.find('section.ask-form.is-withdrawn').exists()).toBe(true)
    expect(wrapper.get('.ask-form-title').text()).toBe(form.title)
    expect(wrapper.get('.ask-form-description').text()).toBe(
      t('block.withdrawn', { reason: '已在对话中直接确认' }),
    )
    expect(wrapper.find('.ask-form-values').exists()).toBe(false)
    expect(wrapper.find('.ask-form-body').exists()).toBe(false)
  })
})
