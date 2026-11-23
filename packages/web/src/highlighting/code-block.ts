import type { CodeBlockHighlighter } from 'dou-ui'
import { resolveShikiLanguage } from './languages'

function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** Format plain text as `<span class="line">` lines matching `DouCodeBlock`'s highlighter contract. */
export function plainCodeLinesHtml(code: string): string {
  return code
    .split('\n')
    .map((line) => `<span class="line">${escapeHtml(line)}</span>`)
    .join('\n')
}

export type CodeHtmlHighlighter = (code: string, lang: string) => Promise<string>

async function defaultHtmlHighlighter(code: string, lang: string): Promise<string> {
  const { highlightCodeToHtml } = await import('./shiki')
  return highlightCodeToHtml(code, lang)
}

/**
 * Create a `DouCodeBlock` highlighter backed by lazy Shiki dual-theme highlighting.
 * Plain text and unsupported languages resolve synchronously as escaped plain lines without
 * loading Shiki, so `DouCodeBlock` keeps rendering their full text cleanly.
 */
export function createCodeBlockHighlighter(
  highlightHtml: CodeHtmlHighlighter = defaultHtmlHighlighter,
): CodeBlockHighlighter {
  return (code, lang) => {
    if (!code) return ''
    if (!resolveShikiLanguage(lang)) return plainCodeLinesHtml(code)
    return highlightHtml(code, lang).then((html) => html || plainCodeLinesHtml(code))
  }
}

/** Shared Shiki highlighter configured as the default for `DouCodeBlock` across the app. */
export const highlightCodeBlock: CodeBlockHighlighter = createCodeBlockHighlighter()
