import { mkdirSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'

/**
 * `bun run release`: one program per platform with the pages inside, in `dist/release/`.
 * Builds the pages, writes an entry that names every built file, compiles it with Bun and
 * compresses the result with UPX.
 *
 *   bun run release                      this machine's platform
 *   bun run release --target linux-x64,windows-x64
 *   bun run release --target all
 *   bun run release --no-upx             leave the programs uncompressed
 */

const root = join(import.meta.dirname, '../../..')
const web = join(root, 'packages/web')
const dist = join(web, 'dist')
const out = join(root, 'dist/release')

export const TARGETS = [
  'linux-x64',
  'linux-arm64',
  'darwin-x64',
  'darwin-arm64',
  'windows-x64',
] as const
export type Target = (typeof TARGETS)[number]

/** This machine's platform, when Bun builds for it. */
export function hostTarget(): Target | null {
  const os = process.platform === 'win32' ? 'windows' : process.platform
  const target = `${os}-${process.arch}`
  return (TARGETS as readonly string[]).includes(target) ? (target as Target) : null
}

export interface ReleaseOptions {
  targets: Target[]
  upx: boolean
}

export function parseArgs(argv: string[]): ReleaseOptions {
  let targets: Target[] | null = null
  let upx = true
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!
    if (arg === '--no-upx') upx = false
    else if (arg === '--target' || arg.startsWith('--target=')) {
      const value = arg === '--target' ? argv[++i] : arg.slice('--target='.length)
      if (!value) throw new Error('--target needs a value')
      targets = value === 'all' ? [...TARGETS] : value.split(',').map((t) => toTarget(t.trim()))
    } else throw new Error(`unknown argument: ${arg}`)
  }
  if (!targets) {
    const host = hostTarget()
    if (!host) throw new Error(`no build for ${process.platform}-${process.arch}; pass --target`)
    targets = [host]
  }
  return { targets: [...new Set(targets)], upx }
}

function toTarget(name: string): Target {
  if ((TARGETS as readonly string[]).includes(name)) return name as Target
  throw new Error(`unknown target: ${name} (one of ${TARGETS.join(', ')}, or all)`)
}

/** Every file under `dir`, as paths in the address: `/index.html`, `/assets/…`. */
export function listFiles(dir: string, prefix = ''): string[] {
  const files: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = `${prefix}/${entry.name}`
    if (entry.isDirectory()) files.push(...listFiles(join(dir, entry.name), path))
    else if (entry.isFile()) files.push(path)
  }
  return files.toSorted()
}

/** A path as an import specifier, with forward slashes on Windows too. */
const spec = (path: string) => JSON.stringify(path.split(sep).join('/'))

/**
 * The program's entry: every built file is imported as a file, so the compiler puts it in
 * the program, and the map from its address to where the program keeps it goes to the server.
 */
export function entrySource(dir: string, files: string[], starter: string): string {
  const lines = [`import { runRelease } from ${spec(starter)}`]
  files.forEach((file, i) => {
    lines.push(`import file${i} from ${spec(join(dir, file))} with { type: 'file' }`)
  })
  lines.push('', 'await runRelease({')
  files.forEach((file, i) => lines.push(`  ${JSON.stringify(file)}: file${i},`))
  lines.push('})', '')
  return lines.join('\n')
}

async function run(command: string[], cwd = root): Promise<void> {
  const proc = Bun.spawn(command, {
    cwd,
    stdin: 'ignore',
    stdout: 'inherit',
    stderr: 'inherit',
    windowsHide: true,
  })
  if ((await proc.exited) !== 0) throw new Error(`failed: ${command.join(' ')}`)
}

function size(path: string): string {
  return `${(statSync(path).size / 1024 / 1024).toFixed(1)} MB`
}

/** Whether UPX packs this platform's program: never macOS, which would no longer start it. */
const packs = (target: Target) => !target.startsWith('darwin')

export async function release({ targets, upx }: ReleaseOptions) {
  const needsUpx = upx && targets.some(packs)
  const upxPath = needsUpx ? Bun.which('upx') : null
  if (needsUpx && !upxPath) {
    throw new Error('upx is not on PATH: install it (https://upx.github.io), or pass --no-upx')
  }

  console.info('building the pages')
  await run(['bun', 'run', 'build'], web)
  const files = listFiles(dist)
  if (!files.includes('/index.html')) throw new Error(`no index.html in ${dist}`)

  rmSync(out, { recursive: true, force: true })
  mkdirSync(out, { recursive: true })
  const entry = join(out, 'entry.ts')
  writeFileSync(entry, entrySource(dist, files, join(import.meta.dirname, 'release.ts')))

  const host = hostTarget()
  for (const target of targets) {
    const outfile = join(out, `ariadne-${target}${target.startsWith('windows') ? '.exe' : ''}`)
    console.info(`compiling ${target}`)
    await run([
      'bun',
      'build',
      entry,
      '--compile',
      `--target=bun-${target}`,
      // Names stay as they are: errors and logs read the same as from the repository.
      '--minify-syntax',
      '--minify-whitespace',
      '--outfile',
      outfile,
    ])
    if (upx && !packs(target)) {
      console.info(`${target}: not compressed, UPX does not support macOS`)
    } else if (upxPath) {
      console.info(`compressing ${target}`)
      await run([upxPath, '--lzma', '-q', outfile])
    }
    // Only this machine's program can be run here: it must still find its pages.
    if (target === host) await run([outfile, 'self-check'])
    else console.info(`${target}: built for another platform, self-check not run`)
    console.info(`${relative(root, outfile)}  ${size(outfile)}`)
  }
  rmSync(entry)
}

if (import.meta.main) {
  try {
    await release(parseArgs(process.argv.slice(2)))
  } catch (error) {
    console.error(error instanceof Error ? error.message : error)
    process.exit(1)
  }
}
