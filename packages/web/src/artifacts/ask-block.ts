import { defineBlock, type EditorExtension } from '@dou-ui/editor'
import AskBlockView from './AskBlockView.vue'
import {
  DEFAULT_ASK_PROPS,
  normalizeAskProps,
  serializeAskPayload,
  type AskBlockProps,
} from './document-block-data'

export type { AskBlockOption, AskBlockProps } from './document-block-data'
export { normalizeAskProps, serializeAskPayload } from './document-block-data'

function escapeHTML(text: string): string {
  return text
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}

function parseAskJson(raw: string): AskBlockProps | null {
  try {
    const parsed = JSON.parse(raw.trim())
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    return normalizeAskProps(parsed as Record<string, unknown>)
  } catch {
    return null
  }
}

function parseAskTag(element: Element): AskBlockProps | null {
  const body = (element.textContent ?? '').trim()
  if (body.startsWith('{')) {
    const parsed = parseAskJson(body)
    if (parsed) return parsed
  }
  const question = (element.getAttribute('question') ?? '').trim() || body
  if (!question) return null
  const raw: Record<string, unknown> = {
    id: element.getAttribute('id') ?? '',
    question,
    description: element.getAttribute('description') ?? '',
    pick: element.getAttribute('pick') ?? '',
  }
  const opts = (element.getAttribute('options') ?? '').trim()
  if (opts) {
    try {
      const parsedOpts = JSON.parse(opts)
      if (Array.isArray(parsedOpts)) raw.options = parsedOpts
    } catch {
      // Ignore malformed attribute JSON.
    }
  }
  return normalizeAskProps(raw)
}

function createAskElement(doc: Document, props: AskBlockProps): HTMLElement {
  const element = doc.createElement('div')
  element.dataset.contentType = 'ask'
  for (const [key, value] of Object.entries(props)) {
    element.setAttribute(`data-prop-${key}`, JSON.stringify(value))
  }
  const payload = serializeAskPayload(props as unknown as Record<string, unknown>)
  if (payload) element.setAttribute('data-prop-ask', JSON.stringify(payload))
  return element
}

function attrsFromDom(dom: HTMLElement): Record<string, unknown> {
  const blob = dom.getAttribute('data-prop-ask')
  if (blob) {
    const parsed = parseAskJson(blob)
    if (parsed) return { ...parsed }
  }
  const collected: Record<string, unknown> = {}
  for (const key of Object.keys(DEFAULT_ASK_PROPS)) {
    const raw = dom.getAttribute(`data-prop-${key}`)
    if (raw === null) continue
    try {
      collected[key] = JSON.parse(raw)
    } catch {
      // Ignore malformed prop.
    }
  }
  const normalized = normalizeAskProps(collected)
  return normalized ? { ...normalized } : { ...DEFAULT_ASK_PROPS, ...collected }
}

const baseAskBlock = defineBlock({
  type: 'ask',
  props: { ...DEFAULT_ASK_PROPS },
  content: 'none',
  component: AskBlockView,
  spec: {
    parseDOM: [
      {
        tag: 'div[data-content-type="ask"]',
        priority: 70,
        getAttrs: (dom) => attrsFromDom(dom as HTMLElement),
      },
    ],
  },
})

/**
 * An inline question block inside a document. Agents insert it via ```ask JSON fences or
 * <ask> tags; users pick options or enter a note inline, and changes sync over Yjs.
 */
export const askDocumentBlock: EditorExtension = {
  ...baseAskBlock,
  name: 'ariadne-ask',
  prepareHTML: (root) => {
    for (const code of Array.from(root.querySelectorAll('pre > code.language-ask'))) {
      const parsed = parseAskJson(code.textContent ?? '')
      if (!parsed || !code.parentElement) continue
      code.parentElement.replaceWith(createAskElement(root.ownerDocument, parsed))
    }
    for (const element of Array.from(root.querySelectorAll('ask'))) {
      const parsed = parseAskTag(element)
      if (!parsed) continue
      const block = createAskElement(element.ownerDocument, parsed)
      const paragraph = element.parentElement
      if (paragraph?.tagName !== 'P') {
        element.replaceWith(block)
        continue
      }
      paragraph.before(block)
      element.remove()
      if (!paragraph.textContent?.trim() && !paragraph.querySelector('img')) paragraph.remove()
    }
  },
  output: {
    blocks: {
      ask: {
        html: (block, { children }) => {
          const payload = serializeAskPayload(block.props)
          if (!payload) return children
          const encoded = escapeHTML(JSON.stringify(payload))
          const question = escapeHTML(String(payload.question ?? ''))
          return `<div data-content-type="ask" data-prop-ask="${encoded}">${question}</div>${children}`
        },
        markdown: (block, { children }) => {
          const payload = serializeAskPayload(block.props)
          if (!payload) return children
          const written = `\`\`\`ask\n${JSON.stringify(payload)}\n\`\`\``
          return children ? `${written}\n\n${children}` : written
        },
      },
    },
  },
}
