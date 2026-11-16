import { mkdtempSync, readdirSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { afterEach, expect, test } from 'vitest'
import { Checkpoints } from './checkpoints'

let dir: string
afterEach(() => {
  if (dir) rmSync(dir, { recursive: true, force: true })
})
const fixture = () => {
  dir = mkdtempSync(join(tmpdir(), 'ariadne-checkpoints-'))
  return new Checkpoints(dir, 'codex', 4)
}
const saved = { config: 'config', system: 'system', fps: ['u'], driver: { threadId: 'thread' } }

test('checkpoints are bounded and expire; deletion releases a saved session', () => {
  const checkpoints = fixture()
  for (let i = 0; i < 6; i++) checkpoints.write(`c${i}`, saved)
  const files = join(dir, 'checkpoints', 'codex')
  expect(readdirSync(files)).toHaveLength(4)
  checkpoints.write('new', saved)
  expect(checkpoints.read('new')).toEqual(saved)
  checkpoints.remove('new')
  expect(checkpoints.read('new')).toBeNull()
  for (const name of readdirSync(files)) utimesSync(join(files, name), 1, 1)
  const pruned = new Checkpoints(dir, 'codex')
  expect(pruned.read('c5')).toBeNull()
  expect(readdirSync(files)).toHaveLength(0)
})

test('oversized and malformed checkpoints cannot be restored', () => {
  const checkpoints = fixture()
  checkpoints.write('large', { ...saved, system: 'x'.repeat(4 * 1024 * 1024) })
  expect(checkpoints.read('large')).toBeNull()
  checkpoints.write('broken', saved)
  const files = join(dir, 'checkpoints', 'codex')
  writeFileSync(join(files, readdirSync(files)[0]!), '{')
  expect(checkpoints.read('broken')).toBeNull()
})
