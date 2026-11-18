import type { TranscriptEntry, TranscriptNote } from '@ariadne/core'
import { describeNote, toChatMessages, withImageData } from '../transcript'

const input = (await Bun.stdin.json()) as {
  entries: TranscriptEntry[]
  notes: TranscriptNote[]
  files: Record<string, string>
  options: {
    images?: boolean
    clearUpTo?: number
    promptUpdates?: boolean
    keep?: string[]
  }[]
}
const titleOf = (id: string) => `title:${id}`
console.info(
  JSON.stringify({
    notes: input.notes.map((note) => describeNote(note, titleOf)),
    chats: input.options.map(({ keep, ...options }) => {
      const messages = toChatMessages(input.entries, titleOf, {
        ...options,
        readText: (id) => input.files[id] ?? null,
        ...(keep ? { keepReasoning: (hash: string) => keep.includes(hash) } : {}),
      })
      return {
        messages,
        loaded: withImageData(messages, (file) =>
          file.fileId === 'fil_missing' ? null : { mime: file.mime, data: file.fileId },
        ),
      }
    }),
  }),
)
