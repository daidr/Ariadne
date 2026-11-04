import { describe, expect, test } from 'vitest'
import { describeAnswer, parseAnswer, parseBlock, type FormBlock } from './blocks'
import { DomainError } from './errors'

/** The reason `fn` gives for refusing a block, or `null`. */
function reason(fn: () => unknown): string | null {
  try {
    fn()
  } catch (error) {
    if (error instanceof DomainError && error.code === 'error.blockInvalid') {
      return String(error.params?.reason)
    }
    throw error
  }
  return null
}

describe('parseBlock', () => {
  test('fills in what a block left out', () => {
    expect(
      parseBlock({
        type: 'choice',
        question: ' 用哪个？ ',
        options: [
          { label: 'A', consequence: '快' },
          { label: 'B', consequence: '稳' },
        ],
      }),
    ).toEqual({
      type: 'choice',
      question: '用哪个？',
      options: [
        { label: 'A', consequence: '快' },
        { label: 'B', consequence: '稳' },
      ],
      recommended: null,
    })
    expect(parseBlock({ type: 'confirm', title: '删除吗？' })).toEqual({
      type: 'confirm',
      title: '删除吗？',
      description: null,
      detail: null,
      tone: 'neutral',
      approveText: null,
      denyText: null,
    })
    expect(
      parseBlock({ type: 'steps', items: [{ title: '构建', status: 'done' }, { title: '发布' }] }),
    ).toEqual({
      type: 'steps',
      title: null,
      items: [
        { title: '构建', description: null, status: 'done' },
        { title: '发布', description: null, status: 'pending' },
      ],
    })
  })

  test('tells the model what is wrong, field by field', () => {
    expect(reason(() => parseBlock({ type: 'html', html: '<b>' }))).toBe(
      'block.type must be one of choice, confirm, form, table, chart, steps',
    )
    expect(
      reason(() =>
        parseBlock({ type: 'choice', question: 'q', options: [{ label: 'A', consequence: 'a' }] }),
      ),
    ).toBe('block.options needs at least 2 items')
    expect(
      reason(() =>
        parseBlock({
          type: 'choice',
          question: 'q',
          options: [
            { label: 'A', consequence: 'a' },
            { label: 'B', consequence: 'b' },
          ],
          recommended: 2,
        }),
      ),
    ).toBe('block.recommended must be the index of an option')
    expect(reason(() => parseBlock({ type: 'confirm', title: 'x', onClick: 'alert(1)' }))).toBe(
      'block has unknown fields: onClick',
    )
  })

  test('a table keeps its cells by column and refuses others', () => {
    const table = parseBlock({
      type: 'table',
      columns: [
        { key: 'name', title: '名称' },
        { key: 'size', title: '大小', align: 'end' },
      ],
      rows: [{ name: 'a.ts', size: 12 }, { name: 'b.ts' }],
    })
    expect(table).toMatchObject({
      rows: [
        { name: 'a.ts', size: 12 },
        { name: 'b.ts', size: null },
      ],
    })
    expect(
      reason(() =>
        parseBlock({
          type: 'table',
          columns: [{ key: 'name', title: '名称' }],
          rows: [{ name: 'a', owner: 'x' }],
        }),
      ),
    ).toBe('block.rows[0] has unknown fields: owner')
    expect(
      reason(() =>
        parseBlock({
          type: 'table',
          columns: [{ key: 'name', title: '名称' }],
          rows: [{ name: { nested: true } }],
        }),
      ),
    ).toBe('block.rows[0].name must be text, a number or a boolean')
  })

  test('a chart needs numbers for its series, and a pie has one', () => {
    const rows = [
      { day: '2026-11-03', open: 4, closed: 2 },
      { day: '2026-11-04', open: 3, closed: null },
    ]
    expect(
      parseBlock({ type: 'chart', kind: 'line', x: 'day', y: ['open', 'closed'], rows }),
    ).toMatchObject({ labels: {}, stacked: false, horizontal: false })
    expect(
      reason(() =>
        parseBlock({ type: 'chart', kind: 'pie', x: 'day', y: ['open', 'closed'], rows }),
      ),
    ).toBe('block.y has more than 1 items')
    expect(
      reason(() =>
        parseBlock({
          type: 'chart',
          kind: 'bar',
          x: 'day',
          y: ['open'],
          rows: [{ day: 'a', open: '3' }],
        }),
      ),
    ).toBe('block.rows[0].open must be a number or null')
    expect(
      reason(() =>
        parseBlock({
          type: 'chart',
          kind: 'bar',
          x: 'day',
          y: ['open'],
          labels: { closed: '已关' },
          rows,
        }),
      ),
    ).toBe('block.labels.closed is not one of y')
  })

  test('form fields check their kind, options and defaults', () => {
    expect(
      reason(() =>
        parseBlock({
          type: 'form',
          title: 't',
          fields: [{ key: 'env', label: '环境', kind: 'select' }],
        }),
      ),
    ).toBe('block.fields[0].options must be an array')
    expect(
      reason(() =>
        parseBlock({
          type: 'form',
          title: 't',
          fields: [{ key: 'env', label: '环境', kind: 'select', options: ['a'], default: 'b' }],
        }),
      ),
    ).toBe("block.fields[0].default must be one of the field's option values")
    expect(
      reason(() =>
        parseBlock({
          type: 'form',
          title: 't',
          fields: [
            { key: 'a', label: 'A', kind: 'text' },
            { key: 'a', label: 'B', kind: 'text' },
          ],
        }),
      ),
    ).toBe('block.fields uses the key "a" twice')
  })

  test('a checked block checks again the same, as stored blocks are', () => {
    const blocks = [
      {
        type: 'choice',
        question: 'q',
        options: [
          { label: 'A', consequence: 'a' },
          { label: 'B', consequence: 'b' },
        ],
      },
      { type: 'confirm', title: 't', detail: 'rm -rf build' },
      {
        type: 'form',
        title: 't',
        fields: [
          { key: 'a', label: 'A', kind: 'text' },
          { key: 'n', label: 'N', kind: 'number', min: 0 },
          { key: 's', label: 'S', kind: 'select', options: ['x'] },
          { key: 'm', label: 'M', kind: 'multiselect', options: ['x'], default: ['x'] },
          { key: 'w', label: 'W', kind: 'switch' },
        ],
      },
      { type: 'table', columns: [{ key: 'a', title: 'A' }], rows: [{ a: 1 }] },
      { type: 'chart', kind: 'pie', x: 'k', y: ['v'], rows: [{ k: 'a', v: 1 }] },
      { type: 'steps', items: [{ title: 'a' }] },
    ]
    for (const block of blocks) {
      const once = parseBlock(block)
      expect(parseBlock(once)).toEqual(once)
    }
  })
})

describe('parseAnswer', () => {
  const form = parseBlock({
    type: 'form',
    title: '发布',
    fields: [
      { key: 'version', label: '版本', kind: 'text', required: true },
      { key: 'count', label: '数量', kind: 'number', min: 1, max: 5, default: 2 },
      { key: 'env', label: '环境', kind: 'select', options: ['staging', 'prod'] },
      { key: 'tags', label: '标签', kind: 'multiselect', options: ['a', 'b'] },
      { key: 'notify', label: '通知', kind: 'switch' },
    ],
  }) as FormBlock

  const values = (v: Record<string, unknown>) => () =>
    parseAnswer(form, { type: 'form', values: { version: '1', ...v } })

  test('a form answer holds every field, empty ones as their empty value', () => {
    expect(form.fields.map((field) => field.default)).toEqual([null, 2, null, [], false])
    const answer = parseAnswer(form, { type: 'form', values: { version: '1.0', count: 3 } })
    expect(answer).toEqual({
      type: 'form',
      values: { version: '1.0', count: 3, env: null, tags: [], notify: false },
    })
    expect(describeAnswer(form, answer)).toBe(
      'submitted:\nversion (版本): "1.0"\ncount (数量): 3\nenv (环境): null\ntags (标签): []\nnotify (通知): false',
    )
  })

  test('refuses values the fields do not allow', () => {
    expect(reason(() => parseAnswer(form, { type: 'form', values: { version: '  ' } }))).toBe(
      'values.version is required',
    )
    expect(reason(values({ count: 9 }))).toBe('values.count must be at most 5')
    expect(reason(values({ env: 'dev' }))).toBe(
      "values.env must be one of the field's option values",
    )
    expect(reason(values({ tags: ['a', 'a'] }))).toBe('values.tags names a value twice')
    expect(reason(values({ extra: 1 }))).toBe('values has unknown fields: extra')
  })

  test('a choice is an index of its options; a confirmation a yes or no', () => {
    const choice = parseBlock({
      type: 'choice',
      question: 'q',
      options: [
        { label: 'A', consequence: 'a' },
        { label: 'B', consequence: 'b' },
      ],
    })
    if (choice.type !== 'choice') throw new Error('expected a choice')
    expect(describeAnswer(choice, parseAnswer(choice, { type: 'choice', choice: 1 }))).toBe(
      'choice: B',
    )
    expect(reason(() => parseAnswer(choice, { type: 'choice', choice: 2 }))).toBe(
      'choice must be the index of an option',
    )
    expect(reason(() => parseAnswer(choice, { type: 'confirm', approved: true }))).toBe(
      'the answer must be a choice',
    )
  })
})
