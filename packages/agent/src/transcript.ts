import { unknownResult, type TranscriptEntry, type TranscriptNote } from '@ariadne/core'
import type { ChatMessage } from '@ariadne/plugin-sdk'

/** How a note reads to the model. Titles are looked up so the model sees names, not ids. */
export function describeNote(note: TranscriptNote, titleOf: (threadId: string) => string): string {
  switch (note.type) {
    case 'report':
      return `<thread_report thread="${note.threadId}" title="${titleOf(note.threadId)}" kind="${note.report}">\n${note.summary}\n</thread_report>`
    case 'answer': {
      const answer = [note.choice && `choice: ${note.choice}`, note.text && `text: ${note.text}`]
        .filter(Boolean)
        .join('\n')
      return `<user_answer attention="${note.attentionId}">\nquestion: ${note.question}\n${answer}\n</user_answer>`
    }
    case 'user-in-thread':
      return `<user_wrote_in_thread thread="${note.threadId}" title="${titleOf(note.threadId)}">\n${note.content}\n</user_wrote_in_thread>`
    case 'upstream-done':
      return `<upstream_done thread="${note.threadId}" node="${note.nodeTitle}">\n${note.summary}\n</upstream_done>`
    case 'artifact-edited':
      return `<document_edited_by_user artifact="${note.artifactId}" title="${note.title}" version="${note.version}" />`
    case 'plan-changed-by-user':
      return `<plan_changed_by_user revision="${note.revisionId}"${note.revertOf ? ` reverts="${note.revertOf}"` : ''}>\n${note.reason}\n</plan_changed_by_user>`
  }
}

/**
 * Order entries as the model experienced them. A step goes right after the newest event
 * it had seen (`sawSeq`), so input that arrived while the model was answering comes after
 * that answer, not before it, even though it was stored first.
 */
const orderKey = (entry: TranscriptEntry) =>
  entry.kind === 'step' ? entry.sawSeq + 0.5 : entry.seq

function inModelOrder(entries: TranscriptEntry[]): TranscriptEntry[] {
  return entries.toSorted((a, b) => orderKey(a) - orderKey(b))
}

/**
 * Turn a transcript into model messages. Both protocols need every tool call answered
 * right after the step that made it, so input that arrived while tools ran is held back
 * until the step's results are in, and a call without a result gets one.
 */
export function toChatMessages(
  entries: TranscriptEntry[],
  titleOf: (threadId: string) => string,
): ChatMessage[] {
  const out: ChatMessage[] = []
  let open: { ids: string[]; names: Map<string, string> } | null = null
  let held: ChatMessage[] = []
  const results = new Map<string, Extract<TranscriptEntry, { kind: 'tool' }>>()
  for (const entry of entries) if (entry.kind === 'tool') results.set(entry.toolCallId, entry)

  const closeStep = () => {
    if (!open) return
    for (const id of open.ids) {
      const result = results.get(id)
      out.push({
        role: 'tool',
        toolCallId: id,
        content: result?.content ?? unknownResult('missing result'),
        ...(result ? (result.isError ? { isError: true } : {}) : { isError: true }),
      })
    }
    open = null
    out.push(...held)
    held = []
  }

  for (const entry of inModelOrder(entries)) {
    switch (entry.kind) {
      case 'step':
        closeStep()
        out.push({
          role: 'assistant',
          content: entry.text,
          ...(entry.toolCalls.length ? { toolCalls: entry.toolCalls } : {}),
        })
        if (entry.toolCalls.length) {
          open = {
            ids: entry.toolCalls.map((call) => call.id),
            names: new Map(entry.toolCalls.map((call) => [call.id, call.name])),
          }
        }
        break
      case 'tool':
        // Emitted with its step, in call order.
        break
      case 'message': {
        const { message } = entry
        const content =
          message.kind === 'relay'
            ? `<message_from_coordinator>\n${message.content}\n</message_from_coordinator>`
            : message.content
        const chat: ChatMessage = { role: 'user', content }
        if (open) held.push(chat)
        else out.push(chat)
        break
      }
      case 'note': {
        const chat: ChatMessage = { role: 'user', content: describeNote(entry.note, titleOf) }
        if (open) held.push(chat)
        else out.push(chat)
        break
      }
    }
  }
  closeStep()
  return out
}
