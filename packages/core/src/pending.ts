/**
 * A placeholder: what an agent says will be at a place before it writes it, so the user
 * sees the shape of the whole early. In a document it is a block of its own, written in
 * Markdown as `<pending>what goes here</pending>` on a line by itself; on a whiteboard a
 * node of type `pending` (`data.text`); in a dashboard a widget of type `pending`
 * (`props.text`). The agent replaces each with the real thing as it gets to it.
 */
export const PENDING = 'pending'

/**
 * The document block, made with the parts of `@dou-ui/editor` given: for the editor on the page and for reading and writing Markdown on
 * the server: both must know it, or a placeholder would turn into a plain paragraph.
 */
export function pendingBlock<T>(kit: {
  defineEditorExtension: (extension: never) => T
  textBlock: (type: string) => unknown
  blockElement: (doc: Document, type: string, content: Node[]) => HTMLElement
}): T {
  return kit.defineEditorExtension({
    name: 'ariadne-pending',
    blocks: { [PENDING]: kit.textBlock(PENDING) },
    output: {
      blocks: {
        [PENDING]: {
          html: (_block: unknown, { content }: { content: string }) =>
            `<p><${PENDING}>${content}</${PENDING}></p>`,
          markdown: (_block: unknown, { content }: { content: string }) =>
            `<${PENDING}>${content}</${PENDING}>`,
        },
      },
    },
    // Markdown reads the tag as part of a paragraph: each becomes a block before it.
    prepareHTML: (root: HTMLElement) => {
      for (const element of Array.from(root.querySelectorAll(PENDING))) {
        const block = kit.blockElement(element.ownerDocument, PENDING, [...element.childNodes])
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
  } as never)
}
