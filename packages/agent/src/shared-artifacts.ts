import type { Ariadne, ArtifactKind, ArtifactView } from '@ariadne/core'
import * as decoding from 'lib0/decoding'
import * as encoding from 'lib0/encoding'
import * as awarenessProtocol from 'y-protocols/awareness'
import * as syncProtocol from 'y-protocols/sync'
import * as Y from 'yjs'
import { bridges, sharedKinds, type SharedEvent, type SharedKind } from './shared-content'

/**
 * Documents and whiteboards people and agents edit at the same time. Each one open
 * somewhere is a room: a Yjs document on the server that the pages join over a WebSocket
 * (the y-websocket protocol: sync and awareness) and that agents write into directly.
 * Its state is kept in the database as it changes, and saved as a version of the artifact
 * once the changes stop for a moment, or before someone else starts writing.
 */

const MESSAGE_SYNC = 0
const MESSAGE_AWARENESS = 1

/** One page joined to a room. `send` takes a binary frame; `close` ends the socket. */
export interface SharedSocket {
  send(data: Uint8Array): void
  close(code?: number, reason?: string): void
}

/** Who made a change: the user on a page, or an agent by its conversation. */
type Origin = { user: SharedSocket } | { agent: string } | 'load'

/** How the others see an agent that is writing: its name at its cursor or outline. */
export interface Writer {
  name: string
}

/**
 * An agent writing an artifact piece by piece, while the model is still producing the
 * change: each `write` gives the content as far as it has got, and the page shows it land
 * with the agent's cursor there. It ends when the agent saves the artifact (`write` of the
 * room, which takes it from here), or with `cancel`, which takes back all it wrote.
 */
export interface LiveWrite {
  /**
   * The artifact's newest version when the writing began, and what it holds: what each
   * `write` is worked out from.
   */
  readonly version: number
  readonly base: string
  write(next: string): void
  cancel(): void
}

interface Live extends LiveWrite {
  by: string
  origin: Origin
  /** What was written last: the room goes from this to the next. */
  last: string
  /** Takes the writing back, leaving what others did meanwhile. */
  undo: Y.UndoManager
}

/** The color of an agent's cursor and outlines: the theme's own, in either mode. */
const WRITER_COLOR = 'var(--dou-color-primary)'
/** How long the cursor stays where the agent wrote last. */
const LINGER_MS = 2500

interface Room {
  artifactId: string
  projectId: string
  kind: SharedKind
  doc: Y.Doc
  awareness: awarenessProtocol.Awareness
  /** The pages here, with the awareness clients each one brought. */
  sockets: Map<SharedSocket, Set<number>>
  /** Who changed it since the last version: `user` or a conversation id. */
  pending: string | null
  /** The newest version the room's content is saved as. */
  version: number
  stateTimer: ReturnType<typeof setTimeout> | null
  versionTimer: ReturnType<typeof setTimeout> | null
  /** The agent writing it piece by piece now. */
  live: Live | null
  /** Takes the agent's cursor away a moment after it wrote last. */
  presenceTimer: ReturnType<typeof setTimeout> | null
}

export interface SharedArtifactsOptions {
  /** How long the changes stay unsaved as a version after the last one. */
  versionDelayMs?: number
  /** How long the state waits before it is written to the database. */
  stateDelayMs?: number
}

/** Something the user changed in a room, for the feedback that follows them. */
export type UserChange = { artifactId: string; projectId: string }

export class SharedArtifacts {
  private readonly rooms = new Map<string, Room>()
  private readonly versionDelayMs: number
  private readonly stateDelayMs: number
  private readonly userListeners = new Set<(change: UserChange) => void>()
  private readonly writeListeners = new Set<(artifactId: string) => void>()

  constructor(
    private readonly app: Ariadne,
    options: SharedArtifactsOptions = {},
  ) {
    this.versionDelayMs = options.versionDelayMs ?? 5000
    this.stateDelayMs = options.stateDelayMs ?? 1000
  }

  /** Whether an artifact of this kind is edited together. */
  static shared(kind: ArtifactKind): kind is SharedKind {
    return (sharedKinds as readonly string[]).includes(kind)
  }

  /** Hear about each change the user makes in a room. */
  onUserChange(listener: (change: UserChange) => void): () => void {
    this.userListeners.add(listener)
    return () => this.userListeners.delete(listener)
  }

  /** Hear just before an agent writes an artifact, while the user's edits are still theirs. */
  onBeforeAgentWrite(listener: (artifactId: string) => void): () => void {
    this.writeListeners.add(listener)
    return () => this.writeListeners.delete(listener)
  }

  /** Save the edits waiting in an artifact's room as a version now. */
  save(artifactId: string): void {
    const room = this.rooms.get(artifactId)
    if (room) this.saveVersion(room)
  }

  /** A page joins the room of an artifact; returns what to call with its frames and on close. */
  join(artifactId: string, socket: SharedSocket) {
    const room = this.room(artifactId)
    room.sockets.set(socket, new Set())
    const encoder = encoding.createEncoder()
    encoding.writeVarUint(encoder, MESSAGE_SYNC)
    syncProtocol.writeSyncStep1(encoder, room.doc)
    socket.send(encoding.toUint8Array(encoder))
    const states = room.awareness.getStates()
    if (states.size) {
      socket.send(
        awarenessFrame(awarenessProtocol.encodeAwarenessUpdate(room.awareness, [...states.keys()])),
      )
    }
    return {
      message: (data: Uint8Array) => this.receive(room, socket, data),
      close: () => this.leave(room, socket),
    }
  }

  /**
   * The artifact as it reads now: from its room when it has one (the edits not yet saved
   * as a version included), otherwise its newest version.
   */
  content(artifactId: string): string {
    const room = this.rooms.get(artifactId)
    return room ? read(room) : this.app.getArtifact(artifactId).content
  }

  /**
   * An agent writes the artifact: `next` is the whole content it wants, which goes into the
   * room as the changes from what it holds now, so what others are editing elsewhere in
   * it stays; with `base`, only its changes from `base`. Saved as a version by the agent at once; edits waiting to be saved are saved
   * first, as their author's.
   */
  write(
    artifactId: string,
    by: string,
    next: string | null,
    input: { reason: string; title?: string; base?: string; writer?: Writer },
  ): ArtifactView {
    const room = this.room(artifactId)
    // What the agent wrote piece by piece is its own already: the rest goes on top.
    const live = room.live?.by === by ? room.live : null
    if (by !== 'user' && !live) for (const listener of this.writeListeners) listener(artifactId)
    if (!live || room.pending !== by) this.saveVersion(room)
    // `null`: only the title changes.
    if (next !== null) {
      const base = live ? live.last : input.base
      this.change(room, { agent: by }, input.writer, () =>
        this.bridge(room).update(room.doc, next, base),
      )
    }
    if (live) this.endLive(room)
    const saved = this.saveVersion(room, {
      reason: input.reason,
      title: input.title,
      force: !!input.title,
    })
    // Nobody has it open: the room was only for this write.
    if (!room.sockets.size) this.close(room)
    return saved
  }

  /**
   * An agent starts writing the artifact piece by piece. `null` when another writing of it
   * is going on, or nobody has it open to watch.
   */
  live(artifactId: string, by: string, writer: Writer): LiveWrite | null {
    const room = this.rooms.get(artifactId)
    if (!room?.sockets.size || room.live) return null
    for (const listener of this.writeListeners) listener(artifactId)
    // What the user was changing is theirs: saved before the agent's first piece.
    this.saveVersion(room)
    const origin: Origin = { agent: by }
    // The version as stored, which is what the agent read; the room holds the same.
    const { content: base, version } = this.app.getArtifact(artifactId)
    const live: Live = {
      by,
      origin,
      version,
      base,
      last: base,
      undo: new Y.UndoManager(this.bridge(room).parts(room.doc), {
        trackedOrigins: new Set([origin]),
        // One step, however long the writing takes.
        captureTimeout: Number.MAX_SAFE_INTEGER,
      }),
      write: (next) => {
        if (room.live !== live || next === live.last) return
        try {
          this.change(room, origin, writer, () =>
            this.bridge(room).update(room.doc, next, live.last),
          )
          live.last = next
        } catch {
          // A piece that does not read yet (half a table, a connector to nowhere) waits.
        }
      },
      cancel: () => {
        if (room.live !== live) return
        while (live.undo.undoStack.length) live.undo.undo()
        // Nothing of the agent's is left to save.
        if (room.pending === by) room.pending = null
        this.endLive(room)
        this.setPresence(room, null)
        if (!room.sockets.size) this.close(room)
      },
    }
    room.live = live
    return live
  }

  /** What the agent wrote into the artifact piece by piece and has not saved yet, if any. */
  writing(artifactId: string, by: string): LiveWrite | null {
    const live = this.rooms.get(artifactId)?.live
    return live?.by === by ? live : null
  }

  /** Save what waits as a version now, e.g. before the server stops. */
  flush(): void {
    for (const room of this.rooms.values()) {
      this.saveVersion(room)
      this.saveState(room)
    }
  }

  dispose(): void {
    this.flush()
    for (const room of this.rooms.values()) {
      for (const socket of room.sockets.keys()) socket.close(1001, 'server stopping')
      room.awareness.destroy()
      room.doc.destroy()
    }
    this.rooms.clear()
  }

  // ---- writing ----

  /**
   * Change the room as an agent, and show where: the parts it touched give the cursor or
   * outline the others see under the writer's name, for a moment after the last change.
   */
  private change(room: Room, origin: Origin, writer: Writer | undefined, run: () => void) {
    const bridge = this.bridge(room)
    // Read as the change is made: Yjs only tells what changed while it does.
    let presence: Record<string, unknown> | null = null
    const watch = (events: SharedEvent[]) => {
      presence = bridge.presence(room.doc, events) ?? presence
    }
    const parts = writer && room.sockets.size ? bridge.parts(room.doc) : []
    for (const part of parts) part.observeDeep(watch)
    try {
      room.doc.transact(run, origin)
    } finally {
      for (const part of parts) part.unobserveDeep(watch)
    }
    if (writer && presence) {
      this.setPresence(room, { user: { ...writer, color: WRITER_COLOR }, ...(presence as object) })
    }
  }

  private setPresence(room: Room, state: Record<string, unknown> | null) {
    if (room.presenceTimer) clearTimeout(room.presenceTimer)
    room.presenceTimer = null
    room.awareness.setLocalState(state)
    if (!state) return
    room.presenceTimer = setTimeout(() => {
      room.presenceTimer = null
      // While the writing goes on the cursor stays, however long the model thinks.
      if (!room.live) room.awareness.setLocalState(null)
    }, LINGER_MS)
  }

  private endLive(room: Room) {
    const live = room.live
    if (!live) return
    room.live = null
    live.undo.destroy()
    // The cursor goes a moment after, as after any write.
    if (room.awareness.getLocalState()) this.setPresence(room, room.awareness.getLocalState())
  }

  // ---- rooms ----

  private bridge(room: Room) {
    return bridges[room.kind]
  }

  private room(artifactId: string): Room {
    const open = this.rooms.get(artifactId)
    if (open) return open
    const artifact = this.app.getArtifact(artifactId)
    if (!SharedArtifacts.shared(artifact.kind)) {
      throw new Error(`a ${artifact.kind} is not edited together`)
    }
    const doc = new Y.Doc()
    const stored = this.app.readSharedState(artifactId)
    const bridge = bridges[artifact.kind]
    // A state saved as an older version is not what the artifact holds any more.
    if (stored && stored.version === artifact.version) Y.applyUpdate(doc, stored.state, 'load')
    else doc.transact(() => bridge.seed(doc, artifact.content), 'load')
    const room: Room = {
      artifactId,
      projectId: artifact.projectId,
      kind: artifact.kind,
      doc,
      awareness: new awarenessProtocol.Awareness(doc),
      sockets: new Map(),
      pending: null,
      version: artifact.version,
      stateTimer: null,
      versionTimer: null,
      live: null,
      presenceTimer: null,
    }
    // The server is not someone people edit with.
    room.awareness.setLocalState(null)
    doc.on('update', (update: Uint8Array, origin: Origin) => this.changed(room, update, origin))
    room.awareness.on(
      'update',
      (
        { added, updated, removed }: Record<'added' | 'updated' | 'removed', number[]>,
        origin: unknown,
      ) => {
        const ids = origin instanceof Object && room.sockets.get(origin as SharedSocket)
        if (ids) {
          for (const id of added) ids.add(id)
          for (const id of removed) ids.delete(id)
        }
        const changed = [...added, ...updated, ...removed]
        const frame = awarenessFrame(
          awarenessProtocol.encodeAwarenessUpdate(room.awareness, changed),
        )
        for (const socket of room.sockets.keys()) socket.send(frame)
      },
    )
    this.rooms.set(artifactId, room)
    if (!stored || stored.version !== artifact.version) this.saveState(room)
    return room
  }

  private receive(room: Room, socket: SharedSocket, data: Uint8Array) {
    const decoder = decoding.createDecoder(data)
    const type = decoding.readVarUint(decoder)
    if (type === MESSAGE_SYNC) {
      const encoder = encoding.createEncoder()
      encoding.writeVarUint(encoder, MESSAGE_SYNC)
      syncProtocol.readSyncMessage(decoder, encoder, room.doc, { user: socket } satisfies Origin)
      if (encoding.length(encoder) > 1) socket.send(encoding.toUint8Array(encoder))
    } else if (type === MESSAGE_AWARENESS) {
      awarenessProtocol.applyAwarenessUpdate(
        room.awareness,
        decoding.readVarUint8Array(decoder),
        socket,
      )
    }
  }

  private leave(room: Room, socket: SharedSocket) {
    const ids = room.sockets.get(socket)
    room.sockets.delete(socket)
    if (ids?.size) awarenessProtocol.removeAwarenessStates(room.awareness, [...ids], null)
    // The last page left: the room closes.
    if (!room.sockets.size) this.close(room)
  }

  /** What waits is saved, and the room closes. */
  private close(room: Room) {
    // Left half written with nobody there to watch: the agent's save writes it whole.
    if (room.live) return
    this.saveVersion(room)
    this.saveState(room)
    if (room.stateTimer) clearTimeout(room.stateTimer)
    if (room.presenceTimer) clearTimeout(room.presenceTimer)
    room.awareness.destroy()
    room.doc.destroy()
    this.rooms.delete(room.artifactId)
  }

  private changed(room: Room, update: Uint8Array, origin: Origin) {
    const frame = (() => {
      const encoder = encoding.createEncoder()
      encoding.writeVarUint(encoder, MESSAGE_SYNC)
      syncProtocol.writeUpdate(encoder, update)
      return encoding.toUint8Array(encoder)
    })()
    const from = typeof origin === 'object' && 'user' in origin ? origin.user : null
    for (const socket of room.sockets.keys()) if (socket !== from) socket.send(frame)
    if (origin === 'load') return
    // Writing taken back is the agent's too.
    const live =
      room.live && (origin === room.live.origin || (origin as unknown) === room.live.undo)
    const author = live
      ? room.live!.by
      : from
        ? 'user'
        : typeof origin === 'object' && 'agent' in origin
          ? origin.agent
          : 'user'
    // Another author's changes start a version of their own.
    if (room.pending && room.pending !== author) this.saveVersion(room)
    room.pending = author
    this.later(room, 'stateTimer', this.stateDelayMs, () => this.saveState(room))
    // What an agent is still writing is saved when it is whole.
    if (!live) this.later(room, 'versionTimer', this.versionDelayMs, () => this.saveVersion(room))
    if (author === 'user') {
      for (const listener of this.userListeners) {
        listener({ artifactId: room.artifactId, projectId: room.projectId })
      }
    }
  }

  private later(room: Room, key: 'stateTimer' | 'versionTimer', ms: number, run: () => void) {
    if (room[key]) clearTimeout(room[key])
    room[key] = setTimeout(() => {
      room[key] = null
      run()
    }, ms)
  }

  private saveState(room: Room) {
    if (room.stateTimer) clearTimeout(room.stateTimer)
    room.stateTimer = null
    try {
      this.app.writeSharedState(room.artifactId, {
        version: room.version,
        state: Y.encodeStateAsUpdate(room.doc),
      })
    } catch (error) {
      console.error('could not keep the shared state of', room.artifactId, error)
    }
  }

  /** What changed since the last version, saved as one; the artifact as it is after. */
  private saveVersion(
    room: Room,
    options: { reason?: string; title?: string; force?: boolean } = {},
  ): ArtifactView {
    if (room.versionTimer) clearTimeout(room.versionTimer)
    room.versionTimer = null
    const author = room.pending
    room.pending = null
    if (!author && !options.force) return this.app.getArtifact(room.artifactId)
    try {
      const saved = this.app.reviseArtifact(room.artifactId, {
        content: read(room),
        author: author ?? 'user',
        reason: options.reason ?? '',
        shared: true,
        ...(options.title ? { title: options.title } : {}),
      })
      if (saved.version !== room.version) {
        room.version = saved.version
        this.saveState(room)
      }
      return saved
    } catch (error) {
      // Content the checks refuse stays in the room for its editors to fix.
      console.error('could not save a version of', room.artifactId, error)
      return this.app.getArtifact(room.artifactId)
    }
  }
}

function awarenessFrame(update: Uint8Array): Uint8Array {
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, MESSAGE_AWARENESS)
  encoding.writeVarUint8Array(encoder, update)
  return encoding.toUint8Array(encoder)
}

const read = (room: Room) => bridges[room.kind].read(room.doc)
