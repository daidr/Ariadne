import type { Effort } from './api'

/** Every effort, least first. */
export const EFFORTS: readonly Effort[] = ['low', 'medium', 'high', 'xhigh', 'max']

/** What a model is taken to accept when nobody says which efforts it does. */
export const COMMON_EFFORTS: readonly Effort[] = ['low', 'medium', 'high']

/**
 * The efforts a model may be asked for: what it accepts, as its service or the catalog
 * said (`told`), the common ones when nobody said. The server takes the same.
 */
export const effortsOf = (told: readonly Effort[] | null | undefined): readonly Effort[] =>
  told ?? COMMON_EFFORTS

/**
 * The effort a model that accepts `efforts` thinks at when `wanted` is asked: the nearest
 * it accepts at or below it, else the least it accepts; `null` when it accepts none. The
 * server picks the same way.
 */
export function effortWithin(wanted: Effort, efforts: readonly Effort[]): Effort | null {
  if (efforts.includes(wanted)) return wanted
  const rank = EFFORTS.indexOf(wanted)
  const ranked = EFFORTS.filter((e) => efforts.includes(e))
  return ranked.filter((e) => EFFORTS.indexOf(e) < rank).at(-1) ?? ranked[0] ?? null
}
