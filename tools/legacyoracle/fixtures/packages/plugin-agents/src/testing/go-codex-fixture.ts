import type { ChatMessage, ModelStreamPart, ToolSpec } from '@ariadne/plugin-sdk'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { ToolBridge } from '../bridge'
import { codex } from '../codex'
import { claudeCode } from '../claude-code'
import { acp } from '../acp'
import { agentProvider } from '../sessions'

const { command, kind } = (await Bun.stdin.json()) as { command: string; kind?: string }
const dataDir = mkdtempSync(join(tmpdir(), 'ariadne-go-codex-oracle-'))
const bridge = new ToolBridge()
const provider = agentProvider(kind === 'claude-code' ? claudeCode : kind === 'acp' ? acp : codex, {
  dataDir,
  bridge,
})
const tools: ToolSpec[] = [
  {
    name: 'lookup',
    description: 'Look up a word',
    parameters: { type: 'object', properties: { key: { type: 'string' } } },
  },
]
const messages: ChatMessage[] = [{ role: 'system', content: 'Initial instructions' }]
const steps: unknown[] = []
try {
  for (const [index, prompt] of ['hello', 'CALL lookup {"key":"中文"}', 'last'].entries()) {
    if (index === 2) messages[0] = { role: 'system', content: 'Changed instructions' }
    messages.push({ role: 'user', content: prompt })
    for (let attempt = 0; attempt < 5; attempt++) {
      const parts: ModelStreamPart[] = []
      for await (const part of provider.stream(
        { command },
        {
          model: 'default',
          conversation: 'same',
          messages,
          tools,
          effort: index === 0 ? 'low' : 'high',
        },
      )) {
        if (part.type === 'alive') continue
        if (part.type === 'reasoning') {
          const { startedAt: _, ...stable } = part
          parts.push(stable)
        } else parts.push(part)
      }
      const calls = parts.filter((part) => part.type === 'tool-call')
      steps.push(
        parts.map((part) =>
          part.type === 'tool-call' ? { ...part, id: `call_${calls.indexOf(part)}` } : part,
        ),
      )
      const content = parts
        .filter((part) => part.type === 'text')
        .map((part) => part.text)
        .join('')
      if (calls.length) {
        messages.push({
          role: 'assistant',
          content,
          toolCalls: calls.map(({ id, name, arguments: args }) => ({ id, name, arguments: args })),
        })
        for (const call of calls)
          messages.push({
            role: 'tool',
            toolCallId: call.id,
            content: '返回的内容',
            images: [{ mime: 'image/png', data: 'AAAA' }],
          })
      } else {
        messages.push({ role: 'assistant', content })
        break
      }
    }
  }
  if (!provider.models) throw new Error('agent model listing is unavailable')
  const models = await provider.models({ command })
  console.info(JSON.stringify({ steps, models }))
} finally {
  await provider.dispose()
  bridge.stop()
  rmSync(dataDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 })
}
