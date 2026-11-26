import type { FlowDocument } from '@dou-ui/flow'
import { describe, expect, test } from 'vitest'
import * as Y from 'yjs'
import { resolveOptionNoteLayout } from './decision-groups'
import {
  clearPendingPrototypeTweakRollback,
  getPendingPrototypeTweakRollbacks,
  prunePendingPrototypeTweakRollbacksByArtifacts,
  setPendingPrototypeTweakRollback,
} from './feedback-pending'
import {
  prototypeDefinitionKey,
  readDocPrototypeTweakValues,
  resolvePrototypeViewportPoint,
  rollbackDocPrototypeTweakValues,
} from './prototype-bridge'
import {
  areTweakValuesEqual,
  buildSandboxedPrototypeDoc,
  countModifiedTweaks,
  diffTweakValues,
  effectiveTweakValues,
  extractTweaksFromHtml,
  groupTweaksBySection,
  hasTweakValuesDiff,
  isValidPrototypeSrc,
  resolvePrototypeHeaderLayout,
  sanitizeCommentElement,
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

    expect(diffTweakValues(specs, effectiveTweakValues(specs))).toBeUndefined()
    expect(diffTweakValues(specs, { gap: 99, mode: 'unknown', cta: '' })).toEqual({
      gap: 24,
      cta: '',
    })
    expect(hasTweakValuesDiff(specs, undefined, effectiveTweakValues(specs))).toBe(false)
    expect(hasTweakValuesDiff(specs, { gap: 24 }, { gap: 16 })).toBe(true)
    expect(hasTweakValuesDiff(specs, { gap: 99 }, { gap: 24 })).toBe(false)
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

  test('compares sparse tweak values and safely rolls back a whiteboard Y.Doc node', () => {
    expect(areTweakValuesEqual(undefined, undefined)).toBe(true)
    expect(areTweakValuesEqual(undefined, {})).toBe(true)
    expect(
      areTweakValuesEqual({ accent: '#10b981', gap: 24 }, { gap: 24, accent: '#10b981' }),
    ).toBe(true)
    expect(areTweakValuesEqual({ accent: '#10b981' }, { accent: '#ef4444' })).toBe(false)
    expect(areTweakValuesEqual({ accent: '#10b981' }, undefined)).toBe(false)

    const doc = new Y.Doc()
    const nodes = doc.getMap('nodes') as Y.Map<Y.Map<unknown>>
    const fields = new Y.Map<unknown>()
    fields.set('type', 'prototype')
    fields.set('data', {
      title: '原型',
      html: '<!doctype html><html><body>v1</body></html>',
      tweakValues: { accent: '#10b981', gap: 24 },
    })
    nodes.set('proto-1', fields)

    const initialDef = prototypeDefinitionKey({
      html: '<!doctype html><html><body>v1</body></html>',
    })
    expect(readDocPrototypeTweakValues(doc, 'missing')).toEqual({ exists: false })
    expect(readDocPrototypeTweakValues(doc, 'proto-1')).toEqual({
      exists: true,
      tweakValues: { accent: '#10b981', gap: 24 },
      definitionKey: initialDef,
    })

    // Diverged remote/AI value must never be overwritten.
    expect(
      rollbackDocPrototypeTweakValues(doc, 'proto-1', { accent: '#ef4444' }, undefined, initialDef),
    ).toBe('diverged')
    expect(readDocPrototypeTweakValues(doc, 'proto-1')).toEqual({
      exists: true,
      tweakValues: { accent: '#10b981', gap: 24 },
      definitionKey: initialDef,
    })

    // Exact match rolls back cleanly to undefined (deleting tweakValues).
    expect(
      rollbackDocPrototypeTweakValues(
        doc,
        'proto-1',
        { accent: '#10b981', gap: 24 },
        undefined,
        initialDef,
      ),
    ).toBe('rolled-back')
    expect(readDocPrototypeTweakValues(doc, 'proto-1')).toEqual({
      exists: true,
      tweakValues: undefined,
      definitionKey: initialDef,
    })

    // Calling again recognizes it is already reverted.
    expect(
      rollbackDocPrototypeTweakValues(
        doc,
        'proto-1',
        { accent: '#10b981', gap: 24 },
        undefined,
        initialDef,
      ),
    ).toBe('already-reverted')

    // Rolling back a reset (undefined -> previous values) restores previous tweakValues when definition matches.
    expect(
      rollbackDocPrototypeTweakValues(doc, 'proto-1', undefined, { accent: '#2563eb' }, initialDef),
    ).toBe('rolled-back')
    expect(readDocPrototypeTweakValues(doc, 'proto-1')).toEqual({
      exists: true,
      tweakValues: { accent: '#2563eb' },
      definitionKey: initialDef,
    })

    // Once AI updates prototype HTML and clears tweakValues, rollback with old definitionKey is refused.
    fields.set('data', {
      title: '原型',
      html: '<!doctype html><html><body>v2 baked</body></html>',
    })
    expect(
      rollbackDocPrototypeTweakValues(doc, 'proto-1', undefined, { accent: '#2563eb' }, initialDef),
    ).toBe('diverged')
    expect(readDocPrototypeTweakValues(doc, 'proto-1').exists).toBe(true)
    const afterAi = readDocPrototypeTweakValues(doc, 'proto-1')
    expect(afterAi.exists && afterAi.tweakValues).toBeUndefined()

    // Non-user newer artifact version prunes stale pending rollbacks without touching user-only autosaves.
    clearPendingPrototypeTweakRollback('art_prune_test')
    setPendingPrototypeTweakRollback('art_prune_test', {
      nodeId: 'proto-1',
      nextDiff: undefined,
      prevDiff: { accent: '#2563eb' },
      nextSavedKey: 'next',
      prevSavedKey: 'prev',
      needsDocRollback: true,
      definitionKey: initialDef,
      baseVersion: 2,
    })
    prunePendingPrototypeTweakRollbacksByArtifacts([
      { id: 'art_prune_test', version: 2, updatedBy: 'agent' },
      { id: 'art_prune_test', version: 3, updatedBy: 'user' },
    ])
    expect(getPendingPrototypeTweakRollbacks('art_prune_test')).toHaveLength(1)
    prunePendingPrototypeTweakRollbacksByArtifacts([
      { id: 'art_prune_test', version: 3, updatedBy: 'agent' },
    ])
    expect(getPendingPrototypeTweakRollbacks('art_prune_test')).toHaveLength(0)
  })
})
