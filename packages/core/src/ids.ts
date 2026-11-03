/** Time-ordered ids, so rows sort by creation without an extra column. */
export function newId(prefix: string): string {
  return `${prefix}_${Bun.randomUUIDv7('hex')}`
}
