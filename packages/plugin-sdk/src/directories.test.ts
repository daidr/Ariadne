import { mkdir, mkdtemp, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, posix } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { browseLocal, browseTyped, DirectoryNotFound, type ListDirectory } from './directories'

describe('a typed path', () => {
  const tree: Record<string, string[]> = {
    '/home/me': ['src', 'Projects', '.config', 'notes'],
    '/home/me/Projects': ['ariadne', 'Akasha', 'dou-ui'],
    '/': ['home', 'srv'],
  }
  const list: ListDirectory = async (path) => {
    const real = path === '' ? '/home/me' : path.replace(/\/+$/, '') || '/'
    const names = tree[real]
    return names ? { path: real, names } : null
  }
  const browse = (typed: string) => browseTyped(typed, list, posix)

  test('lists the directory it names, with the one above', async () => {
    expect(await browse('/home/me/Projects')).toEqual({
      path: '/home/me/Projects',
      parent: '/home/me',
      directories: [
        { name: 'Akasha', path: '/home/me/Projects/Akasha' },
        { name: 'ariadne', path: '/home/me/Projects/ariadne' },
        { name: 'dou-ui', path: '/home/me/Projects/dou-ui' },
      ],
    })
  })

  test('part way through a name, lists the ones starting like it', async () => {
    const listing = await browse('/home/me/Projects/a')
    expect(listing.path).toBe('/home/me/Projects')
    expect(listing.directories.map((entry) => entry.name)).toEqual(['Akasha', 'ariadne'])
  })

  test('starts at home when nothing absolute is typed, without hidden directories', async () => {
    for (const typed of ['', '  ', 'relative']) {
      const listing = await browse(typed)
      expect(listing.path).toBe('/home/me')
      expect(listing.directories.map((entry) => entry.name)).toEqual(['notes', 'Projects', 'src'])
    }
  })

  test('shows hidden directories once a dot is typed', async () => {
    const listing = await browse('/home/me/.c')
    expect(listing.directories.map((entry) => entry.name)).toEqual(['.config'])
  })

  test('has nothing above the root', async () => {
    const listing = await browse('/')
    expect(listing.parent).toBeNull()
    expect(listing.directories.map((entry) => entry.path)).toEqual(['/home', '/srv'])
  })

  test('a slash at the end does not filter by the parent', async () => {
    await expect(browse('/nowhere/')).rejects.toThrow(DirectoryNotFound)
    await expect(browse('/nowhere/at/all')).rejects.toThrow(DirectoryNotFound)
  })
})

describe('directories on this machine', () => {
  let base: string
  beforeEach(async () => {
    base = await realpath(await mkdtemp(join(tmpdir(), 'ariadne-dirs-')))
  })
  afterEach(async () => {
    await rm(base, { recursive: true, force: true })
  })

  test('lists directories and links to them, not files', async () => {
    await mkdir(join(base, 'b'))
    await mkdir(join(base, 'a'))
    await writeFile(join(base, 'file.txt'), '')
    await symlink(join(base, 'a'), join(base, 'link'))
    await symlink(join(base, 'file.txt'), join(base, 'file-link'))
    const listing = await browseLocal(base)
    expect(listing.path).toBe(base)
    expect(listing.directories.map((entry) => entry.name)).toEqual(['a', 'b', 'link'])
    expect(listing.directories[0]!.path).toBe(join(base, 'a'))
  })

  test('a file lists its directory, filtered by its name', async () => {
    await mkdir(join(base, 'notes'))
    await writeFile(join(base, 'note.txt'), '')
    const listing = await browseLocal(join(base, 'note'))
    expect(listing.path).toBe(base)
    expect(listing.directories.map((entry) => entry.name)).toEqual(['notes'])
  })
})
