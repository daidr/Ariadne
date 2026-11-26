import type { FlowDocument } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import { resolveOptionNoteLayout } from './decision-groups'
import { resolvePrototypeViewportPoint } from './prototype-bridge'
import {
  buildSandboxedPrototypeDoc,
  countModifiedTweaks,
  effectiveTweakValues,
  estimateTweakLabelWidth,
  extractTweaksFromHtml,
  groupTweaksBySection,
  isValidPrototypeSrc,
  resolveEnumTweakControl,
  resolvePrototypeHeaderLayout,
  sanitizeCommentElement,
} from './prototype-tweaks'

const makeEnumOptions = (labels: string[]) => labels.map((label, i) => ({ value: `v${i}`, label }))

describe('prototype tweaks & sandbox builder', () => {
  test('extracts and normalizes declarative tweaks from HTML script tag', () => {
    const html = `<!doctype html><html><head>
      <script data-ariadne-tweaks type="application/json">
      {
        "tweaks": [
          {"key":"accent","label":"主色","kind":"color","default":"#2563eb","section":"外观","swatches":["#2563eb","#10b981"]},
          {"key":"density","label":"密度","kind":"enum","default":"comfortable","options":["compact","comfortable"],"section":"布局"},
          {"key":"gap","label":"间距","kind":"range","min":8,"max":24,"step":2,"unit":"px","default":16,"section":"布局"},
          {"key":"dense","label":"紧凑开关","kind":"boolean","default":false},
          {"key":"ctaLabel","label":"按钮文案","kind":"text","default":"立即发布"}
        ]
      }
      </script>
    </head><body></body></html>`

    const specs = extractTweaksFromHtml(html)
    expect(specs).toHaveLength(5)
    expect(specs[0]?.kind).toBe('color')
    expect(specs[1]?.options).toEqual([
      { value: 'compact', label: 'compact' },
      { value: 'comfortable', label: 'comfortable' },
    ])
    expect(specs[2]?.unit).toBe('px')
    expect(specs[3]?.default).toBe(false)
    expect(specs[4]).toEqual({
      key: 'ctaLabel',
      label: '按钮文案',
      kind: 'text',
      default: '立即发布',
    })

    const grouped = groupTweaksBySection(specs)
    expect(grouped.map((g) => g.section)).toEqual(['外观', '布局', null])
    expect(grouped[1]?.items.map((i) => i.key)).toEqual(['density', 'gap'])
  })

  test('computes effective values, clamps ranges, and counts modifications including text & boolean', () => {
    const specs = extractTweaksFromHtml(
      `<script type="application/json" data-ariadne-tweaks>[
        {"key":"accent","kind":"color","default":"#2563eb"},
        {"key":"gap","kind":"range","min":8,"max":24,"default":16,"unit":"px"},
        {"key":"mode","kind":"enum","default":"light","options":["light","dark"]},
        {"key":"dense","kind":"boolean","default":false},
        {"key":"cta","kind":"text","default":"确认发布"}
      ]</script>`,
    )
    const vals = effectiveTweakValues(specs, {
      accent: '#10b981',
      gap: 99,
      mode: 'unknown',
      dense: true,
      cta: '立即灰度发布',
    })
    expect(vals).toEqual({
      accent: '#10b981',
      gap: 24,
      mode: 'light',
      dense: true,
      cta: '立即灰度发布',
    })
    expect(countModifiedTweaks(specs, vals)).toBe(4)

    // Empty string is preserved for text tweaks and counted as modified.
    const cleared = effectiveTweakValues(specs, { cta: '' })
    expect(cleared.cta).toBe('')
    expect(countModifiedTweaks(specs, cleared)).toBe(1)
  })

  test('prepends strict CSP before leading comments or external script tags and escapes script closes', () => {
    const html =
      '<!-- <head> trick --><script src="https://evil.example/leak.js"></script><head><title>Demo</title></head><body><h1>Hi</h1></body>'
    const doc = buildSandboxedPrototypeDoc(
      html,
      'frame-1',
      [
        {
          key: 'note',
          label: 'Note',
          kind: 'text',
          default: '</script><script>alert(1)</script>',
        },
      ],
      { note: '</script><script>alert(1)</script>' },
    )
    const cspIdx = doc.indexOf('Content-Security-Policy')
    const commentIdx = doc.indexOf('<!-- <head> trick -->')
    const externalScriptIdx = doc.indexOf('https://evil.example/leak.js')
    expect(cspIdx).toBeGreaterThan(-1)
    expect(cspIdx).toBeLessThan(commentIdx)
    expect(cspIdx).toBeLessThan(externalScriptIdx)
    expect(doc).toContain("connect-src 'none'")
    expect(doc).toContain('data-ariadne-bridge="1"')
    expect(doc).not.toContain('</script><script>alert(1)')
  })

  test('validates prototype data.src strictly and sanitizes untrusted comment element payloads', () => {
    expect(isValidPrototypeSrc('/api/files/fil_abc123-XYZ')).toBe(true)
    expect(isValidPrototypeSrc('/api/files/../../api/settings')).toBe(false)
    expect(isValidPrototypeSrc('/api/files/fil_abc/extra')).toBe(false)
    expect(isValidPrototypeSrc('https://evil.example/x.html')).toBe(false)
    expect(isValidPrototypeSrc('')).toBe(false)

    expect(sanitizeCommentElement(null)).toBeNull()
    expect(sanitizeCommentElement([])).toBeNull()
    expect(
      sanitizeCommentElement({
        tag: 'button',
        role: 'button',
        label: '  立即   发布  ',
        selector: 'button#go',
        rect: { x: -0.5, y: 0.25, w: 1.5, h: 0.2 },
        injected: { evil: true },
      }),
    ).toEqual({
      tag: 'button',
      role: 'button',
      label: '立即 发布',
      selector: 'button#go',
      rect: { x: 0, y: 0.25, w: 1, h: 0.2 },
    })
  })

  test('maps comment pins on standalone and option-embedded prototypes to logical viewport coords', () => {
    const doc: FlowDocument = {
      version: 1,
      nodes: [
        {
          id: 'opt-1',
          type: 'option',
          x: 100,
          y: 100,
          width: 512,
          height: 400,
          data: { title: '方案 A' },
        },
        {
          id: 'opt-1-prototype',
          type: 'prototype',
          parent: 'opt-1',
          x: 116,
          y: 140,
          width: 480,
          height: 336, // 36px header + 300px stage -> scale = 480/1280 = 0.375, rendered 480x300
          data: { title: '工作台', html: '<div/>', viewportWidth: 1280, viewportHeight: 800 },
        },
      ],
      edges: [],
    }

    // Center of stage on the prototype node directly:
    // header = 36, stageH = 300 -> stage middle is y = 36 + 150 = 186 -> pinY = 186 / 336
    const direct = resolvePrototypeViewportPoint(doc, 'opt-1-prototype', 0.5, 186 / 336)
    expect(direct).toEqual({
      prototypeNodeId: 'opt-1-prototype',
      viewportX: 640,
      viewportY: 400,
    })

    // Pin inside the header returns null.
    const inHeader = resolvePrototypeViewportPoint(doc, 'opt-1-prototype', 0.5, 10 / 336)
    expect(inHeader).toBeNull()

    // Pin on parent option covering the center of the embedded prototype resolves to the prototype.
    // canvas point = (116 + 240, 140 + 186) = (356, 326) -> relative to opt-1: (256/512, 226/400)
    const viaOption = resolvePrototypeViewportPoint(doc, 'opt-1', 256 / 512, 226 / 400)
    expect(viaOption).toEqual({
      prototypeNodeId: 'opt-1-prototype',
      viewportX: 640,
      viewportY: 400,
    })
  })

  test('adapts prototype header chrome cleanly across narrow portrait and wide desktop widths', () => {
    // Narrow 240px resized card with tweaks collapses both action labels and hides inline size.
    expect(resolvePrototypeHeaderLayout(240, { hasTweaks: true, interacting: false })).toEqual({
      showSize: false,
      compactTweaks: true,
      compactInteract: true,
      compactModeTag: false,
    })

    // Portrait mobile card (280–328px) with tweaks hides inline size so title + buttons have room.
    expect(resolvePrototypeHeaderLayout(280, { hasTweaks: true, interacting: false })).toEqual({
      showSize: false,
      compactTweaks: false,
      compactInteract: false,
      compactModeTag: false,
    })
    expect(resolvePrototypeHeaderLayout(300, { hasTweaks: true, interacting: true })).toEqual({
      showSize: false,
      compactTweaks: true,
      compactInteract: false,
      compactModeTag: true,
    })

    // Wide desktop card (480px) shows inline dimensions and full action labels in both modes.
    expect(resolvePrototypeHeaderLayout(480, { hasTweaks: true, interacting: false })).toEqual({
      showSize: true,
      compactTweaks: false,
      compactInteract: false,
      compactModeTag: false,
    })
    expect(resolvePrototypeHeaderLayout(480, { hasTweaks: true, interacting: true })).toEqual({
      showSize: true,
      compactTweaks: false,
      compactInteract: false,
      compactModeTag: false,
    })

    // Cards without tweaks can show inline size earlier (>= 320px).
    expect(resolvePrototypeHeaderLayout(330, { hasTweaks: false, interacting: false })).toEqual({
      showSize: true,
      compactTweaks: false,
      compactInteract: false,
      compactModeTag: false,
    })
  })

  test('computes option card note layout for empty, legacy tight inset, reserved inset, and note-only cards', () => {
    expect(resolveOptionNoteLayout(undefined, 240, 40)).toEqual({ mode: 'none', maxLines: 0 })
    expect(resolveOptionNoteLayout('   ', 240, 40)).toEqual({ mode: 'none', maxLines: 0 })

    // Legacy boards with children placed at y + 40 fall back to a compact two-line header.
    expect(resolveOptionNoteLayout('Legacy short note', 240, 40)).toEqual({
      mode: 'compact',
      maxLines: 1,
    })

    // New boards reserve inset height for 1, 2, or 3 wrapped lines above child nodes.
    expect(resolveOptionNoteLayout('One-line note', 240, 68)).toEqual({
      mode: 'block',
      maxLines: 1,
    })
    expect(resolveOptionNoteLayout('Two-line wrapped note', 260, 88)).toEqual({
      mode: 'block',
      maxLines: 2,
    })
    expect(resolveOptionNoteLayout('Three-line wrapped note', 300, 108)).toEqual({
      mode: 'block',
      maxLines: 3,
    })

    // Pure-note option cards without children use the card height.
    expect(resolveOptionNoteLayout('Standalone note card', 160, null)).toEqual({
      mode: 'block',
      maxLines: 5,
    })
  })

  test('picks segmented, wrapping tags, or select for enum tweaks based on count and label width', () => {
    // Short 2–4 option labels fit comfortably inside equal-width segmented controls.
    expect(resolveEnumTweakControl(makeEnumOptions(['浅色', '深色']))).toBe('segmented')
    expect(resolveEnumTweakControl(makeEnumOptions(['compact', 'comfortable']))).toBe('segmented')
    expect(resolveEnumTweakControl(makeEnumOptions(['浅色', '深色', '跟随系统']))).toBe('segmented')
    expect(resolveEnumTweakControl(makeEnumOptions(['今日', '近 7 天', '近 30 天']))).toBe(
      'segmented',
    )
    expect(resolveEnumTweakControl(makeEnumOptions(['sm', 'md', 'lg', 'xl']))).toBe('segmented')
    expect(resolveEnumTweakControl(makeEnumOptions(['全部', '文档', '画板', '仪表盘']))).toBe(
      'segmented',
    )

    // Longer 2–4 option labels that would clip in equal-width 288px segments switch to wrapping tags.
    expect(
      resolveEnumTweakControl(
        makeEnumOptions(['侧栏顶部（推荐）', '顶栏常驻按钮', '单项目右侧 [更多]']),
      ),
    ).toBe('tags')
    expect(
      resolveEnumTweakControl(
        makeEnumOptions(['今日（日报）', '近 7 天（周报）', '近 30 天（月报）']),
      ),
    ).toBe('tags')
    expect(
      resolveEnumTweakControl(
        makeEnumOptions(['默认折叠（0 干扰）', '展开 AI 复盘摘要', '纯规则（无 AI）']),
      ),
    ).toBe('tags')
    expect(resolveEnumTweakControl(makeEnumOptions(['关', '开', '仅在桌面端自动启用']))).toBe(
      'tags',
    )
    expect(resolveEnumTweakControl(makeEnumOptions(['Comfortable', 'Compact', 'Spacious']))).toBe(
      'tags',
    )

    // Single options, > 4 options, and ultra-long labels (> single chip width) fall back to select.
    expect(resolveEnumTweakControl(undefined)).toBe('select')
    expect(resolveEnumTweakControl(makeEnumOptions(['唯一选项']))).toBe('select')
    expect(resolveEnumTweakControl(makeEnumOptions(['一', '二', '三', '四', '五']))).toBe('select')
    expect(
      resolveEnumTweakControl(
        makeEnumOptions(['这是一段超过整个面板单行标签宽度的非常长描述方案', '另一项超长描述']),
      ),
    ).toBe('select')

    // Emoji / surrogate pairs count as a single wide glyph rather than two UTF-16 code units.
    expect(estimateTweakLabelWidth('🎨')).toBe(13.5)
  })
})
