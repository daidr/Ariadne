import { anthropicProvider } from '@ariadne/plugin-anthropic'
import { openAICompatibleProvider } from '@ariadne/plugin-openai-compatible'
import type { ModelProvider } from '@ariadne/plugin-sdk'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, describe, expect, test } from 'vitest'
import {
  check,
  FIXTURES_DIR,
  fixtureFile,
  loadFixtures,
  record,
  replay,
  saveFixtures,
} from './provider-fixtures'

type AnyProvider = ModelProvider<Record<string, unknown>>
const openai = openAICompatibleProvider as unknown as AnyProvider
const providers = new Map<string, AnyProvider>(
  [openai, anthropicProvider as unknown as AnyProvider].map((provider) => [
    provider.kind,
    provider,
  ]),
)

// Samples recorded from real services; see record-provider.ts.
describe('recorded model service samples', () => {
  const fixtures = loadFixtures(FIXTURES_DIR)
  test.skipIf(fixtures.length > 0)('none recorded yet', () => {})
  for (const fixture of fixtures) {
    test(`${fixture.set} / ${fixture.scenario}`, async () => {
      const provider = providers.get(fixture.provider)
      expect(provider, `provider ${fixture.provider}`).toBeDefined()
      expect(check(fixture)).toEqual([])
      const { parts, path, body } = await replay(provider!, fixture)
      expect(path).toBe(fixture.sent.path)
      // What we send now is what the service took then (say, the reasoning sent back).
      expect(body).toEqual(fixture.sent.body)
      expect(parts).toEqual(fixture.parts)
    })
  }
})

/** A service in the OpenAI-compatible protocol that thinks out loud like DeepSeek's. */
const upstream = Bun.serve({
  port: 0,
  hostname: '127.0.0.1',
  async fetch(request) {
    const body = (await request.json()) as {
      model: string
      messages: { role: string }[]
      tools?: unknown[]
      max_tokens?: number
    }
    if (request.headers.get('authorization') !== 'Bearer sk-secret') {
      return new Response('{"error":{"message":"no key"}}', { status: 401 })
    }
    if (body.model === 'ariadne-no-such-model') {
      return Response.json({ error: { message: 'Model Not Exist' } }, { status: 400 })
    }
    const last = body.messages.at(-1)!.role
    const chunks: unknown[] = [{ choices: [{ delta: { reasoning_content: '想一想' } }] }]
    if (body.tools && last === 'user') {
      chunks.push({
        choices: [
          {
            delta: {
              tool_calls: [
                {
                  index: 0,
                  id: 'call_1',
                  function: { name: 'get_weather', arguments: '{"city":"杭州"}' },
                },
              ],
            },
            finish_reason: 'tool_calls',
          },
        ],
      })
    } else if (body.max_tokens && last === 'user' && body.messages.length < 4) {
      chunks.push({ choices: [{ delta: { content: '1, 2, 3' }, finish_reason: 'length' }] })
    } else {
      chunks.push({ choices: [{ delta: { content: '好的。' }, finish_reason: 'stop' }] })
    }
    chunks.push({ choices: [], usage: { prompt_tokens: 10, completion_tokens: 5 } })
    return new Response(
      [...chunks.map((c) => `data: ${JSON.stringify(c)}\n\n`), 'data: [DONE]\n\n'].join(''),
      { headers: { 'content-type': 'text/event-stream' } },
    )
  },
})
afterAll(() => upstream.stop(true))

describe('recording samples', () => {
  test('keeps every exchange without the key, and plays it back the same', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'ariadne-fixtures-'))
    try {
      const problems: Record<string, string[]> = {}
      const fixtures = await record({
        provider: openai,
        settings: { baseUrl: `http://127.0.0.1:${upstream.port}/v1`, apiKey: 'sk-secret' },
        model: 'fake-reasoner',
        set: 'fake',
        onScenario: (fixture, found) => (problems[fixture.scenario] = found),
      })
      expect(fixtures.map((f) => f.scenario)).toEqual([
        'text',
        'tool-call',
        'tool-result',
        'effort',
        'cut-off',
        'cut-off-continue',
        'bad-model',
      ])
      expect(Object.values(problems).flat()).toEqual([])
      // The reasoning of the call's step went back with the tool result.
      const next = fixtures.find((f) => f.scenario === 'tool-result')!
      expect(next.sent.body).toMatchObject({
        messages: expect.arrayContaining([
          expect.objectContaining({ role: 'assistant', reasoning_content: '想一想' }),
        ]),
      })
      expect(
        fixtures.find((f) => f.scenario === 'cut-off-continue')!.request.messages.at(-1),
      ).toMatchObject({ role: 'user', content: expect.stringContaining('<output_cut_off>') })

      saveFixtures(dir, fixtures)
      const text = readFileSync(fixtureFile(dir, fixtures[1]!), 'utf8')
      expect(text).not.toContain('sk-secret')
      const loaded = loadFixtures(dir)
      expect(loaded).toHaveLength(fixtures.length)
      for (const fixture of loaded) {
        const replayed = await replay(openai, fixture)
        expect(replayed.parts).toEqual(fixture.parts)
        expect(replayed.body).toEqual(fixture.sent.body)
      }
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })

  test('a reply the provider reads differently now fails the check', () => {
    const fixture = {
      scenario: 'tool-call',
      parts: [{ type: 'finish', reason: 'stop' }],
    } as never
    expect(check(fixture)).toEqual(['finished with stop', 'no tool call', 'no usage'])
  })
})
