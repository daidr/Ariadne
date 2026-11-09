import { pendingBlock } from '@ariadne/core'
import { blockElement, defineEditorExtension, textBlock } from '@dou-ui/editor'
import './pending-block.css'

/** A placeholder in a document: what an agent will write at this place, shown in grey. */
export const pendingDocumentBlock = pendingBlock({ defineEditorExtension, textBlock, blockElement })
