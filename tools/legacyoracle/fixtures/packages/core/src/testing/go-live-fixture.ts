import { LiveCommands, ReplyDrafts, screenText } from '../live'

const input = (await Bun.stdin.json()) as {
  actions: { op: string; handle?: string; text?: string }[]
  screens: string[]
}
const live = new LiveCommands(() => 1_800_000_000_000)
const drafts = new ReplyDrafts(() => 1_800_000_000_000)
const liveHandles = new Map<string, ReturnType<LiveCommands['start']>>()
const draftHandles = new Map<string, ReturnType<ReplyDrafts['start']>>()
const states = []
for (const action of input.actions) {
  const handle = action.handle ?? ''
  switch (action.op) {
    case 'live.start':
      liveHandles.set(
        handle,
        live.start({ projectId: 'p', conversationId: 'c', command: handle, cwd: '.' }),
      )
      break
    case 'live.output':
      liveHandles.get(handle)!.output(action.text ?? '')
      break
    case 'live.end':
      liveHandles.get(handle)!.end()
      break
    case 'draft.start':
      draftHandles.set(
        handle,
        drafts.start({ projectId: 'p', conversationId: 'c', messageId: handle }),
      )
      break
    case 'draft.write':
      draftHandles.get(handle)!.write(action.text ?? '')
      break
    case 'draft.posted':
      draftHandles.get(handle)!.posted()
      break
    case 'draft.withdraw':
      draftHandles.get(handle)!.withdraw()
      break
    case 'draft.drop':
      drafts.drop('c')
      break
    case 'wait':
      await Bun.sleep(320)
      break
  }
  states.push(JSON.parse(JSON.stringify({ live: live.list(), drafts: drafts.list() })) as unknown)
}
for (const handle of liveHandles.values()) handle.end()
drafts.drop('c')
process.stdout.write(JSON.stringify({ states, screens: input.screens.map(screenText) }))
