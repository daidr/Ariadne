import { FileTooLargeError, WorkspacePathError, type Workspace } from '@ariadne/plugin-sdk'
import { describe, expect, it } from 'vitest'
import { keepMessageImages } from './message-images'

const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0, 0, 0, 0, 0, 0])

function workspace(files: Record<string, Uint8Array | 'large' | 'outside'>) {
  const read: string[] = []
  const ws = {
    async readBytes(path: string) {
      read.push(path)
      const file = files[path]
      if (file === 'large') throw new FileTooLargeError(path, 20, 10)
      if (file === 'outside' || !file) throw new WorkspacePathError(path)
      return file
    },
  } as unknown as Workspace
  return { ws, read }
}

function saver() {
  const saved: { mime: string; data: Uint8Array }[] = []
  return {
    saved,
    save: (file: { mime: string; data: Uint8Array }) => {
      saved.push(file)
      return { id: `fil_${saved.length}` }
    },
  }
}

describe('message images', () => {
  it('keeps workspace images and links the copies', async () => {
    const { ws } = workspace({ 'shots/a.png': PNG, 'shots/b c.png': PNG })
    const { saved, save } = saver()
    const content = await keepMessageImages(
      '截图：![用量](shots/a.png) 和 [另一张](<shots/b c.png> "标题")，再看 ![用量](./shots/missing.png)',
      async () => ws,
      save,
    )
    expect(content).toBe(
      '截图：![用量](file:fil_1) 和 [另一张](file:fil_2 "标题")，再看 ![用量](./shots/missing.png)',
    )
    expect(saved.map((file) => file.mime)).toEqual(['image/png', 'image/png'])
  })

  it('leaves code, addresses, other files and fakes alone', async () => {
    const { ws, read } = workspace({
      'fake.png': new TextEncoder().encode('not an image'),
      'big.png': 'large',
      '/etc/x.png': 'outside',
    })
    const { saved, save } = saver()
    const content = [
      '`![a](code.png)`',
      '```\n![b](block.png)\n```',
      '![c](https://example.com/c.png)',
      '![d](file:fil_old)',
      '[e](notes.md)',
      '![f](fake.png) ![g](big.png) ![h](/etc/x.png)',
    ].join('\n')
    expect(await keepMessageImages(content, async () => ws, save)).toBe(content)
    expect(read).toEqual(['fake.png', 'big.png', '/etc/x.png'])
    expect(saved).toEqual([])
  })

  it('opens no workspace for a message without images', async () => {
    let opened = false
    const content = 'Nothing to see, [a link](https://example.com).'
    const out = await keepMessageImages(
      content,
      async () => {
        opened = true
        throw new Error('no workspace')
      },
      () => ({ id: 'x' }),
    )
    expect(out).toBe(content)
    expect(opened).toBe(false)
  })

  it('keeps the text when there is no workspace', async () => {
    const content = '![a](a.png)'
    expect(
      await keepMessageImages(
        content,
        () => Promise.reject(new Error('none')),
        () => ({ id: 'x' }),
      ),
    ).toBe(content)
  })
})
