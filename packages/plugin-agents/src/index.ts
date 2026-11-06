import { definePlugin, type ModelProvider } from '@ariadne/plugin-sdk'
import { acp } from './acp'
import { ToolBridge } from './bridge'
import { claudeCode } from './claude-code'
import { codex } from './codex'
import { agentProvider, type AgentKind } from './sessions'

export { acp, forOwnTools } from './acp'
export { ToolBridge, SERVER_NAME, type ToolOutput } from './bridge'
export { claudeCode } from './claude-code'
export { codex } from './codex'
export { fingerprint, renderHistory } from './history'
export { splitCommand } from './process'
export { agentProvider, QUIET_MS, type AgentKind, type AgentProvider } from './sessions'

/**
 * Agent programs as model providers: Claude Code, Codex and any agent that speaks the
 * Agent Client Protocol. They run with the accounts they are signed in with, and work only
 * through the conversation's tools.
 */
export default definePlugin({
  id: 'agents',
  name: { 'zh-CN': '外部 agent', en: 'Agent programs' },
  setup(ctx) {
    const bridge = new ToolBridge()
    const providers = ([claudeCode, codex, acp] as AgentKind<never>[]).map((agent) =>
      agentProvider(agent, { dataDir: ctx.dataDir, sandbox: ctx.sandbox, bridge }),
    )
    for (const provider of providers) ctx.registerModelProvider(provider as ModelProvider<never>)
    return async () => {
      await Promise.all(providers.map((provider) => provider.dispose()))
      bridge.stop()
    }
  },
})
