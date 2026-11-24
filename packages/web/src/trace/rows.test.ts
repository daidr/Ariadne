import type { TraceTurn } from '@ariadne/core'
import { afterEach, describe, expect, test } from 'vitest'
import { locale } from '../i18n'
import { traceRows } from './rows'

function makeTurn(turnId: string, outcome: TraceTurn['outcome'] = 'completed'): TraceTurn {
  return {
    turnId,
    seq: 1,
    model: 'test-model',
    startedAt: 1_000,
    finishedAt: 2_000,
    outcome,
    error: null,
    usage: { input: 100, output: 50 },
    items: [
      {
        kind: 'request',
        seq: 1,
        number: 1,
        purpose: 'step',
        startedAt: 1_100,
        finishedAt: 1_500,
        provider: 'anthropic',
        model: 'test-model',
        effort: null,
        firstOutputAt: 1_200,
        finish: 'stop',
        usage: { input: 100, output: 50 },
        reasoning: [],
        text: 'hello',
        toolCalls: [],
      },
    ],
  }
}

describe('traceRows caching', () => {
  const initialLocale = locale.value

  afterEach(() => {
    locale.value = initialLocale
  })

  test('reuses cached TraceRow instances for unchanged turns and rebuilds updated turns', () => {
    const turnA = makeTurn('turn_1')
    const turnB = makeTurn('turn_2', null)

    const first = traceRows([turnA, turnB])
    const turnBNext = makeTurn('turn_2', 'completed')
    const second = traceRows([turnA, turnBNext])

    expect(second[0]).toBe(first[0])
    expect(second[1]).not.toBe(first[1])
  })

  test('invalidates cached rows when locale changes', () => {
    locale.value = 'zh-CN'
    const turn = makeTurn('turn_1')
    const zhRows = traceRows([turn])

    locale.value = 'en'
    const enRows = traceRows([turn])

    expect(enRows[0]).not.toBe(zhRows[0])
    expect(enRows[0]!.event).not.toBe(zhRows[0]!.event)
  })
})
