import { expect, test } from 'vitest'
import { locale } from '../i18n'
import { messageTime, messageTimes } from './conversation-timeline'
import {
  coordinatorReading,
  leaveCoordinator,
  receiveCoordinatorReplies,
} from './coordinator-unread'

const minute = 60_000
const item = (id: string, minutes: number) => ({ at: minutes * minute, message: { id } })

test('labels the first message, ten minutes of conversation and gaps over five minutes', () => {
  const items = [
    { at: -minute, message: null },
    item('first', 0),
    item('five', 5),
    item('nine', 9),
    item('ten', 10),
    { at: 14 * minute, message: null },
    item('after-gap', 15.01),
    item('same-minute', 15.01),
  ]
  expect([...messageTimes(items)]).toEqual(['first', 'ten', 'after-gap'])
})

test('chooses the shortest timestamp that identifies the local calendar day', () => {
  locale.value = 'zh-CN'
  const today = new Date(2026, 10, 21, 15).getTime()
  const time = (year: number, month: number, day: number) =>
    messageTime(new Date(year, month - 1, day, 9, 5).getTime(), today)
  expect(time(2026, 11, 21)).toBe('09:05')
  expect(time(2026, 11, 20)).toBe('昨天 09:05')
  expect(time(2026, 11, 19)).toBe('前天 09:05')
  expect(time(2026, 10, 1)).toBe('10-01 09:05')
  expect(time(2025, 12, 31)).toBe('2025-12-31 09:05')
})

const reply = (id: string) => ({ id, author: 'coordinator' as const })

test('shows the first new reply after reading even when the coordinator stays open', () => {
  const messages = [reply('msg-1')]
  const reading = coordinatorReading('', messages)
  reading.seen = 'msg-1'
  messages.push(reply('msg-2'))
  receiveCoordinatorReplies(reading, messages, false)
  expect(reading.boundary).toBe('msg-2')
  reading.seen = 'msg-2'
  messages.push(reply('msg-3'))
  receiveCoordinatorReplies(reading, messages, false)
  expect(reading.boundary).toBe('msg-2')
})

test('the unread boundary only moves for another batch after reading and leaving', () => {
  const messages = [reply('msg-1')]
  const reading = coordinatorReading('', messages)
  reading.seen = 'msg-1'
  leaveCoordinator(reading)

  messages.push(reply('msg-3'), reply('msg-4'))
  receiveCoordinatorReplies(reading, messages, true)
  expect(reading.boundary).toBe('msg-3')

  reading.seen = 'msg-3'
  leaveCoordinator(reading)
  messages.push(reply('msg-5'))
  receiveCoordinatorReplies(reading, messages, true)
  expect(reading.boundary).toBe('msg-3')

  reading.seen = 'msg-5'
  messages.push(reply('msg-6'))
  receiveCoordinatorReplies(reading, messages, false)
  expect(reading.boundary).toBe('msg-3')

  reading.seen = 'msg-6'
  leaveCoordinator(reading)
  messages.push(reply('msg-7'))
  receiveCoordinatorReplies(reading, messages, true)
  expect(reading.boundary).toBe('msg-7')
})

test('restores the first unread AI reply after reloading and ignores user messages', () => {
  const messages = [reply('msg-1'), { id: 'msg-2', author: 'user' as const }, reply('msg-3')]
  const reading = coordinatorReading('msg-1', messages)
  receiveCoordinatorReplies(reading, messages, false)
  expect(reading.boundary).toBe('msg-3')
  receiveCoordinatorReplies(reading, [reply('msg-0'), ...messages], false)
  expect(reading.boundary).toBe('msg-3')
})
