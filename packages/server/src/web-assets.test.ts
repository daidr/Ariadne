import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'
import { directoryAssets, embeddedAssets } from './web-assets'

describe('web assets', () => {
  test('a directory serves its files and nothing above it', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'ariadne-web-'))
    mkdirSync(join(dir, 'assets'))
    writeFileSync(join(dir, 'assets', 'a.js'), 'export {}')
    const web = directoryAssets(dir)
    expect(await (await web.get('/assets/a.js'))?.text()).toBe('export {}')
    expect(await web.get('/assets/b.js')).toBeNull()
    expect(await web.get('/../secret')).toBeNull()
  })

  test('carried files are typed by the path asked for, not their own name', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'ariadne-web-'))
    // The program renames what it carries; the name keeps no meaningful extension.
    const stored = join(dir, 'index-1a2b3c')
    writeFileSync(stored, '<!doctype html>')
    const web = embeddedAssets({ '/index.html': stored })
    const shell = await web.get('/index.html')
    expect(shell?.type).toContain('text/html')
    expect(await shell?.text()).toBe('<!doctype html>')
    expect(await web.get('/missing.js')).toBeNull()
    expect(await web.get('/constructor')).toBeNull()
  })
})
