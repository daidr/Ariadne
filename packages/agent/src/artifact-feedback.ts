import {
  lineDiff,
  type Ariadne,
  type ArtifactCommentAnchor,
  type ArtifactFeedbackItem,
  type ArtifactFeedbackView,
  type ArtifactKind,
  type WhiteboardContent,
  type WhiteboardNode,
} from '@ariadne/core'
import * as Y from 'yjs'
import type { SharedArtifacts } from './shared-artifacts'
import { bridges } from './shared-content'

/**
 * What the user does in a document or whiteboard reaches the agent that wrote it on its
 * own, in rounds: everything since the last round is sent together once the user stops for
 * a moment or leaves (the page says when, with pictures of the options they marked), and
 * at the latest `fallbackMs` after their last change when the page went away without.
 * The user never has to send anything.
 */
export class ArtifactFeedback {
  private readonly waiting = new Map<string, ReturnType<typeof setTimeout>>()
  private readonly stops: (() => void)[] = []
  /** When each conversation's running turn started. */
  private readonly turns = new Map<string, number>()
  private readonly fallbackMs: number

  constructor(
    private readonly app: Ariadne,
    private readonly shared: SharedArtifacts,
    options: { fallbackMs?: number } = {},
  ) {
    this.fallbackMs = options.fallbackMs ?? 120_000
  }

  start(): void {
    this.stops.push(
      this.shared.onUserChange(({ artifactId }) => {
        if (followed(this.app.getArtifact(artifactId).kind)) this.touched(artifactId)
      }),
    )
    this.stops.push(
      this.app.subscribe((events) => {
        for (const event of events) {
          if (event.type === 'artifact.commented' && event.data.by === 'user') {
            this.touched(event.data.artifactId)
          } else if (event.type === 'turn.started') {
            this.turns.set(event.data.conversationId, event.at)
          } else if (event.type === 'turn.finished') {
            const started = this.turns.get(event.data.conversationId)
            this.turns.delete(event.data.conversationId)
            if (event.data.outcome === 'completed' && started !== undefined) {
              this.settle(event.projectId, event.data.conversationId, started)
            }
          }
        }
      }),
    )
    // Before an agent writes, the user's part so far goes to it as a round of its own.
    this.stops.push(this.shared.onBeforeAgentWrite((artifactId) => void this.send(artifactId)))
    this.recover()
  }

  /** Send what still waits, before the rooms close: nothing the user did is left unsent. */
  stop(): void {
    for (const stop of this.stops.splice(0)) stop()
    for (const artifactId of Array.from(this.waiting.keys())) this.trySend(artifactId)
    for (const timer of this.waiting.values()) clearTimeout(timer)
    this.waiting.clear()
  }

  /**
   * What the user did before the server last stopped without it being sent (it stopped
   * abruptly) goes out now, as a round of its own.
   */
  private recover() {
    for (const project of this.app.listProjects()) {
      for (const artifact of this.app.listArtifacts(project.id)) {
        if (!followed(artifact.kind)) continue
        try {
          const items = this.itemsOf(artifact.id)
          if (items.length) this.app.sendArtifactFeedback(artifact.id, { items })
        } catch (error) {
          console.error('could not send the feedback left on', artifact.id, error)
        }
      }
    }
  }

  /**
   * A turn that saw a round ended without saying it finished it: the round counts as done,
   * so it does not look worked on forever.
   */
  private settle(projectId: string, conversationId: string, started: number) {
    try {
      for (const artifact of this.app.listArtifacts(projectId)) {
        if (!followed(artifact.kind)) continue
        for (const round of this.app.listArtifactFeedback(artifact.id)) {
          if (round.doneAt !== null || round.notify !== conversationId) continue
          if (round.createdAt > started) continue
          this.app.finishArtifactFeedback(artifact.id, round.round, conversationId, '')
        }
      }
    } catch (error) {
      console.error('could not settle the feedback rounds of', conversationId, error)
    }
  }

  private trySend(artifactId: string) {
    try {
      this.send(artifactId)
    } catch (error) {
      console.error('could not send the feedback on', artifactId, error)
    }
  }

  /** Whether the user did something in the artifact that was not sent yet. */
  pending(artifactId: string): boolean {
    return this.waiting.has(artifactId)
  }

  /**
   * Send what waits as a round, with the pictures the page took of the options the user
   * marked; nothing when nothing waits or nothing changed.
   */
  send(artifactId: string, images: string[] = []): ArtifactFeedbackView | null {
    const timer = this.waiting.get(artifactId)
    if (!timer) return null
    clearTimeout(timer)
    this.waiting.delete(artifactId)
    // What waits in the room is saved first, so the round names a version that has it.
    this.shared.save(artifactId)
    const items = this.itemsOf(artifactId)
    return this.app.sendArtifactFeedback(artifactId, { items, images })
  }

  private touched(artifactId: string) {
    const timer = this.waiting.get(artifactId)
    if (timer) clearTimeout(timer)
    this.waiting.set(
      artifactId,
      setTimeout(() => this.trySend(artifactId), this.fallbackMs),
    )
  }

  /** What the user did since the last round, or since the agent last wrote it. */
  itemsOf(artifactId: string): ArtifactFeedbackItem[] {
    const artifact = this.app.getArtifact(artifactId)
    const since = this.baseline(artifactId)
    const items: ArtifactFeedbackItem[] = []
    let board: WhiteboardContent | null = null
    if (artifact.kind === 'document') {
      // Both as the room writes Markdown, so how it is spelled is not taken for a change.
      // Untouched since then, which most are: nothing to parse.
      const diff =
        since.content === artifact.content
          ? null
          : lineDiff(normalized(since.content), normalized(artifact.content))
      if (diff) items.push({ kind: 'edit', diff })
    } else if (artifact.kind === 'whiteboard') {
      board = JSON.parse(artifact.content) as WhiteboardContent
      items.push(...boardChanges(JSON.parse(since.content) as WhiteboardContent, board))
    }
    for (const thread of this.app.listArtifactComments(artifactId)) {
      for (const [i, comment] of thread.comments.entries()) {
        if (comment.author !== 'user' || comment.createdAt <= since.at) continue
        items.push({
          kind: 'comment',
          threadId: thread.id,
          action: i === 0 ? 'create' : 'reply',
          body: comment.body,
          where: board
            ? anchorWhere(board, thread.anchor, thread.id)
            : `on the text "${thread.quote}"`,
        })
      }
    }
    return items
  }

  /**
   * Where this round starts: the artifact as the last round sent it or as the agent last
   * wrote it, whichever is newer, and when that was.
   */
  private baseline(artifactId: string): { content: string; at: number } {
    const last = this.app.listArtifactFeedback(artifactId)[0]
    const agent = this.app
      .listArtifactVersions(artifactId)
      .find((version) => version.author !== 'user')
    const fromRound = last && (!agent || last.version >= agent.version)
    const version = fromRound ? last.version : (agent?.version ?? 1)
    const at = fromRound ? last.createdAt : (agent?.createdAt ?? 0)
    return { content: this.app.getArtifact(artifactId, version).content, at }
  }
}

/**
 * The kinds whose changes go back to the agent. A dashboard's layout is the user's own to
 * arrange, and its comments are off.
 */
const followed = (kind: ArtifactKind) => kind === 'document' || kind === 'whiteboard'

/** A document's Markdown as its room writes it. */
function normalized(markdown: string): string {
  const doc = new Y.Doc()
  try {
    bridges.document.seed(doc, markdown)
    return bridges.document.read(doc)
  } finally {
    doc.destroy()
  }
}

// ---- whiteboards in words ----

const TEXT_KINDS: Record<string, string> = {
  ink: 'drawing',
  shape: 'shape',
  sticky: 'sticky note',
  text: 'text',
  image: 'image',
  default: 'box',
  group: 'group',
  option: 'option',
}

interface Rect {
  x: number
  y: number
  width: number
  height: number
}

function rectOf(node: WhiteboardNode): Rect {
  return { x: node.x, y: node.y, width: node.width ?? 160, height: node.height ?? 90 }
}

const textOf = (node: WhiteboardNode) => (typeof node.data?.text === 'string' ? node.data.text : '')
const titleOf = (node: WhiteboardNode) =>
  typeof node.data?.title === 'string' ? node.data.title : node.id

const inside = (x: number, y: number, r: Rect) =>
  x >= r.x && x <= r.x + r.width && y >= r.y && y <= r.y + r.height

/** The option a node is in: by its container, or by where it lies. */
function optionOf(board: WhiteboardContent, node: WhiteboardNode): WhiteboardNode | null {
  const byId = new Map(board.nodes.map((n) => [n.id, n]))
  for (let at: WhiteboardNode | undefined = node; at;) {
    if (at.type === 'option' && at !== node) return at
    at = typeof at.parent === 'string' ? byId.get(at.parent) : undefined
  }
  const r = rectOf(node)
  const cx = r.x + r.width / 2
  const cy = r.y + r.height / 2
  return (
    board.nodes.find((n) => n.type === 'option' && n !== node && inside(cx, cy, rectOf(n))) ?? null
  )
}

/** Where a node is, in words: its option, and the spot on a screenshot under it. */
export function whereOn(board: WhiteboardContent, node: WhiteboardNode): string {
  if (node.type === 'option') return `on option "${titleOf(node)}" (${node.id}) as a whole`
  const option = optionOf(board, node)
  const r = rectOf(node)
  const cx = r.x + r.width / 2
  const cy = r.y + r.height / 2
  const image =
    node.type === 'image'
      ? null
      : board.nodes.find((n) => n.type === 'image' && n !== node && inside(cx, cy, rectOf(n)))
  const place = option
    ? `in option "${titleOf(option)}" (${option.id})`
    : 'on the board, in no option'
  if (node.type === 'image') return `${place}, the image ${node.id}`
  if (!image) return place
  const box = rectOf(image)
  const px = Math.round(((cx - box.x) / box.width) * 100)
  const py = Math.round(((cy - box.y) / box.height) * 100)
  const alt = typeof image.data?.alt === 'string' && image.data.alt ? ` "${image.data.alt}"` : ''
  return `${place}, over the image${alt} (${image.id}) at ${px}% across, ${py}% down`
}

/** Where a point of the board is, in words: on which screenshot, how far across and down. */
function pointOn(board: WhiteboardContent, node: WhiteboardNode, x: number, y: number): string {
  const image =
    node.type === 'image'
      ? node
      : board.nodes.find((n) => n.type === 'image' && n !== node && inside(x, y, rectOf(n)))
  const box = rectOf(image ?? node)
  const px = Math.round(((x - box.x) / box.width) * 100)
  const py = Math.round(((y - box.y) / box.height) * 100)
  if (!image) return `at ${px}% across, ${py}% down of ${node.id}`
  const alt = typeof image.data?.alt === 'string' && image.data.alt ? ` "${image.data.alt}"` : ''
  return `on the image${alt} (${image.id}) at ${px}% across, ${py}% down`
}

/**
 * Where a comment thread is on a whiteboard, in words: the item it is on, its option, and
 * for one pinned to a point of the item, that point on the screenshot there.
 */
export function anchorWhere(
  board: WhiteboardContent,
  anchor: ArtifactCommentAnchor | null,
  threadId?: string,
): string {
  if (!anchor) return 'on the board'
  if ('edge' in anchor) return `on the connector ${anchor.edge}`
  const node = board.nodes.find((n) => n.id === anchor.node)
  if (!node) return `on ${anchor.node}, which is gone now`
  const pins = node.commentPins as Record<string, { x: number; y: number }> | undefined
  const pin = threadId ? pins?.[threadId] : undefined
  if (pin) {
    const r = rectOf(node)
    const option = node.type === 'option' ? node : optionOf(board, node)
    const place = option ? `in option "${titleOf(option)}" (${option.id})` : 'on the board'
    return `pinned ${place}, ${pointOn(board, node, r.x + pin.x * r.width, r.y + pin.y * r.height)}`
  }
  const what = TEXT_KINDS[node.type] ?? node.type
  const text = textOf(node)
  return node.type === 'option'
    ? whereOn(board, node)
    : `on the ${what}${text ? ` "${text}"` : ''} (${node.id}) ${whereOn(board, node)}`
}

const chosen = (board: WhiteboardContent) =>
  board.nodes.filter((n) => n.type === 'option' && n.data?.chosen === true)

/** What the user changed on a whiteboard: what they picked, put on it, removed or rewrote. */
export function boardChanges(
  before: WhiteboardContent,
  after: WhiteboardContent,
): ArtifactFeedbackItem[] {
  const items: ArtifactFeedbackItem[] = []
  const was = chosen(before).map((n) => n.id)
  const now = chosen(after)
  if (was.join() !== now.map((n) => n.id).join()) {
    items.push({ kind: 'choice', chosen: now.map((n) => ({ id: n.id, title: titleOf(n) })) })
  }
  const old = new Map(before.nodes.map((n) => [n.id, n]))
  const fresh = new Set(after.nodes.map((n) => n.id))
  const changes: string[] = []
  for (const node of after.nodes) {
    const previous = old.get(node.id)
    if (!previous) {
      items.push({
        kind: 'mark',
        nodeId: node.id,
        type: TEXT_KINDS[node.type] ?? node.type,
        text: textOf(node),
        where: whereOn(after, node),
      })
    } else if (textOf(previous) !== textOf(node) && node.type !== 'option') {
      changes.push(`They rewrote ${node.id} from "${textOf(previous)}" to "${textOf(node)}".`)
    } else if (node.type === 'option' && titleOf(previous) !== titleOf(node)) {
      changes.push(`They renamed option ${node.id} to "${titleOf(node)}".`)
    }
  }
  for (const node of before.nodes) {
    if (!fresh.has(node.id)) {
      const text = textOf(node) || (node.type === 'option' ? titleOf(node) : '')
      changes.push(
        `They removed the ${TEXT_KINDS[node.type] ?? node.type} ${node.id}${text ? ` "${text}"` : ''}.`,
      )
    }
  }
  if (changes.length) items.push({ kind: 'board', changes })
  return items
}
