import {
  fingerprint,
  nativeFingerprint,
  renderHistory,
  joinInput,
  systemHash,
  updatedInstructions,
} from '../history'
import type { ChatMessage } from '@ariadne/plugin-sdk'

const input = (await Bun.stdin.json()) as {
  messages: ChatMessage[]
  prefix: string | null
  system: string
}[]
console.info(
  JSON.stringify(
    input.map(({ messages, prefix, system }) => ({
      fps: messages.map(fingerprint),
      native: messages.map(nativeFingerprint),
      render: renderHistory(messages),
      join: joinInput(messages, prefix),
      system: systemHash(system),
      updated: updatedInstructions(system),
    })),
  ),
)
