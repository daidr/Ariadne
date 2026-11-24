import type { CodeBlockHighlightContext, CodeBlockHighlighter } from 'dou-ui'
import { resolveShikiLanguage } from './languages'
import { shikiWorkerPool, type HighlightOptions } from './shiki-worker-pool'

export type { CodeBlockHighlightContext }

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

export type CodeHtmlHighlighter = (
  code: string,
  lang: string,
  options?: HighlightOptions,
) => Promise<string>

export interface CodeHtmlHighlighterSource {
  peek?(code: string, lang: string): string | null
  highlight(code: string, lang: string, options?: HighlightOptions): Promise<string>
}

/**
 * Create a `DouCodeBlock` highlighter backed by lazy Shiki dual-theme highlighting.
 * Plain text and unsupported languages resolve synchronously as escaped plain lines without
 * starting workers or loading Shiki. Cached results also return synchronously so scrolling back
 * through long conversations paints highlighted blocks immediately without flashing.
 */
export function createCodeBlockHighlighter(
  source: CodeHtmlHighlighter | CodeHtmlHighlighterSource = shikiWorkerPool,
): CodeBlockHighlighter {
  const peek = typeof source === 'function' ? undefined : source.peek?.bind(source)
  const highlightHtml = typeof source === 'function' ? source : source.highlight.bind(source)

  return (code: string, lang: string, context?: CodeBlockHighlightContext) => {
    if (!code) return ''
    if (!resolveShikiLanguage(lang)) return plainCodeLinesHtml(code)
    const cached = peek?.(code, lang)
    if (cached) return cached
    const options = context?.signal ? { signal: context.signal } : undefined
    return highlightHtml(code, lang, options).then(
      (html) => html || plainCodeLinesHtml(code),
      () => (context?.signal?.aborted ? '' : plainCodeLinesHtml(code)),
    )
  }
}

/** Shared Shiki worker-pool highlighter configured as the default for `DouCodeBlock` across the app. */
export const highlightCodeBlock: CodeBlockHighlighter = createCodeBlockHighlighter()
