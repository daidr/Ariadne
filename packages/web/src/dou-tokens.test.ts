import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, test } from 'vitest'

const WEB_ROOT = resolve(import.meta.dirname, '..')
const SRC_ROOT = resolve(WEB_ROOT, 'src')
const NODE_MODULES = resolve(WEB_ROOT, 'node_modules')

function walk(dir: string, predicate: (file: string) => boolean, out: string[] = []): string[] {
  if (!existsSync(dir)) return out
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    let isDir = entry.isDirectory()
    if (!isDir && entry.isSymbolicLink()) {
      try {
        isDir = statSync(full).isDirectory()
      } catch {
        isDir = false
      }
    }
    if (isDir) walk(full, predicate, out)
    else if (predicate(full)) out.push(full)
  }
  return out
}

function collectKnownDouTokens(): Set<string> {
  const roots = [NODE_MODULES, resolve(WEB_ROOT, '..', '..', 'node_modules')]
  const cssFiles = roots.flatMap((nm) => [
    ...walk(join(nm, 'dou-ui', 'dist'), (f) => f.endsWith('.css')),
    ...walk(join(nm, '@dou-ui'), (f) => f.endsWith('.css')),
  ])
  const tokens = new Set<string>()
  for (const file of cssFiles) {
    const content = readFileSync(file, 'utf8')
    for (const match of content.matchAll(/(--dou-[a-z0-9-]+)\s*:/g)) {
      tokens.add(match[1]!)
    }
  }
  return tokens
}

describe('dou-ui CSS tokens', () => {
  test('every var(--dou-*) referenced in packages/web/src exists in dou-ui or @dou-ui/*', () => {
    const known = collectKnownDouTokens()
    expect(known.size).toBeGreaterThan(100)

    const sourceFiles = walk(SRC_ROOT, (f) => /\.(vue|ts|css)$/.test(f) && !f.endsWith('.test.ts'))
    const unknown: string[] = []

    for (const file of sourceFiles) {
      const content = readFileSync(file, 'utf8')
      const localDeclared = new Set(
        [...content.matchAll(/(--dou-[a-z0-9-]+)\s*:/g)].map((m) => m[1]),
      )
      const lines = content.split(/\r?\n/)
      lines.forEach((line, index) => {
        for (const match of line.matchAll(/var\(\s*(--dou-[a-z0-9-]+)/g)) {
          const token = match[1]!
          if (!known.has(token) && !localDeclared.has(token)) {
            unknown.push(`${relative(WEB_ROOT, file)}:${index + 1} (${token})`)
          }
        }
      })
    }

    expect(unknown).toEqual([])
  })
})
