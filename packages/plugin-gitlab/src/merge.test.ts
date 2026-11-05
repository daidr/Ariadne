import { describe, expect, test } from 'vitest'
import { GitLabApi } from './api'

function apiWith(answers: Record<string, () => Response>) {
  const calls: { key: string; body: unknown }[] = []
  const api = new GitLabApi({
    base: 'https://gitlab.example.com/api/v4',
    path: 'group/app',
    webUrl: 'https://gitlab.example.com/group/app',
    token: async () => 't',
    fetch: (async (url: string, init: RequestInit) => {
      const key = `${init.method} ${new URL(url).pathname.replace('/api/v4', '')}`
      calls.push({ key, body: init.body ? JSON.parse(String(init.body)) : null })
      return answers[key]?.() ?? Response.json({ message: '404 Not Found' }, { status: 404 })
    }) as unknown as typeof fetch,
  })
  return { api, calls }
}

const MERGE = 'PUT /projects/group%2Fapp/merge_requests/3/merge'
const MR = 'GET /projects/group%2Fapp/merge_requests/3'

const cannot = () => Response.json({ message: 'Branch cannot be merged' }, { status: 406 })

describe('merging a merge request', () => {
  test('squash follows the method; the commit comes back', async () => {
    const { api, calls } = apiWith({
      [MERGE]: () => Response.json({ state: 'merged', merge_commit_sha: 'm3' }),
    })
    expect(await api.mergeMergeRequest(3, { method: 'squash', sha: 'h1' })).toEqual({
      kind: 'merged',
      sha: 'm3',
    })
    expect(calls).toEqual([{ key: MERGE, body: { sha: 'h1', squash: true } }])
  })

  test('a moved branch, a conflict and a refusal come back as outcomes', async () => {
    const moved = apiWith({
      [MERGE]: () => Response.json({ message: 'SHA does not match HEAD' }, { status: 409 }),
    })
    expect(await moved.api.mergeMergeRequest(3, { method: 'merge', sha: 'h1' })).toEqual({
      kind: 'moved',
    })
    const conflict = apiWith({
      [MERGE]: cannot,
      [MR]: () => Response.json({ has_conflicts: true }),
    })
    expect(await conflict.api.mergeMergeRequest(3, { method: 'merge', sha: 'h1' })).toEqual({
      kind: 'conflict',
    })
    const draft = apiWith({
      [MERGE]: () => Response.json({ message: '405 Method Not Allowed' }, { status: 405 }),
      [MR]: () => Response.json({ has_conflicts: false }),
    })
    expect(await draft.api.mergeMergeRequest(3, { method: 'merge', sha: 'h1' })).toEqual({
      kind: 'refused',
      detail: '405 Method Not Allowed',
    })
  })
})
