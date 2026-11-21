// @vitest-environment happy-dom
import { editorKit, type PartialBlock } from '@dou-ui/editor'
import { blocksToMarkdown, markdownToBlocks } from '@dou-ui/editor/markdown'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { describe, expect, test } from 'vitest'
import { anchorQuotes } from './comment-anchors'
import { documentCodeLanguages, highlightDocumentCode } from './document-code'
import { resolveShikiLanguage } from '../highlighting/languages'
import { createShikiService } from '../highlighting/shiki'

const stripIds = (value: unknown) =>
  JSON.parse(JSON.stringify(value, (key, item) => (key === 'id' ? undefined : item)))

describe('document code highlighting', () => {
  test('resolves common language names and aliases, and ignores plain or unsupported tags', () => {
    expect(resolveShikiLanguage('ts')).toBe('typescript')
    expect(resolveShikiLanguage('TypeScript')).toBe('typescript')
    expect(resolveShikiLanguage(' js ')).toBe('javascript')
    expect(resolveShikiLanguage('golang')).toBe('go')
    expect(resolveShikiLanguage('sh')).toBe('bash')
    expect(resolveShikiLanguage('zsh')).toBe('bash')
    expect(resolveShikiLanguage('shell')).toBe('bash')
    expect(resolveShikiLanguage('protobuf')).toBe('proto')
    expect(resolveShikiLanguage('proto')).toBe('proto')
    expect(resolveShikiLanguage('md')).toBe('markdown')
    expect(resolveShikiLanguage('yml')).toBe('yaml')
    expect(resolveShikiLanguage('py')).toBe('python')
    expect(resolveShikiLanguage('rs')).toBe('rust')
    expect(resolveShikiLanguage('docker')).toBe('dockerfile')
    expect(resolveShikiLanguage('ps1')).toBe('powershell')

    expect(resolveShikiLanguage('')).toBeNull()
    expect(resolveShikiLanguage('text')).toBeNull()
    expect(resolveShikiLanguage('plaintext')).toBeNull()
    expect(resolveShikiLanguage('mermaid')).toBeNull()
    expect(resolveShikiLanguage('not-a-language')).toBeNull()
  })

  test('returns empty string for empty or unsupported code without throwing', async () => {
    expect(await highlightDocumentCode('', 'ts')).toBe('')
    expect(await highlightDocumentCode('graph TD; A-->B;', 'mermaid')).toBe('')
    expect(await highlightDocumentCode('plain text', 'text')).toBe('')
  })

  test('highlights common languages with dual light/dark CSS variables', async () => {
    const shiki = createShikiService(() => createJavaScriptRegexEngine())
    const samples: [string, string][] = [
      ['ts', 'export const answer: number = 42'],
      ['js', 'function greet(name) { return `hi ${name}` }'],
      ['go', 'package main\nfunc main() { println("ok") }'],
      ['json', '{"enabled": true, "count": 3}'],
      ['bash', 'set -euo pipefail\necho "hello"'],
      ['sql', 'SELECT id, name FROM projects WHERE active = true;'],
      ['proto', 'syntax = "proto3";\nmessage Ping { string id = 1; }'],
      ['markdown', '# Heading\n\nSome **bold** text'],
      ['html', '<div class="card">Hello</div>'],
      ['css', '.card { color: var(--dou-color-text); }'],
    ]

    for (const [lang, code] of samples) {
      const html = await shiki.highlightToHtml(code, lang)
      expect(html, `expected highlighted HTML for ${lang}`).toContain('--shiki-light:')
      expect(html, `expected dark theme CSS variable for ${lang}`).toContain('--shiki-dark:')
      const parsed = new DOMParser().parseFromString(html, 'text/html')
      const root = parsed.querySelector('code')
      expect(root?.textContent, `expected exact text content for ${lang}`).toBe(code)
    }

    expect(await shiki.highlightToHtml('plain text', 'text')).toBe('')
  })

  test('keeps document schema, markdown round-trip and comment anchoring identical to default editorKit', () => {
    const source = [
      '# API 规范',
      '',
      '需要评审这段说明文字。',
      '',
      '```proto',
      'syntax = "proto3";',
      'message Item {',
      '  string id = 1;',
      '}',
      '```',
    ].join('\n')

    const plainKit = [editorKit()]
    const highlightedKit = [
      editorKit({
        codeBlock: {
          highlight: highlightDocumentCode,
          languages: documentCodeLanguages,
        },
      }),
    ]

    const withoutHighlight = markdownToBlocks(source, plainKit) as PartialBlock[]
    const withHighlight = markdownToBlocks(source, highlightedKit) as PartialBlock[]

    expect(stripIds(withHighlight)).toEqual(stripIds(withoutHighlight))
    expect(blocksToMarkdown(withHighlight as never, highlightedKit)).toBe(
      blocksToMarkdown(withoutHighlight as never, plainKit),
    )

    const anchored = anchorQuotes(withHighlight, [{ id: 'c1', quote: '评审这段说明' }])
    expect(anchored.found.has('c1')).toBe(true)
    expect(documentCodeLanguages.some((item) => item.value === 'proto')).toBe(true)
  })
})
