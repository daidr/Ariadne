import { Ariadne } from '@ariadne/core'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, expect, test } from 'vitest'
import { DataStore } from './data'

let home: string
let app: Ariadne

beforeEach(() => {
  home = mkdtempSync(join(tmpdir(), 'ariadne-data-'))
  app = new Ariadne({ path: join(home, 'ariadne.db') })
})

afterEach(() => app.close())

test('a backup is a database of its own, listed newest first and removable by name', async () => {
  app.createProject('备份')
  const data = new DataStore(app, home, join(home, 'ariadne.db'))
  expect((await data.usage()).backups).toEqual([])
  const first = data.backup()
  const second = data.backup()
  expect(first.name).toMatch(/^ariadne-\d{8}-\d{6}(-\d+)?\.db$/)
  expect(second.name).not.toBe(first.name)
  const usage = await data.usage()
  expect(usage.database).toBeGreaterThan(0)
  expect(usage.backups.map((b) => b.name).toSorted()).toEqual([first.name, second.name].toSorted())
  // The copy opens as Ariadne's own database, with the project in it.
  const copy = new Ariadne({ path: join(home, 'backups', first.name) })
  expect(copy.listProjects().map((p) => p.name)).toEqual(['备份'])
  copy.close()
  expect(() => data.removeBackup('../ariadne.db')).toThrow()
  data.removeBackup(first.name)
  expect((await data.usage()).backups.map((b) => b.name)).toEqual([second.name])
})
