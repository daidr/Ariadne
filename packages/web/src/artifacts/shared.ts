import { onScopeDispose, ref, shallowRef } from 'vue'
import { WebsocketProvider } from 'y-websocket'
import * as Y from 'yjs'
import { api } from '../api'
import { t } from '../i18n'

/**
 * Join the room of a document or whiteboard edited together: the Yjs document the page
 * edits, shared with the server (and through it with agents and other tabs), and who else
 * is there. `ready` turns true once the page holds what the server has. Leaves when the
 * component that asked goes.
 */
export function useSharedArtifact(artifactId: string, onLockedEdit?: () => void) {
  const doc = new Y.Doc()
  const { server, room, params } = api.sharedRoom(artifactId)
  const provider = new WebsocketProvider(server, room, doc, {
    params,
    // Document updates go through the room so it can enforce the locks.
    disableBc: !!onLockedEdit,
  })
  provider.awareness.setLocalStateField('user', { name: t('artifact.by.user') })
  const ready = ref(provider.synced)
  const connected = ref(provider.wsconnected)
  provider.on('sync', (synced: boolean) => (ready.value = synced))
  provider.on('status', ({ status }: { status: string }) => {
    connected.value = status === 'connected'
  })
  provider.on('closed', ({ code }: { code: number }) => {
    if (code === 4409) queueMicrotask(() => onLockedEdit?.())
  })
  onScopeDispose(() => {
    provider.destroy()
    doc.destroy()
  })
  return { doc, awareness: provider.awareness, ready, connected, provider: shallowRef(provider) }
}

/** What the page edits through: the document and who else is in it. */
export type SharedArtifact = ReturnType<typeof useSharedArtifact>
