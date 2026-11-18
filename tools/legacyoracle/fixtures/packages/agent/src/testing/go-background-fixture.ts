import { BackgroundCommands, servedUrl } from '../background'

const input = (await Bun.stdin.json()) as { urls: string[]; chunks: string[] }
Date.now = () => 1_800_000_000_000
const background = new BackgroundCommands()
let append: ((text: string, stream: 'stdout' | 'stderr') => void) | undefined
let finish: ((value: { exitCode: number; stdout: string; stderr: string }) => void) | undefined
const command = background.start(
  'thread',
  'serve',
  (options) => {
    append = options.onOutput
    return new Promise((resolve) => {
      finish = resolve
    })
  },
  { local: true, keep: true, cwd: 'sub' },
)
const reads = []
for (const chunk of input.chunks) {
  append!(chunk, 'stdout')
  reads.push(await background.read('thread', command.id))
}
const peek = background.peek('thread', command.id)
await background.stopThread('thread', { keepAsked: true })
const kept = background.takeKept('thread')
finish!({ exitCode: 0, stdout: '', stderr: '' })
await background.wait('thread', command.id)
const ended = background.list('thread')
await background.stopThread('thread')
process.stdout.write(
  JSON.stringify({
    command,
    reads,
    peek,
    kept,
    ended,
    forgotten: background.list('thread'),
    urls: input.urls.map(servedUrl),
  }),
)
