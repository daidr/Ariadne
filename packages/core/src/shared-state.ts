import type { Database } from 'bun:sqlite'

/**
 * The shared editing state of a document or whiteboard that people and agents edit
 * together: a Yjs update holding the whole document, with the version it was last saved
 * as. Kept beside the event log like files: what it holds reaches the log as versions,
 * and a state whose version is not the artifact's newest is built again from that version.
 */
export function setupSharedState(db: Database): void {
  db.run(`CREATE TABLE IF NOT EXISTS artifact_shared (
    artifact_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    state BLOB NOT NULL,
    updated_at INTEGER NOT NULL
  )`)
}

export interface SharedState {
  /** The artifact's version the state was saved as, or built from. */
  version: number
  state: Uint8Array
}

export function readSharedState(db: Database, artifactId: string): SharedState | null {
  const row = db
    .query<{ version: number; state: Uint8Array }, [string]>(
      'SELECT version, state FROM artifact_shared WHERE artifact_id = ?',
    )
    .get(artifactId)
  return row ? { version: row.version, state: new Uint8Array(row.state) } : null
}

export function writeSharedState(
  db: Database,
  projectId: string,
  artifactId: string,
  shared: SharedState,
  at: number,
): void {
  db.run(
    `INSERT INTO artifact_shared (artifact_id, project_id, version, state, updated_at) VALUES (?, ?, ?, ?, ?)
     ON CONFLICT (artifact_id) DO UPDATE SET version = excluded.version, state = excluded.state, updated_at = excluded.updated_at`,
    [artifactId, projectId, shared.version, shared.state, at],
  )
}
