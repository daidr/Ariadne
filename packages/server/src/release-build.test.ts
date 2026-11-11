import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'
import { entrySource, hostTarget, listFiles, parseArgs, TARGETS } from './release-build'

describe('release build', () => {
  test('targets: this machine by default, a list, or all', () => {
    expect(parseArgs([])).toEqual({ targets: [hostTarget()], upx: true })
    expect(parseArgs(['--target', 'linux-x64,windows-x64', '--no-upx'])).toEqual({
      targets: ['linux-x64', 'windows-x64'],
      upx: false,
    })
    expect(parseArgs(['--target=all']).targets).toEqual([...TARGETS])
    expect(() => parseArgs(['--target', 'plan9-x64'])).toThrow('unknown target')
    expect(() => parseArgs(['--fast'])).toThrow('unknown argument')
  })

  test('the entry carries every built file under its address', () => {
    const dir = mkdtempSync(join(tmpdir(), 'ariadne-dist-'))
    mkdirSync(join(dir, 'assets'))
    writeFileSync(join(dir, 'index.html'), '')
    writeFileSync(join(dir, 'assets', 'index-abc.js'), '')
    const files = listFiles(dir)
    expect(files).toEqual(['/assets/index-abc.js', '/index.html'])
    const source = entrySource(dir, files, '/repo/release.ts')
    expect(source).toContain(`import { runRelease } from "/repo/release.ts"`)
    expect(source).toContain(`with { type: 'file' }`)
    expect(source).toContain(`"/index.html": file1,`)
  })
})
