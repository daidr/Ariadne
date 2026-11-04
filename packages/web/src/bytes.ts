import { locale } from './i18n'

/** A size on disk, short: `12 MB`, `1.4 GB`. */
export function formatBytes(bytes: number): string {
  const unit = bytes >= 1024 ** 3 ? 'gigabyte' : bytes >= 1024 ** 2 ? 'megabyte' : 'kilobyte'
  const scale = bytes >= 1024 ** 3 ? 1024 ** 3 : bytes >= 1024 ** 2 ? 1024 ** 2 : 1024
  return new Intl.NumberFormat(locale.value, {
    notation: 'compact',
    style: 'unit',
    unit,
    unitDisplay: 'short',
    maximumFractionDigits: 1,
  }).format(bytes / scale)
}
