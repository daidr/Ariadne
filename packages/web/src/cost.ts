import type { Currency } from './api'
import { locale } from './i18n'

/**
 * What was spent, as one line per currency used: `$0.42 · ¥3.10`. `null` when nothing was
 * priced. Small amounts keep more digits, so a cheap conversation does not read as zero.
 */
export function costText(cost: Partial<Record<Currency, number>> | undefined): string | null {
  const parts = (['USD', 'CNY'] as const)
    .filter((currency) => (cost?.[currency] ?? 0) > 0)
    .map((currency) => {
      const amount = cost![currency]!
      return new Intl.NumberFormat(locale.value, {
        style: 'currency',
        currency,
        minimumFractionDigits: 2,
        maximumFractionDigits: amount < 1 ? 4 : 2,
      }).format(amount)
    })
  return parts.length ? parts.join(' · ') : null
}
