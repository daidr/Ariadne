import { describe, expect, test } from 'vitest'
import { previewOf } from './artifact-content'

describe('previewOf', () => {
  test('a document keeps its opening Markdown, cut at a line end with fences closed', () => {
    const short = previewOf('document', '# 标题\n\n正文')
    expect(short).toEqual({ kind: 'document', excerpt: '标题 正文', lead: '# 标题\n\n正文' })

    const long = `# 标题\n\n${'字'.repeat(400)}\n\n\`\`\`ts\n${'x\n'.repeat(300)}\`\`\``
    const preview = previewOf('document', long)
    if (preview.kind !== 'document') throw new Error('kind')
    expect(preview.lead.length).toBeLessThanOrEqual(610)
    expect(preview.lead.startsWith(`# 标题\n\n${'字'.repeat(400)}\n\n\`\`\`ts\nx\n`)).toBe(true)
    expect(preview.lead.endsWith('\n```')).toBe(true)
  })

  test('dashboard widgets without a place stack under the placed ones', () => {
    const preview = previewOf(
      'dashboard',
      JSON.stringify({
        version: 1,
        dashboard: {
          version: 1,
          widgets: [
            {
              id: 'a',
              type: 'stat',
              title: '数',
              props: {},
              place: { lg: { x: 0, y: 0, w: 12, h: 2 } },
            },
            { id: 'b', type: 'table', props: {} },
            { id: 'c', type: 'chart', props: {} },
            { id: 'd', type: 'text', props: {} },
          ],
        },
        data: {},
      }),
    )
    expect(preview).toMatchObject({
      tiles: [
        { type: 'stat', title: '数', x: 0, y: 0, w: 12, h: 2 },
        { type: 'table', title: null, x: 0, y: 2, w: 6, h: 3 },
        { type: 'chart', x: 6, y: 2 },
        { type: 'text', x: 0, y: 5 },
      ],
    })
  })
})
