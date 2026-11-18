import { LocalWorkspace } from '../index'

const input = (await Bun.stdin.json()) as { root: string; files: string[] }
const workspace = new LocalWorkspace('source', input.root, null, { git: false })
const entries = await workspace.list('.')
const text: Record<string, string> = {}
for (const file of input.files) text[file] = await workspace.readFile(file)
console.info(JSON.stringify({ entries, text }))
