/** How long a command has run: `m:ss`, or `h:mm:ss` past an hour. */
export function elapsedText(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = String(seconds % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`
}

/** A status line keeps room beside a command: its first line, cut short. */
const MAX_SHOWN = 48

export function firstLine(command: string): string {
  const line = command.split('\n', 1)[0]!.trim()
  return line.length > MAX_SHOWN ? `${line.slice(0, MAX_SHOWN)}…` : line
}
