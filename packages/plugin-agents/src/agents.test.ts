import type { ChatMessage, ModelStreamPart, ToolSpec } from '@ariadne/plugin-sdk'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { acp, acpModels, forOwnTools } from './acp'
import { ToolBridge } from './bridge'
import { claudeCode, claudeModels } from './claude-code'
import { codex } from './codex'
import { fingerprint, renderHistory } from './history'
import { splitCommand } from './process'
import { agentProvider, type AgentKind, type AgentProvider } from './sessions'

const fake = (name: string) =>
  `${process.execPath} ${join(import.meta.dirname, 'fakes', `fake-${name}.ts`)}`

const TOOLS: ToolSpec[] = [
  {
    name: 'lookup',
    description: 'Look a word up',
    parameters: { type: 'object', properties: { key: { type: 'string' } } },
  },
]

let dir: string
let bridge: ToolBridge
let providers: AgentProvider<unknown>[] = []

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'ariadne-agents-'))
  bridge = new ToolBridge()
})

afterEach(async () => {
  await Promise.all(providers.map((p) => p.dispose()))
  providers = []
  bridge.stop()
  rmSync(dir, { recursive: true, force: true })
})

function provider(agent: AgentKind<never>) {
  const made = agentProvider(agent as AgentKind<unknown>, {
    dataDir: dir,
    bridge,
  })
  providers.push(made)
  return made
}

interface Step {
  text: string
  calls: { id: string; name: string; arguments: string }[]
  usage: Extract<ModelStreamPart, { type: 'usage' }> | null
  reasoning: string[]
  finish: Extract<ModelStreamPart, { type: 'finish' }>
}

async function step(
  made: AgentProvider<unknown>,
  settings: unknown,
  messages: ChatMessage[],
  options: {
    conversation?: string
    tools?: ToolSpec[]
    signal?: AbortSignal
    model?: string
    effort?: 'low'
  } = {},
): Promise<Step> {
  const out: Step = { text: '', calls: [], usage: null, reasoning: [], finish: null! }
  for await (const part of made.stream(settings, {
    model: options.model ?? 'default',
    messages,
    tools: options.tools ?? TOOLS,
    ...('conversation' in options
      ? { conversation: options.conversation }
      : { conversation: 'c1' }),
    ...(options.effort ? { effort: options.effort } : {}),
    ...(options.signal ? { signal: options.signal } : {}),
  })) {
    if (part.type === 'text') out.text += part.text
    else if (part.type === 'tool-call')
      out.calls.push({ id: part.id, name: part.name, arguments: part.arguments })
    else if (part.type === 'usage') out.usage = part
    else if (part.type === 'reasoning') out.reasoning.push(part.text)
    else if (part.type === 'finish') out.finish = part
  }
  return out
}

/**
 * Run a turn as the agent loop does: steps until the model answers, each call answered by
 * `answer`. Returns the steps; `messages` grows with the turn.
 */
async function turn(
  made: AgentProvider<unknown>,
  settings: unknown,
  messages: ChatMessage[],
  answer: (name: string, args: { key?: string }) => string = (_n, args) => `v-${args.key}`,
  options: Parameters<typeof step>[3] = {},
): Promise<Step[]> {
  const steps: Step[] = []
  for (let i = 0; i < 10; i++) {
    const s = await step(made, settings, messages, options)
    steps.push(s)
    if (s.finish.reason !== 'tool-calls') {
      if (s.finish.reason === 'stop') messages.push({ role: 'assistant', content: s.text })
      return steps
    }
    messages.push({ role: 'assistant', content: s.text, toolCalls: s.calls })
    for (const call of s.calls) {
      messages.push({
        role: 'tool',
        toolCallId: call.id,
        content: answer(call.name, JSON.parse(call.arguments)),
      })
    }
  }
  throw new Error('too many steps')
}

const system = (content = 'be brief'): ChatMessage => ({ role: 'system', content })

describe.each([
  ['claude-code', claudeCode, { command: fake('claude') }],
  ['codex', codex, { command: fake('codex') }],
  ['acp', acp, { command: fake('acp') }],
] as const)('%s', (_name, agent, settings) => {
  test('runs the conversation’s tools step by step and answers', async () => {
    const made = provider(agent as AgentKind<never>)
    const messages: ChatMessage[] = [
      system(),
      { role: 'user', content: 'CALL lookup {"key":"a"}\nCALL lookup {"key":"b"}' },
    ]
    const steps = await turn(made, settings, messages)
    // Calls made one after another are steps of their own; each result goes back to it.
    expect(steps.map((s) => s.calls.map((c) => c.arguments))).toEqual([
      ['{"key":"a"}'],
      ['{"key":"b"}'],
      [],
    ])
    expect(steps.at(-1)!.finish.reason).toBe('stop')
    expect(steps.at(-1)!.text).toContain('turn 1: v-a | v-b')
    expect(made.sessions()).toBe(1)
  })

  test('calls made side by side end one step together', async () => {
    const made = provider(agent as AgentKind<never>)
    const messages: ChatMessage[] = [
      system(),
      { role: 'user', content: 'CALL lookup {"key":"a"}\n&CALL lookup {"key":"b"}' },
    ]
    const steps = await turn(made, settings, messages)
    expect(steps[0]!.calls.map((c) => c.arguments).toSorted()).toEqual([
      '{"key":"a"}',
      '{"key":"b"}',
    ])
    expect(steps.at(-1)!.text).toContain('v-a | v-b')
  })

  test('a later message goes on in the same session; a different history starts over', async () => {
    const made = provider(agent as AgentKind<never>)
    const messages: ChatMessage[] = [system(), { role: 'user', content: 'hello' }]
    await turn(made, settings, messages)
    messages.push({ role: 'user', content: 'again' })
    const [again] = await turn(made, settings, messages)
    // The second turn of the same program, told only the new message.
    expect(again!.text).toMatch(/^turn 2: {2}len 5 /)

    // History summarised: it no longer starts with what the session saw.
    const summarised: ChatMessage[] = [
      system(),
      { role: 'assistant', content: 'Summary: said hello twice.' },
      { role: 'user', content: 'third' },
    ]
    const [fresh] = await turn(made, settings, summarised)
    expect(fresh!.text).toMatch(/^turn 1: /)
    expect(made.sessions()).toBe(1)
  })

  test('a failure ends the session and says why', async () => {
    const made = provider(agent as AgentKind<never>)
    const result = await step(made, settings, [
      system(),
      { role: 'user', content: 'FAIL rate limit reached' },
    ])
    expect(result.finish).toMatchObject({ reason: 'error', retryable: true, busy: true })
    expect(result.finish.error).toContain('rate limit reached')
    expect(made.sessions()).toBe(0)
  })

  test('reports usage and reasoning', async () => {
    const made = provider(agent as AgentKind<never>)
    const result = await step(made, settings, [system(), { role: 'user', content: 'hi' }])
    expect(result.usage?.input).toBeGreaterThan(0)
    expect(result.usage?.cached).toBeGreaterThan(0)
    expect(result.reasoning.join('')).toMatch(/hmm|thinking/)
  })

  test('a request without a conversation stands alone', async () => {
    const made = provider(agent as AgentKind<never>)
    const result = await step(made, settings, [system(), { role: 'user', content: 'sum up' }], {
      conversation: undefined,
      tools: [],
    })
    expect(result.finish.reason).toBe('stop')
    expect(made.sessions()).toBe(0)
  })
})

describe('claude code', () => {
  test('reports the cost it was told, once per turn, and corrects usage from the result', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    const result = await step(made, { command: fake('claude') }, [
      system(),
      { role: 'user', content: 'hi' },
    ])
    expect(result.usage).toMatchObject({ input: 100, output: 7, cached: 90, costUsd: 0.01 })
  })

  test('stopping ends the program', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    const controller = new AbortController()
    const pending = step(
      made,
      { command: fake('claude') },
      [system(), { role: 'user', content: 'SLOW' }],
      {
        signal: controller.signal,
      },
    )
    await Bun.sleep(300)
    controller.abort()
    await pending
    expect(made.sessions()).toBe(0)
  })

  test('a missing program fails without retrying', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    const result = await step(made, { command: 'no-such-agent-program' }, [
      system(),
      { role: 'user', content: 'hi' },
    ])
    expect(result.finish).toMatchObject({ reason: 'error' })
    expect(result.finish.retryable).toBeFalsy()
  })

  test('changed instructions reach the running session', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    const messages: ChatMessage[] = [system('one'), { role: 'user', content: 'hi' }]
    await turn(made, { command: fake('claude') }, messages)
    messages[0] = system('two')
    messages.push({ role: 'user', content: 'again' })
    const [next] = await turn(made, { command: fake('claude') }, messages)
    expect(next!.text).toMatch(/^turn 2: /)
    // The input grew by the new instructions.
    expect(Number(/len (\d+)/.exec(next!.text)![1])).toBeGreaterThan('again'.length + 'two'.length)
  })

  test('messages that arrive while it waits on a call come with the result', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    const settings = { command: fake('claude') }
    const messages: ChatMessage[] = [system(), { role: 'user', content: 'CALL lookup {"key":"a"}' }]
    const first = await step(made, settings, messages)
    messages.push({ role: 'assistant', content: '', toolCalls: first.calls })
    messages.push({ role: 'tool', toolCallId: first.calls[0]!.id, content: 'R' })
    messages.push({ role: 'user', content: 'by the way' })
    const second = await step(made, settings, messages)
    expect(second.text).toContain(
      'turn 1: R\n\n<arrived_meanwhile>\nby the way\n</arrived_meanwhile>',
    )
  })
})

describe('codex', () => {
  test('declines what it asks to approve and passes the effort', async () => {
    const made = provider(codex as AgentKind<never>)
    const result = await step(
      made,
      { command: fake('codex') },
      [system(), { role: 'user', content: 'hi' }],
      {
        effort: 'low',
      },
    )
    expect(result.text).toContain('approval decline effort low')
  })

  test('has no environment of its own and is not told about this machine', async () => {
    const made = provider(codex as AgentKind<never>)
    const result = await step(made, { command: fake('codex') }, [
      system(),
      { role: 'user', content: 'hi' },
    ])
    // Its own command, patch and image tools have nowhere to run; the work is where the
    // conversation's tools run, which may be another machine.
    expect(result.text).toContain('environments [] machine context off place note true')
  })
})

describe('acp', () => {
  test('allows only the permissions its own tools need, and picks the model', async () => {
    const made = provider(acp as AgentKind<never>)
    const result = await step(
      made,
      { command: fake('acp') },
      [system(), { role: 'user', content: 'CALL lookup {"key":"a"}' }],
      { model: 'fast' },
    )
    expect(result.calls).toHaveLength(1)
  })

  test("a permission counts as for the conversation's tools only by the tool's exact name", () => {
    const tools = ['lookup', 'read_file']
    for (const title of [
      'lookup (ariadne MCP Server)',
      'mcp__ariadne__read_file',
      'ariadne/lookup',
      'ariadne__read_file',
    ]) {
      expect(forOwnTools({ title }, tools)).toBe(true)
    }
    expect(forOwnTools({ title: 'Tool', rawInput: { tool: 'mcp__ariadne__lookup' } }, tools)).toBe(
      true,
    )
    // The agent's own tools, even when their paths or commands say "ariadne".
    expect(forOwnTools({ title: 'Run shell command: ls' }, tools)).toBe(false)
    expect(
      forOwnTools(
        {
          kind: 'execute',
          title: 'ls',
          rawInput: { command: 'ls', cwd: '/home/u/.ariadne/plugins/agents/sessions/x' },
        },
        tools,
      ),
    ).toBe(false)
    expect(
      forOwnTools(
        {
          kind: 'edit',
          title: 'Edit /home/u/code/ariadne/a.ts',
          locations: [{ path: '/home/u/code/ariadne/a.ts' }],
        },
        tools,
      ),
    ).toBe(false)
    // Not one of this conversation's tools, though it is spelled like one.
    expect(forOwnTools({ title: 'mcp__ariadne__shell' }, tools)).toBe(false)
    expect(forOwnTools({ title: 'mcp__ariadne__lookup' }, [])).toBe(false)
  })

  test('the first message carries the instructions; images go along', async () => {
    const made = provider(acp as AgentKind<never>)
    const result = await step(made, { command: fake('acp') }, [
      system('S'.repeat(50)),
      { role: 'user', content: 'look', images: [{ mime: 'image/png', data: 'AAAA' }] },
    ])
    expect(result.text).toContain('own no model default')
    expect(result.text).toContain('images 1')
    expect(Number(/len (\d+)/.exec(result.text)![1])).toBeGreaterThan(50)
  })

  test('a missing command is said plainly', async () => {
    const made = provider(acp as AgentKind<never>)
    const result = await step(made, {}, [system(), { role: 'user', content: 'hi' }])
    expect(result.finish.error).toContain('no command')
  })
})

describe('the tool bridge over stdio', () => {
  test('passes calls through and back', async () => {
    let seen: unknown = null
    const { url, remove } = bridge.add({
      tools: () => TOOLS,
      call: async (_name, args) => {
        seen = args
        return { content: 'ok', isError: false, images: [] }
      },
    })
    const proc = Bun.spawn([process.execPath, join(import.meta.dirname, 'mcp-stdio.ts'), url], {
      stdin: 'pipe',
      stdout: 'pipe',
      windowsHide: true,
    })
    proc.stdin.write(
      `${JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' })}\n${JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'lookup', arguments: { key: 'k' } } })}\n`,
    )
    await proc.stdin.flush()
    const reader = proc.stdout.getReader()
    let out = ''
    while (out.split('\n').filter(Boolean).length < 2) {
      const { value, done } = await reader.read()
      if (done) break
      out += new TextDecoder().decode(value)
    }
    proc.kill()
    remove()
    const answers = out
      .split('\n')
      .filter(Boolean)
      .map((l) => JSON.parse(l))
    expect(answers.find((a) => a.id === 1).result.tools[0].name).toBe('lookup')
    expect(answers.find((a) => a.id === 2).result.content[0].text).toBe('ok')
    expect(seen).toEqual({ key: 'k' })
  })

  test('an unknown address is not served', async () => {
    bridge.add({ tools: () => [], call: async () => ({ content: '', isError: false, images: [] }) })
    const { url } = bridge.add({
      tools: () => [],
      call: async () => ({ content: '', isError: false, images: [] }),
    })
    const response = await fetch(url.replace(/[^/]+$/, 'nope'), { method: 'POST', body: '{}' })
    expect(response.status).toBe(404)
  })
})

describe('history', () => {
  test('a first message goes as it is; more is written out with calls and results', () => {
    expect(renderHistory([{ role: 'user', content: 'hi' }]).text).toBe('hi')
    const text = renderHistory([
      { role: 'user', content: 'q' },
      {
        role: 'assistant',
        content: 'a',
        toolCalls: [{ id: 'c1', name: 'read_file', arguments: '{}' }],
      },
      { role: 'tool', toolCallId: 'c1', content: 'file', isError: true },
    ]).text
    expect(text).toContain('<call id="c1" name="read_file">{}</call>')
    expect(text).toContain('<result id="c1" name="read_file" error="true">')
    expect(text).toContain('the calls above have already run')
  })

  test('cleared results and aged-out pictures keep a message the same', () => {
    expect(fingerprint({ role: 'tool', toolCallId: 'x', content: 'long' })).toBe(
      fingerprint({ role: 'tool', toolCallId: 'x', content: '[cleared]' }),
    )
    const base = 'u'.repeat(500)
    expect(fingerprint({ role: 'user', content: base })).toBe(
      fingerprint({ role: 'user', content: `${base}\n[image no longer shown]` }),
    )
  })
})

test('command lines split like a shell does', () => {
  expect(splitCommand(`my-agent --acp "a b" 'c d' e\\ f`)).toEqual([
    'my-agent',
    '--acp',
    'a b',
    'c d',
    'e f',
  ])
  expect(splitCommand('  ')).toEqual([])
})

const looking = (home: string, programs: string[], env: Record<string, string> = {}) => ({
  env,
  home,
  which: (program: string) => (programs.includes(program) ? `/bin/${program}` : null),
})

describe('found on this machine', () => {
  test('Claude Code counts once it is installed and signed in', () => {
    const made = provider(claudeCode as AgentKind<never>)
    expect(made.find!(looking(dir, ['claude']))).toEqual([])
    expect(made.find!(looking(dir, [], { CLAUDE_CODE_OAUTH_TOKEN: 't' }))).toEqual([])
    const found = [{ id: 'claude-code', from: { program: 'claude' }, settings: {} }]
    expect(made.find!(looking(dir, ['claude'], { CLAUDE_CODE_OAUTH_TOKEN: 't' }))).toEqual(found)
    mkdirSync(join(dir, 'elsewhere'))
    writeFileSync(join(dir, 'elsewhere', '.credentials.json'), '{}')
    expect(
      made.find!(looking(dir, ['claude'], { CLAUDE_CONFIG_DIR: join(dir, 'elsewhere') })),
    ).toEqual(found)
  })

  test('Codex counts once it is installed and signed in', () => {
    const made = provider(codex as AgentKind<never>)
    expect(made.find!(looking(dir, ['codex']))).toEqual([])
    mkdirSync(join(dir, '.codex'))
    writeFileSync(join(dir, '.codex', 'auth.json'), '{}')
    expect(made.find!(looking(dir, ['codex']))).toEqual([
      { id: 'codex', from: { program: 'codex' }, settings: {} },
    ])
    expect(made.find!(looking(dir, []))).toEqual([])
  })

  test('an agent program is checked by whether its command is there', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    expect(await made.check!({ command: `${process.execPath} --x` } as never)).toBeNull()
    expect(await made.check!({ command: 'no-such-program-here --x' } as never)).toEqual({
      'zh-CN': '找不到命令 no-such-program-here',
      en: 'command not found: no-such-program-here',
    })
  })

  test('Claude Code says which models its account can use', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    expect(await made.models!({ command: fake('claude') } as never)).toEqual([
      { id: 'default', title: 'Big 2', resolves: 'claude-big-2', efforts: ['low', 'high'] },
      {
        id: 'big',
        title: 'Big 2',
        resolves: 'claude-big-2',
        efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
      },
      { id: 'small', title: 'Small 1', resolves: 'claude-small-1', efforts: [] },
      // Left out by a version that tells them: it takes none.
      { id: 'claude-big-1', title: 'Big 1', efforts: [] },
    ])
    // An older version tells none of them: nothing is known.
    expect(claudeModels([{ value: 'opus', displayName: 'Opus' }])).toEqual([
      { id: 'opus', title: 'Opus' },
    ])
  })

  test('Codex lists its models page by page', async () => {
    const made = provider(codex as AgentKind<never>)
    expect(await made.models!({ command: fake('codex') } as never)).toEqual([
      { id: 'default' },
      { id: 'gpt-big', title: 'GPT Big', efforts: ['low', 'high', 'xhigh'] },
      { id: 'gpt-small' },
    ])
  })

  test('an ACP agent lists the models of a session it opens', async () => {
    const made = provider(acp as AgentKind<never>)
    expect(await made.models!({ command: fake('acp') } as never)).toEqual([
      { id: 'default' },
      { id: 'fast' },
    ])
    expect(
      acpModels({
        currentModelId: 'pro',
        availableModels: [{ modelId: 'pro', name: 'Pro' }, { modelId: 'lite' }],
      }),
    ).toEqual([
      { id: 'default', title: 'Pro', resolves: 'pro' },
      { id: 'pro', title: 'Pro' },
      { id: 'lite' },
    ])
  })

  test('the fixed models are offered when the program cannot say', async () => {
    const made = provider(claudeCode as AgentKind<never>)
    const crashing = `${process.execPath} -e process.exit(3)`
    expect(await made.models!({ command: crashing } as never)).toEqual([
      'default',
      'sonnet',
      'opus',
      'haiku',
    ])
    expect(await made.models!({ command: 'no-such-program-here' } as never)).toEqual([
      'default',
      'sonnet',
      'opus',
      'haiku',
    ])
    const silent = `${process.execPath} -e setInterval(()=>{},1000)`
    expect(
      await made.models!({ command: silent } as never, { signal: AbortSignal.timeout(300) }),
    ).toEqual(['default', 'sonnet', 'opus', 'haiku'])
    expect(await provider(acp as AgentKind<never>).models!({} as never)).toEqual(['default'])
  })
})
