import { PluginHost } from '@ariadne/agent'
import anthropic from '@ariadne/plugin-anthropic'
import { SettingsStore } from '../settings'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

Date.now = () => 1_800_000_000_000

const cases = (await Bun.stdin.json()) as {
  initial?: unknown
  actions: { op: string; value?: unknown; project?: string; reference?: string }[]
}[]
const output: unknown[] = []
for (const test of cases) {
  const dir = mkdtempSync(join(tmpdir(), 'ariadne-go-settings-oracle-'))
  try {
    const path = join(dir, 'config.json')
    if (test.initial !== undefined) writeFileSync(path, JSON.stringify(test.initial))
    const host = new PluginHost(dir)
    await host.load(anthropic)
    const store = new SettingsStore(path, host)
    const steps: unknown[] = [{ config: structuredClone(store.current) }]
    for (const action of test.actions) {
      try {
        let result: unknown
        switch (action.op) {
          case 'models':
            result = store.updateModels(action.value)
            break
          case 'resolve': {
            const { role, given, triage, contextTokens } = action.value as {
              role: 'thread' | 'coordinator' | 'triage' | 'research'
              given?: string
              triage?: boolean
              contextTokens?: number
            }
            const model =
              contextTokens === undefined
                ? store.resolveModel(role, given)
                : store.resolveCoordinator({ triage: !!triage, contextTokens })
            const stable = (value: typeof model): unknown =>
              value
                ? {
                    ...value,
                    provider: value.provider.kind,
                    ...(value.fallback ? { fallback: stable(value.fallback) } : {}),
                  }
                : null
            result = stable(model)
            break
          }
          case 'choices':
            result = store.modelChoices()
            break
          case 'useFound': {
            const { setup, model, info } = action.value as {
              setup: Parameters<SettingsStore['useFound']>[0]
              model: string
              info?: Parameters<SettingsStore['useFound']>[2]
            }
            result = store.useFound(setup, model, info)
            break
          }
          case 'concurrency':
            result = store.updateConcurrency(action.value)
            break
          case 'input':
            result = store.updateInput(action.value)
            break
          case 'work':
            result = store.updateWork(action.value)
            break
          case 'add':
            result = store.addInstruction(String(action.value))
            break
          case 'remove':
            result = store.removeInstruction(String(action.value))
            break
          case 'search':
            result = store.updateSearch(action.value)
            break
          case 'env':
            result = store.updateProjectEnv(action.project!, action.value)
            break
          case 'mcp':
            result = store.updateMcp(action.value)
            break
          case 'source':
            result = store.saveSourceSecrets(
              action.project!,
              action.value as Record<string, string>,
            )
            break
          case 'reference':
            result = store.saveReferenceSecrets(
              action.project!,
              action.reference!,
              action.value as Record<string, string>,
            )
            break
          case 'forget':
            result = store.forgetProject(action.project!)
            break
          default:
            throw new Error(`unknown action ${action.op}`)
        }
        steps.push({ result: result ?? null, config: structuredClone(store.current) })
      } catch (error) {
        const localized = (error as { toLocalized?: () => unknown }).toLocalized?.()
        steps.push({ error: localized ?? String(error), config: structuredClone(store.current) })
      }
    }
    output.push(steps)
  } finally {
    rmSync(dir, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 })
  }
}
console.info(JSON.stringify(output))
