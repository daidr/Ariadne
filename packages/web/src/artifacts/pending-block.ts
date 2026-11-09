import { blockElement, defineEditorExtension, textBlock } from '@dou-ui/editor'
import './pending-block.css'

/**
 * A placeholder in a document: what an agent will write at this place, shown in grey.
 * The server reads and writes the same block (`pendingBlock` in `@ariadne/core`, which the
 * page does not load code from); a test keeps the two reading alike.
 */
export const pendingDocumentBlock = defineEditorExtension({
  name: 'ariadne-pending',
  blocks: { pending: textBlock('pending') },
  output: {
    blocks: {
      pending: {
        html: (_block, { content }) => `<p><pending>${content}</pending></p>`,
        markdown: (_block, { content }) => `<pending>${content}</pending>`,
      },
    },
  },
  // Markdown reads the tag as part of a paragraph: each becomes a block before it.
  prepareHTML: (root) => {
    for (const element of Array.from(root.querySelectorAll('pending'))) {
      const block = blockElement(element.ownerDocument, 'pending', [...element.childNodes])
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
})
