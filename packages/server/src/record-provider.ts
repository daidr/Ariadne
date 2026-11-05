/**
 * Record model service samples for the replay tests, with a model from Ariadne's own
 * settings (its key never leaves the settings, and is never written to a sample):
 *
 *   bun run record:provider                    lists the models in the settings
 *   bun run record:provider <model id> [set]   records every scenario with that model
 *
 * `set` names the folder of samples (by default the model id), e.g. `deepseek-reasoner`.
 * `--only text,tool-call` records only those scenarios. The samples go to
 * packages/server/fixtures/providers/<set>/ and are checked in.
 */
import { noSandbox, PluginHost } from '@ariadne/agent'
import anthropic from '@ariadne/plugin-anthropic'
import openAICompatible from '@ariadne/plugin-openai-compatible'
import type { ModelProvider } from '@ariadne/plugin-sdk'
import { join, relative } from 'node:path'
import { parseArgs } from 'node:util'
import { loadConfig } from './config'
import {
  FIXTURES_DIR,
  record,
  saveFixtures,
  SCENARIOS,
  type ScenarioName,
} from './provider-fixtures'
import { SettingsStore } from './settings'

const { values, positionals } = parseArgs({
  args: process.argv.slice(2),
  options: { only: { type: 'string' } },
  allowPositionals: true,
})

const config = loadConfig()
const host = new PluginHost(join(config.home, 'plugins'), noSandbox)
await host.load(openAICompatible)
await host.load(anthropic)
const settings = new SettingsStore(join(config.home, 'config.json'), host)
const models = settings.current.models.models

const [modelId, set = modelId] = positionals
if (!modelId) {
  console.info(
    models.length
      ? `Models in the settings:\n${models.map((m) => `  ${m.id}  (${m.model})`).join('\n')}\n\nbun run record:provider <model id> [set]`
      : 'No models in the settings yet: add one in Ariadne first.',
  )
  process.exit(models.length ? 0 : 1)
}

const resolved = settings.resolveModel('thread', modelId)
if (!resolved || !models.some((m) => m.id === modelId)) {
  console.error(`No model "${modelId}" in the settings.`)
  process.exit(1)
}
const known = new Set(SCENARIOS.map((s) => s.name))
const only = values.only?.split(',').map((name) => name.trim()) as ScenarioName[] | undefined
for (const name of only ?? []) {
  if (!known.has(name)) {
    console.error(`No scenario "${name}"; there are ${[...known].join(', ')}.`)
    process.exit(1)
  }
}

console.info(`Recording ${resolved.label} as "${set}"…`)
let failed = 0
const fixtures = await record({
  provider: resolved.provider as ModelProvider<Record<string, unknown>>,
  settings: resolved.settings as Record<string, unknown>,
  model: resolved.model,
  set: set!,
  ...(only ? { scenarios: only } : {}),
  extra: {
    ...(resolved.request?.headers ? { headers: resolved.request.headers } : {}),
    ...(resolved.request?.extraBody ? { extraBody: resolved.request.extraBody } : {}),
  },
  onScenario: (fixture, problems) => {
    if (problems.length) failed++
    console.info(
      `  ${problems.length ? '✗' : '✓'} ${fixture.scenario} (HTTP ${fixture.reply.status})${problems.length ? `: ${problems.join('; ')}` : ''}`,
    )
  },
})
for (const file of saveFixtures(FIXTURES_DIR, fixtures)) {
  console.info(`  → ${relative(process.cwd(), file)}`)
}
if (failed) {
  console.warn(
    `${failed} scenario(s) did not go as expected. The samples are saved anyway: the replay test fails on them until the provider handles that reply.`,
  )
}
