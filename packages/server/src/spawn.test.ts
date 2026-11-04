import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from 'vitest'

const root = join(import.meta.dirname, '../../..')

/** The text of the call that opens at `start`, up to its matching parenthesis. */
function callAt(source: string, start: number): string {
  let depth = 0
  for (let i = source.indexOf('(', start); i < source.length; i++) {
    if (source[i] === '(') depth++
    else if (source[i] === ')' && --depth === 0) return source.slice(start, i + 1)
  }
  return source.slice(start)
}

// On Windows a console program started without `windowsHide` opens a console window of its
// own, which flashes up every time a thread runs a command or the server calls git.
test('every process the code starts hides its window on Windows', () => {
  const missing: string[] = []
  for (const file of new Bun.Glob('packages/*/src/**/*.ts').scanSync({ cwd: root })) {
    if (file.endsWith('.test.ts')) continue
    const source = readFileSync(join(root, file), 'utf8')
    const line = (index: number) => `${file}:${source.slice(0, index).split('\n').length}`
    // node:child_process would need the same option; the code uses Bun.spawn only.
    // Same for spawn imported from 'bun' and called without the Bun. prefix.
    for (const match of source.matchAll(
      /['"](?:node:)?child_process['"]|import\s*\{[^}]*\bspawn(?:Sync)?\b[^}]*\}\s*from\s*['"]bun['"]/g,
    )) {
      missing.push(line(match.index))
    }
    for (const match of source.matchAll(/\bBun\.spawn(?:Sync)?\s*(?:<[^()]*>)?\(/g)) {
      if (!/\bwindowsHide:\s*true\b/.test(callAt(source, match.index)))
        missing.push(line(match.index))
    }
  }
  expect(missing).toEqual([])
})
