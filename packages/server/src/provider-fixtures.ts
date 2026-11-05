import { CUT_HINT } from '@ariadne/agent'
import type {
  ChatMessage,
  ModelProvider,
  ModelRequest,
  ModelStreamPart,
  SettingsSchema,
  ToolCall,
} from '@ariadne/plugin-sdk'
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * Model service samples: real requests to a real service, recorded once with
 * `bun run record:provider`, and replayed in the tests without a network or a key. They
 * catch a protocol change on our side that the service would refuse, or a reply shape of
 * the service (a reasoning model's, a cut-off answer's) that the provider no longer reads.
 */

/** Where the samples live: one folder per sample set, e.g. `deepseek-reasoner`. */
export const FIXTURES_DIR = join(import.meta.dirname, '../fixtures/providers')

/** One request and the service's reply to it, byte for byte. */
export interface ProviderFixture {
  /** The provider kind, e.g. `openai-compatible`. */
  provider: string
  /** The sample set, named by whoever recorded it, e.g. `deepseek-reasoner`. */
  set: string
  scenario: ScenarioName
  recordedAt: string
  /** The provider's settings, without the secret ones. */
  settings: Record<string, unknown>
  request: Omit<ModelRequest, 'signal' | 'headers'>
  /** What went over the wire, without credentials, for reading. */
  sent: { path: string; body: unknown }
  reply: { status: number; contentType: string; retryAfter: string | null; body: string }
  /** What the provider made of the reply when it was recorded. */
  parts: ModelStreamPart[]
}

export type ScenarioName =
  'text' | 'tool-call' | 'tool-result' | 'effort' | 'cut-off' | 'cut-off-continue' | 'bad-model'

const WEATHER_TOOL = {
  name: 'get_weather',
  description: 'The weather in a city now.',
  parameters: {
    type: 'object',
    properties: { city: { type: 'string', description: 'City name, e.g. 杭州' } },
    required: ['city'],
  },
}

const SYSTEM = 'You are a test of a model service. Follow the instruction exactly.'

/** What came back, as the next request sends it: the assistant's message of that step. */
export function assistantOf(parts: ModelStreamPart[]): ChatMessage {
  let content = ''
  const toolCalls: ToolCall[] = []
  const reasoning: { text: string; signature?: string; redacted?: boolean }[] = []
  for (const part of parts) {
    if (part.type === 'text') content += part.text
    if (part.type === 'tool-call') {
      toolCalls.push({ id: part.id, name: part.name, arguments: part.arguments })
    }
    if (part.type === 'reasoning') {
      const { type: _type, startedAt: _startedAt, ...block } = part
      reasoning.push(block)
    }
  }
  return {
    role: 'assistant',
    content,
    ...(toolCalls.length ? { toolCalls } : {}),
    ...(reasoning.length ? { reasoning } : {}),
  }
}

/** What a scenario checks of the parts the provider made of the reply. */
export interface Expectation {
  finish: Array<'stop' | 'length' | 'tool-calls' | 'error'>
  toolCall?: boolean
  retryable?: boolean
}

interface Scenario {
  name: ScenarioName
  /** The scenario whose reply this one goes on from. */
  after?: ScenarioName
  build(model: string, previous: ProviderFixture | null): Omit<ModelRequest, 'signal'>
  expect: Expectation
}

export const SCENARIOS: Scenario[] = [
  {
    name: 'text',
    build: (model) => ({
      model,
      messages: [
        { role: 'system', content: SYSTEM },
        { role: 'user', content: '用一句话回答：一年有几个季节？' },
      ],
      tools: [],
    }),
    expect: { finish: ['stop'] },
  },
  {
    name: 'tool-call',
    build: (model) => ({
      model,
      messages: [
        { role: 'system', content: SYSTEM },
        { role: 'user', content: '杭州现在天气怎么样？用 get_weather 查。' },
      ],
      tools: [WEATHER_TOOL],
    }),
    expect: { finish: ['tool-calls'], toolCall: true },
  },
  {
    // The step after a tool call, with what the model sent back in it (for a reasoning
    // model, its reasoning: some services refuse the request without it).
    name: 'tool-result',
    after: 'tool-call',
    build: (model, previous) => {
      const assistant = assistantOf(previous!.parts)
      const call = assistant.role === 'assistant' ? assistant.toolCalls?.[0] : undefined
      return {
        model,
        messages: [
          ...previous!.request.messages,
          assistant,
          {
            role: 'tool',
            toolCallId: call?.id ?? 'missing',
            content: '{"city":"杭州","weather":"小雨","celsius":18}',
          },
        ],
        tools: [WEATHER_TOOL],
      }
    },
    expect: { finish: ['stop'] },
  },
  {
    name: 'effort',
    build: (model) => ({
      model,
      messages: [
        { role: 'system', content: SYSTEM },
        { role: 'user', content: '17 × 23 等于多少？只回答数字。' },
      ],
      tools: [],
      effort: 'high',
    }),
    expect: { finish: ['stop'] },
  },
  {
    name: 'cut-off',
    build: (model) => ({
      model,
      messages: [
        { role: 'system', content: SYSTEM },
        { role: 'user', content: '从 1 数到 300，用逗号隔开。' },
      ],
      tools: [],
      // Enough for a reasoning model to start writing, too little for the whole answer.
      maxTokens: 64,
    }),
    expect: { finish: ['length'] },
  },
  {
    // How the agent loop goes on after a cut-off reply: what was written, then the hint.
    name: 'cut-off-continue',
    after: 'cut-off',
    build: (model, previous) => ({
      model,
      messages: [
        ...previous!.request.messages,
        assistantOf(previous!.parts),
        { role: 'user', content: CUT_HINT },
      ],
      tools: [],
      maxTokens: 64,
    }),
    expect: { finish: ['stop', 'length'] },
  },
  {
    name: 'bad-model',
    build: () => ({
      model: 'ariadne-no-such-model',
      messages: [{ role: 'user', content: 'hi' }],
      tools: [],
    }),
    expect: { finish: ['error'], retryable: false },
  },
]

/** What a scenario's parts must show; empty when they do. */
export function check(fixture: ProviderFixture): string[] {
  const scenario = SCENARIOS.find((s) => s.name === fixture.scenario)
  if (!scenario) return [`unknown scenario ${fixture.scenario}`]
  const problems: string[] = []
  const finish = fixture.parts.findLast((part) => part.type === 'finish')
  if (!finish || finish.type !== 'finish') problems.push('no finish part')
  else {
    if (!scenario.expect.finish.includes(finish.reason)) {
      problems.push(`finished with ${finish.reason}${finish.error ? ` (${finish.error})` : ''}`)
    }
    if (scenario.expect.retryable === false && finish.retryable) problems.push('marked retryable')
  }
  const calls = fixture.parts.filter((part) => part.type === 'tool-call')
  if (scenario.expect.toolCall && !calls.length) problems.push('no tool call')
  for (const call of calls) {
    try {
      JSON.parse(call.type === 'tool-call' ? call.arguments : '')
    } catch {
      problems.push(`tool call arguments are not JSON`)
    }
  }
  if (finish?.type === 'finish' && finish.reason !== 'error') {
    if (!fixture.parts.some((part) => part.type === 'usage')) problems.push('no usage')
  }
  return problems
}

/** Parts as a fixture keeps them: without the keep-alives and the clock. */
export function comparable(parts: ModelStreamPart[]): ModelStreamPart[] {
  return parts
    .filter((part) => part.type !== 'alive')
    .map((part) => {
      if (part.type !== 'reasoning') return part
      const { startedAt: _startedAt, ...rest } = part
      return rest
    })
}

/** The settings without the secret fields (an API key), as a fixture keeps them. */
export function publicSettings(
  schema: SettingsSchema,
  settings: Record<string, unknown>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(settings).filter(([key]) => {
      const field = schema.properties[key]
      return !(field && 'secret' in field && field.secret)
    }),
  )
}

/**
 * Record every scenario against a service. `settings` are the provider's settings for the
 * service, with its `baseUrl`; requests go through a local proxy that keeps a copy of what
 * went each way.
 */
export async function record(options: {
  provider: ModelProvider<Record<string, unknown>>
  settings: Record<string, unknown>
  model: string
  set: string
  scenarios?: ScenarioName[]
  /** Added to every request, as the user's model settings do. */
  extra?: Pick<ModelRequest, 'headers' | 'extraBody'>
  onScenario?: (fixture: ProviderFixture, problems: string[]) => void
}): Promise<ProviderFixture[]> {
  const upstream = String(options.settings.baseUrl ?? '').replace(/\/+$/, '')
  if (!upstream) throw new Error('the provider settings have no baseUrl')
  type Exchange = { path: string; body: unknown; reply: ProviderFixture['reply'] }
  let exchange: Exchange | null = null
  const proxy = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    idleTimeout: 0,
    async fetch(request) {
      const url = new URL(request.url)
      const text = await request.text()
      const headers = new Headers()
      for (const [key, value] of request.headers) {
        if (!/^(host|content-length|connection)$/i.test(key)) headers.set(key, value)
      }
      const response = await fetch(`${upstream}${url.pathname}${url.search}`, {
        method: request.method,
        headers,
        body: text,
      })
      const body = await response.text()
      let sentBody: unknown = text
      try {
        sentBody = JSON.parse(text)
      } catch {
        // Kept as text.
      }
      exchange = {
        path: url.pathname,
        body: sentBody,
        reply: {
          status: response.status,
          contentType: response.headers.get('content-type') ?? '',
          retryAfter: response.headers.get('retry-after'),
          body,
        },
      }
      return new Response(body, {
        status: response.status,
        headers: { 'content-type': exchange.reply.contentType },
      })
    },
  })
  // The path the provider asks for is kept apart from the base, e.g. `/v1` + `/chat/completions`.
  const base = new URL(upstream)
  const settings = {
    ...options.settings,
    baseUrl: `http://127.0.0.1:${proxy.port}${base.pathname}`,
  }
  const done = new Map<ScenarioName, ProviderFixture>()
  const wanted = new Set(options.scenarios ?? SCENARIOS.map((s) => s.name))
  try {
    for (const scenario of SCENARIOS) {
      if (!wanted.has(scenario.name)) continue
      const previous = scenario.after ? (done.get(scenario.after) ?? null) : null
      if (scenario.after && !previous) continue
      const request = scenario.build(options.model, previous)
      exchange = null
      const parts: ModelStreamPart[] = []
      for await (const part of options.provider.stream(settings, {
        ...request,
        ...(options.extra?.headers ? { headers: options.extra.headers } : {}),
        ...(options.extra?.extraBody
          ? { extraBody: { ...options.extra.extraBody, ...request.extraBody } }
          : {}),
      })) {
        parts.push(part)
      }
      const seen = exchange as Exchange | null
      if (!seen) throw new Error(`${scenario.name}: the provider sent no request`)
      const fixture: ProviderFixture = {
        provider: options.provider.kind,
        set: options.set,
        scenario: scenario.name,
        recordedAt: new Date().toISOString(),
        settings: publicSettings(options.provider.settings, options.settings),
        request: {
          ...request,
          ...(options.extra?.extraBody
            ? { extraBody: { ...options.extra.extraBody, ...request.extraBody } }
            : {}),
        },
        sent: { path: seen.path, body: seen.body },
        reply: seen.reply,
        parts: comparable(parts),
      }
      done.set(scenario.name, fixture)
      options.onScenario?.(fixture, check(fixture))
    }
  } finally {
    proxy.stop(true)
  }
  return [...done.values()]
}

export function fixtureFile(dir: string, fixture: Pick<ProviderFixture, 'set' | 'scenario'>) {
  return join(dir, fixture.set, `${fixture.scenario}.json`)
}

export function saveFixtures(dir: string, fixtures: ProviderFixture[]): string[] {
  return fixtures.map((fixture) => {
    const file = fixtureFile(dir, fixture)
    mkdirSync(join(dir, fixture.set), { recursive: true })
    writeFileSync(file, `${JSON.stringify(fixture, null, 2)}\n`)
    return file
  })
}

export function loadFixtures(dir: string): ProviderFixture[] {
  let sets: string[]
  try {
    sets = readdirSync(dir)
  } catch {
    return []
  }
  return sets.flatMap((set) => {
    let files: string[]
    try {
      files = readdirSync(join(dir, set)).filter((name) => name.endsWith('.json'))
    } catch {
      return []
    }
    return files
      .toSorted()
      .map((name) => JSON.parse(readFileSync(join(dir, set, name), 'utf8')) as ProviderFixture)
  })
}

/**
 * Play a recorded reply back to `provider`: the parts it makes of it now, and the request it
 * sends now, which must still be the one the service took.
 */
export async function replay(
  provider: ModelProvider<Record<string, unknown>>,
  fixture: ProviderFixture,
): Promise<{ parts: ModelStreamPart[]; path: string | null; body: unknown }> {
  let path: string | null = null
  let body: unknown = null
  const server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    async fetch(request) {
      path = new URL(request.url).pathname
      const text = await request.text()
      try {
        body = JSON.parse(text)
      } catch {
        body = text
      }
      const { reply } = fixture
      return new Response(reply.body, {
        status: reply.status,
        headers: {
          'content-type': reply.contentType,
          ...(reply.retryAfter ? { 'retry-after': reply.retryAfter } : {}),
        },
      })
    },
  })
  try {
    const recordedBase = new URL(String(fixture.settings.baseUrl ?? 'http://x/'))
    const settings = {
      ...fixture.settings,
      apiKey: 'replay',
      baseUrl: `http://127.0.0.1:${server.port}${recordedBase.pathname}`,
    }
    const parts: ModelStreamPart[] = []
    for await (const part of provider.stream(settings, fixture.request)) parts.push(part)
    return { parts: comparable(parts), path, body }
  } finally {
    server.stop(true)
  }
}
