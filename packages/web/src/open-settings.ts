import type { ConnectorBlock } from '@ariadne/core'
import { shallowRef, type InjectionKey } from 'vue'
import type { SettingsTab } from './components/ModelSettings.vue'

/** Open the app's settings at a tab, from anywhere on the page. */
export const openSettingsKey: InjectionKey<(tab?: SettingsTab) => void> = Symbol('openSettings')

/**
 * A server an agent suggested, waiting for the MCP settings to open with it filled in;
 * once saved, it is turned on for `projectId`.
 */
export const suggestedServer = shallowRef<{ block: ConnectorBlock; projectId: string } | null>(null)

/** An id for a suggested server that the settings take and no saved server has. */
export function serverIdFor(name: string, taken: ReadonlySet<string>): string {
  const base =
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 16) || 'server'
  let id = base
  for (let n = 2; taken.has(id); n++) id = `${base}-${n}`
  return id
}

/** Whether a saved server is the one suggested: the same program and arguments, or address. */
export function sameServer(
  block: ConnectorBlock,
  server: { transport: string; command?: string; args?: string[]; url?: string },
): boolean {
  if (block.transport !== server.transport) return false
  return block.transport === 'http'
    ? block.url === server.url
    : block.command === server.command &&
        JSON.stringify(block.args) === JSON.stringify(server.args ?? [])
}
