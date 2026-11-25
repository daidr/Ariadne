// @vitest-environment happy-dom
import type { PartialBlock } from '@dou-ui/editor'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import DocumentBody from './DocumentBody.vue'
import DocumentToc from './DocumentToc.vue'
import {
  computeDocumentTocLayout,
  extractDocumentHeadings,
  isTocCollidingWithTables,
  pickActiveHeadingId,
  sameDocumentHeadings,
  sampleTocMiniBars,
  TOC_COLLISION_ENTER_GAP,
  TOC_COLLISION_EXIT_GAP,
  TOC_LEFT,
  TOC_MAX_WIDTH,
  TOC_MIN_WIDTH,
  TOC_PEEK_WIDTH,
  TOC_RAIL_MIN_READING_LEFT,
  TOC_READING_GAP,
  type DocumentTocHeading,
} from './document-toc'

describe('extractDocumentHeadings', () => {
  it('extracts headings in document order including nested children and normalizes depth', () => {
    const blocks: PartialBlock[] = [
      { id: 'p1', type: 'paragraph', content: [{ type: 'text', text: 'Intro' }] },
      {
        id: 'h2-a',
        type: 'heading',
        props: { level: 2 },
        content: [{ type: 'text', text: 'Overview' }],
      },
      {
        id: 'h3-a',
        type: 'heading',
        props: { level: 3 },
        content: [
          { type: 'text', text: 'API ' },
          { type: 'break' },
          { type: 'text', text: 'Details' },
          { type: 'inline', name: 'mention', props: { label: ' v2' } },
        ],
      },
      {
        id: 'toggle-1',
        type: 'toggle',
        content: [{ type: 'text', text: 'More' }],
        children: [{ id: 'h2-b', type: 'heading', props: { level: 2 }, content: 'Nested Section' }],
      },
      {
        id: 'h-empty',
        type: 'heading',
        props: { level: 2 },
        content: [{ type: 'text', text: '   ' }],
      },
      { type: 'heading', props: { level: 2 }, content: [{ type: 'text', text: 'Missing ID' }] },
    ]

    const headings = extractDocumentHeadings(blocks)
    expect(headings).toEqual([
      { id: 'h2-a', level: 2, depth: 0, text: 'Overview' },
      { id: 'h3-a', level: 3, depth: 1, text: 'API Details v2' },
      { id: 'h2-b', level: 2, depth: 0, text: 'Nested Section' },
    ])
  })

  it('returns empty array when document has no non-empty headings', () => {
    expect(
      extractDocumentHeadings([
        { id: 'p1', type: 'paragraph', content: [{ type: 'text', text: 'Only body text' }] },
      ]),
    ).toEqual([])
  })

  it('compares heading lists structurally', () => {
    const a: DocumentTocHeading[] = [{ id: 'h1', level: 1, depth: 0, text: 'Title' }]
    const b: DocumentTocHeading[] = [{ id: 'h1', level: 1, depth: 0, text: 'Title' }]
    const c: DocumentTocHeading[] = [{ id: 'h1', level: 1, depth: 0, text: 'Renamed' }]
    expect(sameDocumentHeadings(a, b)).toBe(true)
    expect(sameDocumentHeadings(a, c)).toBe(false)
  })
})

describe('computeDocumentTocLayout', () => {
  it('docks and caps width on wide viewports', () => {
    const layout = computeDocumentTocLayout(340)
    expect(layout).toEqual({
      mode: 'docked',
      width: TOC_MAX_WIDTH,
      dockedRight: TOC_LEFT + TOC_MAX_WIDTH,
    })
  })

  it('scales docked width down to TOC_MIN_WIDTH without squeezing the reading column', () => {
    const readingLeft = TOC_LEFT + TOC_READING_GAP + TOC_MIN_WIDTH + 12
    const layout = computeDocumentTocLayout(readingLeft)
    expect(layout).toEqual({
      mode: 'docked',
      width: TOC_MIN_WIDTH + 12,
      dockedRight: TOC_LEFT + TOC_MIN_WIDTH + 12,
    })
  })

  it('collapses into the left-edge rail on medium panes', () => {
    const layout = computeDocumentTocLayout(TOC_RAIL_MIN_READING_LEFT + 10)
    expect(layout).toEqual({
      mode: 'rail',
      width: TOC_PEEK_WIDTH,
      dockedRight: 0,
    })
  })

  it('hides completely on narrow panes or invalid reading offsets', () => {
    expect(computeDocumentTocLayout(TOC_RAIL_MIN_READING_LEFT - 1)).toEqual({
      mode: 'hidden',
      width: 0,
      dockedRight: 0,
    })
    expect(computeDocumentTocLayout(0)).toEqual({ mode: 'hidden', width: 0, dockedRight: 0 })
    expect(computeDocumentTocLayout(Number.NaN)).toEqual({
      mode: 'hidden',
      width: 0,
      dockedRight: 0,
    })
  })
})

describe('isTocCollidingWithTables', () => {
  const viewport = { top: 0, bottom: 800 }
  const tocVertical = { top: 24, bottom: 220 }
  const dockedTocRight = 216

  it('does not collide while a wide table sits at its reading column left edge', () => {
    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical,
        currentlyCollided: false,
        tables: [{ visibleLeft: 264, top: 120, bottom: 420, height: 300 }],
      }),
    ).toBe(false)
  })

  it('collides when a visible table scrolls leftward into the TOC zone', () => {
    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical,
        currentlyCollided: false,
        tables: [
          {
            visibleLeft: dockedTocRight + TOC_COLLISION_ENTER_GAP,
            top: 120,
            bottom: 420,
            height: 300,
          },
        ],
      }),
    ).toBe(true)
  })

  it('applies hysteresis between enter and exit thresholds to avoid rapid toggling', () => {
    const midEdge =
      dockedTocRight + Math.floor((TOC_COLLISION_ENTER_GAP + TOC_COLLISION_EXIT_GAP) / 2)
    const table = { visibleLeft: midEdge, top: 120, bottom: 420, height: 300 }

    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical,
        currentlyCollided: false,
        tables: [table],
      }),
    ).toBe(false)

    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical,
        currentlyCollided: true,
        tables: [table],
      }),
    ).toBe(true)

    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical,
        currentlyCollided: true,
        tables: [{ ...table, visibleLeft: dockedTocRight + TOC_COLLISION_EXIT_GAP + 1 }],
      }),
    ).toBe(false)
  })

  it('ignores scrolled tables outside the vertical viewport or barely peeking below the TOC', () => {
    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical,
        currentlyCollided: false,
        tables: [
          { visibleLeft: 60, top: -400, bottom: -10, height: 390 },
          { visibleLeft: 60, top: 820, bottom: 1120, height: 300 },
          { visibleLeft: 60, top: 785, bottom: 1085, height: 300 },
        ],
      }),
    ).toBe(false)
  })

  it('collides when a scrolled table is meaningfully visible in the viewport even below a short TOC', () => {
    expect(
      isTocCollidingWithTables({
        dockedTocRight,
        viewport,
        tocVertical: { top: 24, bottom: 120 },
        currentlyCollided: false,
        tables: [{ visibleLeft: 90, top: 260, bottom: 520, height: 260 }],
      }),
    ).toBe(true)
  })
})

describe('pickActiveHeadingId', () => {
  const headings = [
    { id: 'h1', relativeTop: -240 },
    { id: 'h2', relativeTop: 18 },
    { id: 'h3', relativeTop: 420 },
  ]

  it('picks the last heading past the activation offset', () => {
    expect(
      pickActiveHeadingId({
        headings,
        scrollTop: 280,
        clientHeight: 700,
        scrollHeight: 1600,
      }),
    ).toBe('h2')
  })

  it('falls back to the first heading near the top and last heading at the bottom', () => {
    expect(
      pickActiveHeadingId({
        headings: [
          { id: 'h1', relativeTop: 120 },
          { id: 'h2', relativeTop: 520 },
        ],
        scrollTop: 0,
        clientHeight: 700,
        scrollHeight: 1600,
      }),
    ).toBe('h1')

    expect(
      pickActiveHeadingId({
        headings,
        scrollTop: 900,
        clientHeight: 700,
        scrollHeight: 1600,
      }),
    ).toBe('h3')
  })
})

describe('sampleTocMiniBars', () => {
  it('keeps small heading lists verbatim and downsamples very long lists while highlighting active', () => {
    const many: DocumentTocHeading[] = Array.from({ length: 36 }, (_, i) => ({
      id: `h-${i}`,
      level: (i % 3) + 1,
      depth: i % 3,
      text: `Section ${i}`,
    }))
    const sampled = sampleTocMiniBars(many, 'h-24', 12)
    expect(sampled).toHaveLength(12)
    expect(sampled.filter((bar) => bar.active)).toHaveLength(1)
  })
})

describe('DocumentToc.vue', () => {
  const headings: DocumentTocHeading[] = [
    { id: 'h1', level: 1, depth: 0, text: '架构概览' },
    { id: 'h2', level: 2, depth: 1, text: '数据流' },
    { id: 'h3', level: 3, depth: 2, text: '宽表对比' },
  ]

  it('stays hidden when there are no headings or mode is hidden', () => {
    const emptyWrapper = mount(DocumentToc, {
      props: { headings: [], activeId: null, mode: 'docked', width: 200, collided: false },
    })
    expect(emptyWrapper.find('.document-toc').exists()).toBe(false)
    emptyWrapper.unmount()

    const hiddenWrapper = mount(DocumentToc, {
      props: { headings, activeId: 'h1', mode: 'hidden', width: 0, collided: false },
    })
    expect(hiddenWrapper.find('.document-toc').exists()).toBe(false)
    hiddenWrapper.unmount()
  })

  it('renders headings, highlights active item, and emits select on click', async () => {
    const wrapper = mount(DocumentToc, {
      props: { headings, activeId: 'h2', mode: 'docked', width: 196, collided: false },
    })
    const items = wrapper.findAll('button.document-toc__item')
    expect(items).toHaveLength(3)
    expect(items[1]!.classes()).toContain('is-active')
    expect(items[1]!.attributes('aria-current')).toBe('location')

    await items[2]!.trigger('click')
    expect(wrapper.emitted('select')).toEqual([['h3']])
    wrapper.unmount()
  })

  it('collapses leftward on table collision, supports hover/click peek, and restores cleanly', async () => {
    vi.useFakeTimers()
    try {
      const wrapper = mount(DocumentToc, {
        props: { headings, activeId: 'h1', mode: 'docked', width: 200, collided: true },
      })
      const root = wrapper.get('.document-toc')
      const panel = wrapper.get('.document-toc__panel')
      const rail = wrapper.get('button.document-toc__rail')

      expect(root.classes()).toContain('is-collapsed')
      expect(root.classes()).toContain('is-collided')
      expect(panel.attributes('aria-hidden')).toBe('true')
      expect(rail.attributes('aria-hidden')).toBe('false')

      await rail.trigger('pointerenter', { pointerType: 'mouse' })
      expect(root.classes()).toContain('is-peeking')
      expect(root.classes()).toContain('is-floating')
      expect(panel.attributes('aria-hidden')).toBe('false')

      // Clicking a heading closes the floating peek and emits select.
      await wrapper.findAll('button.document-toc__item')[1]!.trigger('click')
      expect(wrapper.emitted('select')).toEqual([['h2']])
      expect(root.classes()).not.toContain('is-peeking')

      // Touch click toggles peek without pointerenter conflict.
      await rail.trigger('pointerenter', { pointerType: 'touch' })
      expect(root.classes()).not.toContain('is-peeking')
      await rail.trigger('click')
      expect(root.classes()).toContain('is-peeking')

      // Pressing Escape closes peek.
      await panel.trigger('keydown.esc')
      expect(root.classes()).not.toContain('is-peeking')

      // When collision clears, docked state restores without peeking.
      await wrapper.setProps({ collided: false })
      expect(root.classes()).not.toContain('is-collapsed')
      expect(panel.attributes('aria-hidden')).toBe('false')
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })
})

function makeRect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    x: left,
    y: top,
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
    toJSON: () => ({}),
  } as DOMRect
}

describe('DocumentBody.vue TOC integration', () => {
  it('extracts headings from DouEditor and collapses the TOC when a visible table scrolls left into it', async () => {
    let nextFrameId = 1
    const frames = new Map<number, FrameRequestCallback>()
    const rafSpy = vi
      .spyOn(window, 'requestAnimationFrame')
      .mockImplementation((cb: FrameRequestCallback) => {
        const id = nextFrameId++
        frames.set(id, cb)
        return id
      })
    const cancelSpy = vi.spyOn(window, 'cancelAnimationFrame').mockImplementation((id: number) => {
      frames.delete(id)
    })
    const flushFrames = () => {
      const pending = Array.from(frames.entries())
      frames.clear()
      for (const [, cb] of pending) cb(0)
    }
    const md = [
      '# 总体架构',
      '',
      '## 宽表章节',
      '',
      '| 列一 | 列二 | 列三 | 列四 |',
      '| --- | --- | --- | --- |',
      '| A | B | C | D |',
      '',
      '### 结论',
      '',
      '收尾说明。',
    ].join('\n')

    const wrapper = mount(DocumentBody, {
      props: {
        content: md,
        projectId: 'prj_test',
        readonly: true,
        label: '测试文档',
      },
      attachTo: document.body,
    })

    try {
      await nextTick()
      const shell = wrapper.get('.document-shell').element as HTMLElement
      const scrollEl = wrapper.get('.document').element as HTMLElement
      const surface = wrapper.get('.dou-editor__surface').element as HTMLElement
      const frame = wrapper.get('.dou-editor__content--table').element as HTMLElement
      const table = frame.querySelector('table') as HTMLElement

      let tableLeft = 336
      surface.style.paddingLeft = '288px'
      vi.spyOn(shell, 'getBoundingClientRect').mockImplementation(() => makeRect(0, 0, 1440, 900))
      vi.spyOn(scrollEl, 'getBoundingClientRect').mockImplementation(() =>
        makeRect(0, 0, 1440, 900),
      )
      vi.spyOn(surface, 'getBoundingClientRect').mockImplementation(() =>
        makeRect(48, 0, 1344, 1400),
      )
      vi.spyOn(frame, 'getBoundingClientRect').mockImplementation(() =>
        makeRect(58, 220, 1334, 260),
      )
      vi.spyOn(table, 'getBoundingClientRect').mockImplementation(() =>
        makeRect(tableLeft, 220, 1600, 260),
      )

      const blocks = Array.from(scrollEl.querySelectorAll<HTMLElement>('[data-block-id]'))
      blocks.forEach((block, index) => {
        const content = block.querySelector<HTMLElement>(':scope > .dou-editor__content') ?? block
        vi.spyOn(block, 'getBoundingClientRect').mockImplementation(() =>
          makeRect(336, 32 + index * 140, 768, 40),
        )
        if (content !== frame) {
          vi.spyOn(content, 'getBoundingClientRect').mockImplementation(() =>
            makeRect(336, 32 + index * 140, 768, 40),
          )
        }
      })

      const scrollToSpy = vi.fn()
      scrollEl.scrollTo = scrollToSpy

      // Trigger layout measurement via captured scroll event.
      scrollEl.dispatchEvent(new Event('scroll'))
      flushFrames()
      await nextTick()

      const toc = wrapper.get('.document-toc')
      const items = wrapper.findAll('button.document-toc__item')
      expect(items.map((item) => item.text())).toEqual(['总体架构', '宽表章节', '结论'])
      expect(toc.classes()).not.toContain('is-collided')
      expect(toc.classes()).not.toContain('is-collapsed')

      // Simulate scrolling the wide table horizontally so its content enters the left TOC zone.
      tableLeft = 140
      frame.dispatchEvent(new Event('scroll'))
      flushFrames()
      await nextTick()
      expect(toc.classes()).toContain('is-collided')
      expect(toc.classes()).toContain('is-collapsed')

      // Simulate scrolling the table back to the reading column edge -> TOC restores.
      tableLeft = 336
      frame.dispatchEvent(new Event('scroll'))
      flushFrames()
      await nextTick()
      expect(toc.classes()).not.toContain('is-collided')
      expect(toc.classes()).not.toContain('is-collapsed')

      // Clicking a heading scrolls the document container smoothly.
      await items[2]!.trigger('click')
      expect(scrollToSpy).toHaveBeenCalledTimes(1)
    } finally {
      wrapper.unmount()
      rafSpy.mockRestore()
      cancelSpy.mockRestore()
    }
  })
})
