import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, test } from 'vitest'

const css = readFileSync(resolve(import.meta.dirname, 'selection.css'), 'utf8')

describe('desktop text selection policy (selection.css)', () => {
  test('declares dou-ui layer order and disables selection on shell chrome in dou.base', () => {
    expect(css).toContain('@layer dou.tokens, dou.base, dou.components;')
    expect(css).toMatch(/@layer dou\.base\s*\{[\s\S]*html,\s*body\s*\{[\s\S]*user-select:\s*none/)
  })

  test('keeps editable controls and core content surfaces selectable', () => {
    for (const selector of [
      'textarea',
      "[contenteditable='true']",
      '.dou-chat-message__content',
      '.dou-markdown',
      '.dou-editor__surface',
      '.dou-code-block__code',
      '.dou-comment-thread__body',
      '.dou-table__cell',
      '.dou-pivot__cell',
      '.dou-dashboard-text',
      '.dou-statistic__content',
      '.dou-descriptions__content',
      '.dou-alert__content',
      '.dou-toast__message',
      "[role='alert']",
    ]) {
      expect(css).toContain(selector)
    }
  })

  test('keeps interactive controls non-selectable and locks document during active drags', () => {
    expect(css).toMatch(/button,\s*button \*[\s\S]*user-select:\s*none/)
    expect(css).toMatch(
      /html\[data-ariadne-dragging\],\s*html\[data-ariadne-dragging\] \*\s*\{[\s\S]*user-select:\s*none !important/,
    )
  })
})
