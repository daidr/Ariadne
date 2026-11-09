import type { Ariadne, ArtifactKind, ArtifactView } from '@ariadne/core'
import * as decoding from 'lib0/decoding'
import * as encoding from 'lib0/encoding'
import * as awarenessProtocol from 'y-protocols/awareness'
import * as syncProtocol from 'y-protocols/sync'
import * as Y from 'yjs'
import { bridges, sharedKinds, type SharedKind } from './shared-content'

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
    input: { reason: string; title?: string; base?: string },
  ): ArtifactView {
    if (by !== 'user') for (const listener of this.writeListeners) listener(artifactId)
    const room = this.room(artifactId)
    this.saveVersion(room)
    // `null`: only the title changes.
    if (next !== null) {
      room.doc.transact(() => this.bridge(room).update(room.doc, next, input.base), { agent: by })
    }
    return this.saveVersion(room, {
      reason: input.reason,
      title: input.title,
      force: !!input.title,
    })
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
    if (room.sockets.size) return
    // The last page left: what waits is saved, and the room closes.
    this.saveVersion(room)
    this.saveState(room)
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
    const author = from
      ? 'user'
      : typeof origin === 'object' && 'agent' in origin
        ? origin.agent
        : 'user'
    // Another author's changes start a version of their own.
    if (room.pending && room.pending !== author) this.saveVersion(room)
    room.pending = author
    this.later(room, 'stateTimer', this.stateDelayMs, () => this.saveState(room))
    this.later(room, 'versionTimer', this.versionDelayMs, () => this.saveVersion(room))
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
