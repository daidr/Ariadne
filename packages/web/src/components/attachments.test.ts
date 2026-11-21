import { describe, expect, test } from 'vitest'
import { isInlineImage, shownAttachments } from './attachments'

describe('attachments display helpers', () => {
  test('only allows safe inline raster image MIME types', () => {
    expect(isInlineImage('image/png')).toBe(true)
    expect(isInlineImage('image/webp')).toBe(true)
    expect(isInlineImage('image/svg+xml')).toBe(false)
    expect(isInlineImage('application/zip')).toBe(false)
    expect(isInlineImage(undefined)).toBe(false)
  })

  test('sets thumbnail url only for inline image attachments', () => {
    const shown = shownAttachments([
      { fileId: 'fil_img', name: 'shot.png', mime: 'image/png', size: 1200 },
      { fileId: 'fil_zip', name: 'bundle.zip', mime: 'application/zip', size: 4096 },
      { fileId: 'fil_svg', name: 'logo.svg', mime: 'text/plain', size: 512 },
      { fileId: 'fil_fake', name: 'broken.png', mime: 'application/octet-stream', size: 64 },
    ])
    expect(shown[0]?.url).toBe('/api/files/fil_img')
    expect(shown[1]?.url).toBeUndefined()
    expect(shown[2]?.url).toBeUndefined()
    expect(shown[3]?.url).toBeUndefined()
  })
})
