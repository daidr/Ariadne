import type { FlowDocument } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import { resolvePrototypeViewportPoint } from './prototype-bridge'
import {
  buildSandboxedPrototypeDoc,
  countModifiedTweaks,
  effectiveTweakValues,
  extractTweaksFromHtml,
  groupTweaksBySection,
} from './prototype-tweaks'

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

  test('computes effective values, clamps ranges, and counts modifications', () => {
    const specs = extractTweaksFromHtml(
      `<script type="application/json" data-ariadne-tweaks>[
        {"key":"accent","kind":"color","default":"#2563eb"},
        {"key":"gap","kind":"range","min":8,"max":24,"default":16,"unit":"px"},
        {"key":"mode","kind":"enum","default":"light","options":["light","dark"]}
      ]</script>`,
    )
    const vals = effectiveTweakValues(specs, {
      accent: '#10b981',
      gap: 99,
      mode: 'unknown',
    })
    expect(vals).toEqual({ accent: '#10b981', gap: 24, mode: 'light' })
    expect(countModifiedTweaks(specs, vals)).toBe(2)
  })

  test('injects strict CSP and safely escapes script closing sequences', () => {
    const html =
      '<!doctype html><html><head><title>Demo</title></head><body><h1>Hi</h1></body></html>'
    const doc = buildSandboxedPrototypeDoc(
      html,
      'frame-1',
      [
        {
          key: 'note',
          label: 'Note',
          kind: 'color',
          default: '</script><script>alert(1)</script>',
        },
      ],
      { note: '</script><script>alert(1)</script>' },
    )
    expect(doc).toContain('Content-Security-Policy')
    expect(doc).toContain("connect-src 'none'")
    expect(doc).toContain('data-ariadne-bridge="1"')
    expect(doc).not.toContain('</script><script>alert(1)')
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
})
