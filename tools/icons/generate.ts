import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { Resvg } from '@resvg/resvg-js'

const ROOT = resolve(import.meta.dirname, '..', '..')
const BRANDING_DIR = resolve(ROOT, 'packages/web/branding')
const PUBLIC_DIR = resolve(ROOT, 'packages/web/public')

function renderPng(svg: string, size: number): Buffer {
  const resvg = new Resvg(svg, {
    fitTo: { mode: 'width', value: size },
  })
  return Buffer.from(resvg.render().asPng())
}

/** Pack PNG frames into a standard multi-resolution ICO file. */
function buildIco(frames: { size: number; png: Buffer }[]): Buffer {
  const headerSize = 6
  const entrySize = 16
  let offset = headerSize + entrySize * frames.length
  const parts: Buffer[] = []

  const header = Buffer.alloc(headerSize)
  header.writeUInt16LE(0, 0) // reserved
  header.writeUInt16LE(1, 2) // type = ICO
  header.writeUInt16LE(frames.length, 4)
  parts.push(header)

  for (const frame of frames) {
    const entry = Buffer.alloc(entrySize)
    const dim = frame.size >= 256 ? 0 : frame.size
    entry.writeUInt8(dim, 0)
    entry.writeUInt8(dim, 1)
    entry.writeUInt8(0, 2) // palette
    entry.writeUInt8(0, 3) // reserved
    entry.writeUInt16LE(1, 4) // color planes
    entry.writeUInt16LE(32, 6) // bits per pixel
    entry.writeUInt32LE(frame.png.length, 8)
    entry.writeUInt32LE(offset, 12)
    parts.push(entry)
    offset += frame.png.length
  }

  for (const frame of frames) {
    parts.push(frame.png)
  }

  return Buffer.concat(parts)
}

async function writeAsset(relativePath: string, content: Buffer | string) {
  const fullPath = resolve(PUBLIC_DIR, relativePath)
  await mkdir(dirname(fullPath), { recursive: true })
  await writeFile(fullPath, content)
}

async function main() {
  const [iconSvg, markSvg] = await Promise.all([
    readFile(resolve(BRANDING_DIR, 'icon.svg'), 'utf8'),
    readFile(resolve(BRANDING_DIR, 'mark.svg'), 'utf8'),
  ])

  const png16 = renderPng(iconSvg, 16)
  const png32 = renderPng(iconSvg, 32)
  const png180 = renderPng(iconSvg, 180)
  const png192 = renderPng(iconSvg, 192)
  const png512 = renderPng(iconSvg, 512)

  await Promise.all([
    writeAsset('favicon.svg', markSvg),
    writeAsset('favicon-32x32.png', png32),
    writeAsset(
      'favicon.ico',
      buildIco([
        { size: 16, png: png16 },
        { size: 32, png: png32 },
      ]),
    ),
    writeAsset('apple-touch-icon.png', png180),
    writeAsset('icons/icon-192.png', png192),
    writeAsset('icons/icon-512.png', png512),
    writeAsset('icons/icon-maskable-512.png', png512),
  ])

  console.info('Generated web icons in packages/web/public')
}

await main()
