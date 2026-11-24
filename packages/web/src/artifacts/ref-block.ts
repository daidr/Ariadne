import { defineBlock, type EditorExtension } from '@dou-ui/editor'
import {
  DEFAULT_REF_PROPS,
  normalizeRefProps,
  serializeRefPayload,
  type RefBlockProps,
} from './document-block-data'
import RefBlockView from './RefBlockView.vue'

export type { RefBlockProps } from './document-block-data'
export { normalizeRefProps, serializeRefPayload } from './document-block-data'

function escapeHTML(text: string): string {
  return text
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}

function parseRefJson(raw: string): RefBlockProps | null {
  try {
    const parsed = JSON.parse(raw.trim())
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    return normalizeRefProps(parsed as Record<string, unknown>)
  } catch {
    return null
  }
}

function parseRefTag(element: Element): RefBlockProps | null {
  const body = (element.textContent ?? '').trim()
  if (body.startsWith('{')) {
    const parsed = parseRefJson(body)
    if (parsed) return parsed
  }
  return normalizeRefProps({
    artifactId:
      element.getAttribute('artifact') ??
      element.getAttribute('artifact-id') ??
      element.getAttribute('artifactid') ??
      '',
    groupId:
      element.getAttribute('group') ??
      element.getAttribute('group-id') ??
      element.getAttribute('groupid') ??
      '',
    optionId:
      element.getAttribute('option') ??
      element.getAttribute('option-id') ??
      element.getAttribute('optionid') ??
      '',
    label: element.getAttribute('label') ?? body,
  })
}

function createRefElement(doc: Document, props: RefBlockProps): HTMLElement {
  const element = doc.createElement('div')
  element.dataset.contentType = 'ref'
  for (const [key, value] of Object.entries(props)) {
    element.setAttribute(`data-prop-${key}`, JSON.stringify(value))
  }
  const payload = serializeRefPayload(props as unknown as Record<string, unknown>)
  if (payload) element.setAttribute('data-prop-ref', JSON.stringify(payload))
  return element
}

function attrsFromDom(dom: HTMLElement): Record<string, unknown> {
  const blob = dom.getAttribute('data-prop-ref')
  if (blob) {
    const parsed = parseRefJson(blob)
    if (parsed) return { ...parsed }
  }
  const collected: Record<string, unknown> = {}
  for (const key of Object.keys(DEFAULT_REF_PROPS)) {
    const raw = dom.getAttribute(`data-prop-${key}`)
    if (raw === null) continue
    try {
      collected[key] = JSON.parse(raw)
    } catch {
      // Ignore malformed prop.
    }
  }
  const normalized = normalizeRefProps(collected)
  return normalized ? { ...normalized } : { ...DEFAULT_REF_PROPS, ...collected }
}

const baseRefBlock = defineBlock({
  type: 'ref',
  props: { ...DEFAULT_REF_PROPS },
  content: 'none',
  component: RefBlockView,
  spec: {
    parseDOM: [
      {
        tag: 'div[data-content-type="ref"]',
        priority: 70,
        getAttrs: (dom) => attrsFromDom(dom as HTMLElement),
      },
    ],
  },
})

/**
 * A live cross-artifact reference card inside a document. Shows the current title, version,
 * and live decision/verification status of a referenced artifact or whiteboard node, and jumps
 * directly to it on click.
 */
export const refDocumentBlock: EditorExtension = {
  ...baseRefBlock,
  name: 'ariadne-ref',
  prepareHTML: (root) => {
    for (const code of Array.from(root.querySelectorAll('pre > code.language-ref'))) {
      const parsed = parseRefJson(code.textContent ?? '')
      if (!parsed || !code.parentElement) continue
      code.parentElement.replaceWith(createRefElement(root.ownerDocument, parsed))
    }
    for (const element of Array.from(root.querySelectorAll('ref'))) {
      const parsed = parseRefTag(element)
      if (!parsed) continue
      const block = createRefElement(element.ownerDocument, parsed)
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
      ref: {
        html: (block, { children }) => {
          const payload = serializeRefPayload(block.props)
          if (!payload) return children
          const encoded = escapeHTML(JSON.stringify(payload))
          const label = escapeHTML(String(payload.label ?? payload.artifactId ?? ''))
          return `<div data-content-type="ref" data-prop-ref="${encoded}">${label}</div>${children}`
        },
        markdown: (block, { children }) => {
          const payload = serializeRefPayload(block.props)
          if (!payload) return children
          const written = `\`\`\`ref\n${JSON.stringify(payload)}\n\`\`\``
          return children ? `${written}\n\n${children}` : written
        },
      },
    },
  },
}
