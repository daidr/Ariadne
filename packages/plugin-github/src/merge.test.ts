import { describe, expect, test } from 'vitest'
import { GitHubApi } from './api'

/** An API whose requests get these answers, by method and path. */
function apiWith(answers: Record<string, () => Response>) {
  const calls: { key: string; body: unknown }[] = []
  const api = new GitHubApi({
    base: 'https://api.example.com',
    owner: 'acme',
    name: 'app',
    token: async () => 't',
    fetch: (async (url: string, init: RequestInit) => {
      const key = `${init.method} ${new URL(url).pathname}`
      calls.push({ key, body: init.body ? JSON.parse(String(init.body)) : null })
      return answers[key]?.() ?? Response.json({ message: 'Not Found' }, { status: 404 })
    }) as unknown as typeof fetch,
  })
  return { api, calls }
}

const MERGE = 'PUT /repos/acme/app/pulls/7/merge'
const PULL = 'GET /repos/acme/app/pulls/7'

const notMergeable = () =>
  Response.json({ message: 'Pull Request is not mergeable' }, { status: 405 })

describe('merging a pull request', () => {
  test('merges with the method, only at the given head', async () => {
    const { api, calls } = apiWith({ [MERGE]: () => Response.json({ sha: 'm7', merged: true }) })
    expect(await api.mergePullRequest(7, { method: 'squash', sha: 'h1' })).toEqual({
      kind: 'merged',
      sha: 'm7',
    })
    expect(calls).toEqual([{ key: MERGE, body: { merge_method: 'squash', sha: 'h1' } }])
  })

  test('a moved head, a conflict and a refusal come back as outcomes', async () => {
    const moved = apiWith({
      [MERGE]: () => Response.json({ message: 'Head branch was modified' }, { status: 409 }),
    })
    expect(await moved.api.mergePullRequest(7, { method: 'merge', sha: 'h1' })).toEqual({
      kind: 'moved',
    })
    const conflict = apiWith({
      [MERGE]: notMergeable,
      [PULL]: () => Response.json({ mergeable: false, mergeable_state: 'dirty' }),
    })
    expect(await conflict.api.mergePullRequest(7, { method: 'merge', sha: 'h1' })).toEqual({
      kind: 'conflict',
    })
    const blocked = apiWith({
      [MERGE]: notMergeable,
      [PULL]: () => Response.json({ mergeable: true, mergeable_state: 'blocked' }),
    })
    expect(await blocked.api.mergePullRequest(7, { method: 'merge', sha: 'h1' })).toEqual({
      kind: 'refused',
      detail: 'Pull Request is not mergeable',
    })
  })

  test('other failures throw', async () => {
    const { api } = apiWith({ [MERGE]: () => Response.json({ message: 'Bad' }, { status: 401 }) })
    await expect(api.mergePullRequest(7, { method: 'merge', sha: 'h1' })).rejects.toThrow(/401/)
  })
})
