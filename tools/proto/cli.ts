import { createHash } from 'node:crypto'
import { mkdtemp, readdir, readFile, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, relative, sep } from 'node:path'

const root = join(import.meta.dir, '../..')
const generatedDirs = ['internal/gen/proto', 'packages/proto/src/gen']

async function run(args: string[], cwd = root): Promise<void> {
  const proc = Bun.spawn(args, {
    cwd,
    stdin: 'inherit',
    stdout: 'inherit',
    stderr: 'inherit',
    windowsHide: true,
  })
  const code = await proc.exited
  if (code !== 0) {
    throw new Error(`command failed (${code}): ${args.join(' ')}`)
  }
}

async function collectFiles(baseDir: string): Promise<Map<string, string>> {
  const result = new Map<string, string>()
  async function walk(dir: string): Promise<void> {
    let entries
    try {
      entries = await readdir(dir, { withFileTypes: true })
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return
      throw error
    }
    for (const entry of entries) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        await walk(full)
      } else if (entry.isFile()) {
        const content = await readFile(full, 'utf8')
        const normalized = content.replaceAll('\r\n', '\n')
        const digest = createHash('sha256').update(normalized).digest('hex')
        const key = relative(baseDir, full).split(sep).join('/')
        result.set(key, digest)
      }
    }
  }
  for (const rel of generatedDirs) {
    const target = join(baseDir, rel)
    try {
      const info = await stat(target)
      if (info.isDirectory()) {
        await walk(target)
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
    }
  }
  return result
}

async function generate(): Promise<void> {
  await run(['bun', 'x', 'buf', 'lint'])
  await run(['bun', 'x', 'buf', 'generate'])
  await run(['bun', 'fmt', 'packages/proto/src/gen'])
}

async function check(): Promise<void> {
  await run(['bun', 'x', 'buf', 'lint'])
  const temp = await mkdtemp(join(tmpdir(), 'ariadne-proto-check-'))
  try {
    await run(['bun', 'x', 'buf', 'generate', '--output', temp])
    await run(['bun', 'fmt', join(temp, 'packages/proto/src/gen')])
    const expected = await collectFiles(temp)
    const actual = await collectFiles(root)
    const diffs: string[] = []
    for (const [file, hash] of expected) {
      const current = actual.get(file)
      if (current === undefined) {
        diffs.push(`missing: ${file}`)
      } else if (current !== hash) {
        diffs.push(`outdated: ${file}`)
      }
    }
    for (const file of actual.keys()) {
      if (!expected.has(file)) {
        diffs.push(`stale: ${file}`)
      }
    }
    if (diffs.length > 0) {
      diffs.sort()
      console.error('Generated Protobuf code is out of sync. Run `bun run proto:generate`:')
      for (const diff of diffs) console.error(`  ${diff}`)
      process.exit(1)
    }
  } finally {
    await rm(temp, { recursive: true, force: true })
  }
}

const command = process.argv[2]
if (command === 'generate') {
  await generate()
} else if (command === 'check') {
  await check()
} else {
  console.error('Usage: bun tools/proto/cli.ts <generate|check>')
  process.exit(2)
}
