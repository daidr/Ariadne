import { t } from '../i18n'

const MINUTE = 60_000
const DAY = 24 * 60 * MINUTE

/** Group messages by the time since the last label and the gap since the last message. */
export function messageTimes(items: readonly { at: number; message: { id: string } | null }[]) {
  const times = new Set<string>()
  let label = -Infinity
  let previous = -Infinity
  for (const { at, message } of items) {
    if (!message) continue
    if (at - label >= 10 * MINUTE || at - previous > 5 * MINUTE) {
      times.add(message.id)
      label = at
    }
    previous = at
  }
  return times
}

const calendarDay = (date: Date) => Date.UTC(date.getFullYear(), date.getMonth(), date.getDate())
const two = (value: number) => String(value).padStart(2, '0')

export function messageTime(at: number, now: number) {
  const date = new Date(at)
  const today = new Date(now)
  const days = (calendarDay(today) - calendarDay(date)) / DAY
  const time = `${two(date.getHours())}:${two(date.getMinutes())}`
  if (days === 0) return time
  if (days === 1) return t('message.yesterday', { time })
  if (days === 2) return t('message.dayBeforeYesterday', { time })
  const day = `${two(date.getMonth() + 1)}-${two(date.getDate())}`
  return date.getFullYear() === today.getFullYear()
    ? `${day} ${time}`
    : `${date.getFullYear()}-${day} ${time}`
}
