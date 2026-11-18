import { openAICompatibleProvider } from '@ariadne/plugin-openai-compatible'
import { anthropicProvider } from '@ariadne/plugin-anthropic'
import { claudeCode, codex, acp, ToolBridge } from '@ariadne/plugin-agents'
import { agentProvider } from '../../../plugin-agents/src/sessions'
import type { ModelRequest, ModelProvider } from '@ariadne/plugin-sdk'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

const input = (await Bun.stdin.json()) as {
  env: Record<string, string>
  home: string
  available: string[]
  request: ModelRequest
  settings: Record<string, unknown>
}
const dataDir = mkdtempSync(join(tmpdir(), 'ariadne-go-model-host-oracle-'))
const bridge = new ToolBridge()
const programs = [claudeCode, codex, acp].map((kind) => agentProvider(kind, { dataDir, bridge }))
const providers = [openAICompatibleProvider, anthropicProvider, ...programs] as ModelProvider<
  Record<string, unknown>
>[]
try {
  const result = providers.map((provider) => ({
    info: {
      kind: provider.kind,
      title: provider.title,
      settings: provider.settings,
      ...(provider.program ? { program: true } : {}),
    },
    found:
      provider.find?.({
        env: input.env,
        home: input.home,
        which: (program) => (input.available.includes(program) ? program : null),
      }) ?? [],
    describe: provider.requestBody?.(input.settings, input.request),
  }))
  console.info(JSON.stringify(result))
} finally {
  await Promise.all(programs.map((provider) => provider.dispose()))
  bridge.stop()
  rmSync(dataDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 })
}
