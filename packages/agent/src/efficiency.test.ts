import { Ariadne } from '@ariadne/core'
import type { ChatMessage, ModelRequest } from '@ariadne/plugin-sdk'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { compactAt } from './compaction'
import { callGroups, runTurn, type ResolvedModel } from './runner'
import { call, fakeModel, say } from './test-model'
import type { Tool } from './tools'
import { clearedResult, CLEARABLE_CHARS } from './transcript'
import { commandResult, headAndTail, MAX_COMMAND_CHARS, OUTPUT_DIR } from './workspace-tools'

let app: Ariadne

beforeEach(() => {
  app = new Ariadne({ path: ':memory:' })
})

afterEach(() => {
  app.close()
})

/** Run one turn of the project's coordinator on what the user said. */
async function turn(model: ResolvedModel, tools: Tool[], options: { compactAt?: number } = {}) {
  const project = app.createProject('P')
  app.postMessage(project.id, {
    conversationId: project.coordinatorId,
    author: 'user',
    content: '开始',
  })
  await runTurn({
    app,
    conversation: app.getConversation(project.coordinatorId),
    model,
    locale: 'zh-CN',
    signal: new AbortController().signal,
    tools,
    ...options,
  })
  return project
}

const results = (request: ModelRequest) =>
  request.messages.filter((m): m is Extract<ChatMessage, { role: 'tool' }> => m.role === 'tool')

describe('tool calls that only look', () => {
  test('are grouped with their looking neighbours; others run alone, in order', () => {
    const byName = new Map<string, Pick<Tool, 'readOnly'>>([
      ['read', { readOnly: true }],
      ['write', {}],
    ])
    const calls = ['read', 'read', 'write', 'read', 'unknown', 'read', 'read'].map((name, i) => ({
      id: String(i),
      name,
      arguments: '{}',
    }))
    expect(callGroups(calls, byName)).toEqual([[0, 1], [2], [3], [4], [5, 6]])
  })

  test('run at the same time, and their results keep the model’s order', async () => {
    const log: string[] = []
    const { promise: gate, resolve: release } = Promise.withResolvers<void>()
    const look = (name: string, wait: boolean): Tool => ({
      readOnly: true,
      spec: { name, description: '', parameters: { type: 'object', properties: {} } },
      async run() {
        log.push(`start ${name}`)
        // The first one finishes only once the second has started.
        if (wait) await gate
        else release()
        log.push(`end ${name}`)
        return { content: name }
      },
    })
    const change: Tool = {
      spec: { name: 'change', description: '', parameters: { type: 'object', properties: {} } },
      run() {
        log.push('change')
        return { content: 'changed' }
      },
    }
    const model = fakeModel((request) =>
      results(request).length
        ? say('好')
        : [
            call('a', 'slow', {}),
            call('b', 'fast', {}),
            call('c', 'change', {}),
            { type: 'finish', reason: 'tool-calls' },
          ],
    )
    await turn(model, [look('slow', true), look('fast', false), change])
    expect(log).toEqual(['start slow', 'start fast', 'end fast', 'end slow', 'change'])
    expect(results(model.requests[1]!).map((m) => [m.toolCallId, m.content])).toEqual([
      ['a', 'slow'],
      ['b', 'fast'],
      ['c', 'changed'],
    ])
  })
})

describe('command output', () => {
  const files: Record<string, string> = {}
  const workspace = {
    async writeFile(path: string, content: string) {
      files[path] = content
    },
  }

  test('is plain text: the exit code, then each stream', async () => {
    expect(
      await commandResult({ exitCode: 1, stdout: 'a\nb', stderr: 'oops' }, workspace, 'x'),
    ).toBe('exit code 1\n--- stdout ---\na\nb\n--- stderr ---\noops')
    expect(await commandResult({ exitCode: 0, stdout: '', stderr: '' }, workspace, 'x')).toBe(
      'exit code 0\n(no output)',
    )
    expect(files).toEqual({})
  })

  test('too long, keeps its beginning and end, and is kept whole in the workspace', async () => {
    const lines = Array.from({ length: 5000 }, (_, i) => `line ${i}`)
    const stdout = lines.join('\n')
    const text = await commandResult({ exitCode: 0, stdout, stderr: 'warn' }, workspace, 'run')
    expect(text.length).toBeLessThan(MAX_COMMAND_CHARS + 200)
    expect(text).toContain('line 0\n')
    expect(text).toContain('line 4999')
    expect(text).not.toContain('line 2500\n')
    expect(text).toContain(`the whole output is in ${OUTPUT_DIR}/run.log`)
    expect(text.endsWith('--- stderr ---\nwarn')).toBe(true)
    expect(files[`${OUTPUT_DIR}/run.log`]).toContain(stdout)
    // The directory ignores itself, so the kept output never shows up as a change.
    expect(files['.ariadne/.gitignore']).toBe('*\n')
  })

  test('is cut at a line boundary', () => {
    const text = headAndTail(
      'x'.repeat(50) + '\n' + 'y'.repeat(100) + '\n' + 'z'.repeat(50),
      160,
      'w',
    )
    expect(text.split('\n')[0]).toBe('x'.repeat(50))
    expect(text.endsWith('\n' + 'z'.repeat(50))).toBe(true)
  })
})

describe('when to summarise', () => {
  test('a large context is summarised at the size of a 400k one', () => {
    expect(compactAt(128_000)).toBe(76_800)
    expect(compactAt(200_000)).toBe(120_000)
    expect(compactAt(1_000_000)).toBe(240_000)
    expect(compactAt(1_000_000, 0.9)).toBe(360_000)
  })
})

describe('making room', () => {
  // Each result differs, or the same call coming back would read as going in circles.
  let n = 0
  const big = (name: string): Tool => ({
    readOnly: true,
    spec: { name, description: '', parameters: { type: 'object', properties: {} } },
    run: () => ({ content: `${n++ % 10}:${'x'.repeat(4000)}` }),
  })

  test('old long tool results are cleared first, and no summary is asked for', async () => {
    let summaries = 0
    let steps = 0
    const model = fakeModel((request) => {
      if (!request.tools.length) {
        summaries++
        return say('摘要')
      }
      const done = steps++
      if (done < 14) return [call(`c${done}`, 'look', {}), { type: 'finish', reason: 'tool-calls' }]
      return say('好')
    })
    const project = await turn(model, [big('look')], { compactAt: 20000 })
    expect(summaries).toBe(0)
    const transcript = app.listTranscript(project.coordinatorId)
    expect(transcript.some((entry) => entry.kind === 'cleared')).toBe(true)
    const last = results(model.requests.at(-1)!)
    // The newest results stay whole; older ones read as a placeholder.
    expect(last.at(-1)!.content.length).toBeGreaterThan(CLEARABLE_CHARS)
    expect(last[0]!.content).toBe(clearedResult(4002))
    // Clearing waits until it is needed again: requests in between start the same way.
    const clearedAt = model.requests.findIndex((r) =>
      results(r).some((m) => m.content.startsWith('[Ariadne: this result')),
    )
    for (const request of model.requests.slice(clearedAt + 1)) {
      const before = model.requests[model.requests.indexOf(request) - 1]!
      expect(JSON.stringify(request.messages)).toContain(
        JSON.stringify(before.messages).slice(1, -1),
      )
    }
  })

  test('short results stay when older ones are cleared', async () => {
    const short: Tool = {
      spec: { name: 'note', description: '', parameters: { type: 'object', properties: {} } },
      run: () => ({ content: 'Written.' }),
    }
    let steps = 0
    const model = fakeModel((request) => {
      if (!request.tools.length) return say('摘要')
      const done = steps++
      if (done === 0) return [call('n', 'note', {}), { type: 'finish', reason: 'tool-calls' }]
      if (done < 15) return [call(`c${done}`, 'look', {}), { type: 'finish', reason: 'tool-calls' }]
      return say('好')
    })
    await turn(model, [big('look'), short], { compactAt: 20000 })
    const last = results(model.requests.at(-1)!)
    expect(last[0]!.content).toBe('Written.')
    expect(last[1]!.content).toBe(clearedResult(4002))
  })
})
