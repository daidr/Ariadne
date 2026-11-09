import { MergeRefused, type WorkspaceCapability } from '@ariadne/plugin-sdk'
import { existsSync } from 'node:fs'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { mergeBranch, parseDiff, parseStatus, type GitRun } from './branches'
import { branchFor, localDirectoryProvider, run, serverGitEnv } from './index'

let base: string

beforeEach(async () => {
  base = await mkdtemp(join(tmpdir(), 'ariadne-branches-'))
})

afterEach(async () => {
  await rm(base, { recursive: true, force: true })
})

async function repo(): Promise<string> {
  const dir = join(base, 'repo')
  await run(['mkdir', '-p', dir], { cwd: base })
  const git = (...args: string[]) => run(['git', ...args], { cwd: dir })
  await git('init', '-q', '-b', 'main')
  await git('config', 'user.email', 'test@example.com')
  await git('config', 'user.name', 'test')
  await writeFile(join(dir, 'README.md'), '# repo\n')
  await writeFile(join(dir, 'old.txt'), 'one\ntwo\nthree\nfour\nfive\n')
  await git('add', '.')
  await git('commit', '-q', '-m', 'init')
  return dir
}

const identity = async () => ({ name: 'test', email: 'test@example.com' })

const git = async (cwd: string, ...args: string[]) =>
  (await run(['git', ...args], { cwd })).stdout.trim()

async function setup() {
  const dir = await repo()
  const { workspace } = await localDirectoryProvider(join(base, 'data'), undefined, null).connect({
    path: dir,
  })
  const capability = workspace as WorkspaceCapability
  const thread = await capability.open({ threadId: 'thr_a' })
  return { dir, capability, branches: capability.branches!, thread }
}

describe('parsing', () => {
  test('diff with renames and binary files', () => {
    const nameStatus = ['M', 'a.ts', 'R090', 'old.ts', 'new.ts', 'A', 'img.png', ''].join('\0')
    const numstat = ['3\t1\ta.ts', '2\t2\t', 'old.ts', 'new.ts', '-\t-\timg.png', ''].join('\0')
    expect(parseDiff(nameStatus, numstat)).toEqual([
      { path: 'a.ts', previousPath: null, status: 'modified', added: 3, removed: 1 },
      { path: 'new.ts', previousPath: 'old.ts', status: 'renamed', added: 2, removed: 2 },
      { path: 'img.png', previousPath: null, status: 'added', added: null, removed: null },
    ])
  })

  test('status gives the new path of a rename', () => {
    expect(parseStatus([' M a.ts', 'R  new.ts', 'old.ts', '?? b.ts', ''].join('\0'))).toEqual([
      'a.ts',
      'new.ts',
      'b.ts',
    ])
  })
})

describe('thread branches', () => {
  test('the base is the branch the checkout was on, and kept once recorded', async () => {
    const { dir, capability, thread } = await setup()
    expect(thread.base).toBe('main')
    await git(dir, 'switch', '-q', '-c', 'other')
    expect((await capability.open({ threadId: 'thr_b' })).base).toBe('other')
    expect(
      (await capability.open({ threadId: 'thr_a', branch: branchFor('thr_a'), base: 'main' })).base,
    ).toBe('main')
  })

  test('changes compare the committed branch with where it left the base', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec(
      'printf "one\\n2\\nthree\\nfour\\nfive\\n" > old.txt && git mv old.txt moved.txt && echo hi > new.ts && git add . && git commit -qm work && echo wip > wip.txt',
    )
    // The base moving on does not show up as the thread's change.
    await writeFile(join(dir, 'base.txt'), 'base\n')
    await git(dir, 'add', '.')
    await git(dir, 'commit', '-qm', 'base moves')

    const changes = (await branches.changes(
      { threadId: 'thr_a', branch: branchFor('thr_a') },
      'main',
    ))!
    expect(changes.branch).toBe('ariadne/thr_a')
    expect(changes.commits.map((c) => c.subject)).toEqual(['work'])
    expect(changes.files).toEqual([
      { path: 'moved.txt', previousPath: 'old.txt', status: 'renamed', added: 1, removed: 1 },
      { path: 'new.ts', previousPath: null, status: 'added', added: 1, removed: 0 },
    ])
    expect(changes.uncommitted).toEqual(['wip.txt'])

    const renamed = await branches.file({
      from: changes.from,
      head: changes.head,
      path: 'moved.txt',
      previousPath: 'old.txt',
    })
    expect(renamed).toEqual({
      before: 'one\ntwo\nthree\nfour\nfive\n',
      after: 'one\n2\nthree\nfour\nfive\n',
      skipped: null,
    })
    const added = await branches.file({
      from: changes.from,
      head: changes.head,
      path: 'new.ts',
      previousPath: null,
    })
    expect(added.before).toBeNull()
  })

  test('binary and large files are left out', async () => {
    const { branches, thread } = await setup()
    await thread.exec(
      'printf "a\\0b" > bin.dat && head -c 600000 /dev/zero | tr "\\0" "x" > big.txt && git add . && git commit -qm files',
    )
    const changes = (await branches.changes(
      { threadId: 'thr_a', branch: branchFor('thr_a') },
      'main',
    ))!
    const file = (path: string) =>
      branches.file({ from: changes.from, head: changes.head, path, previousPath: null })
    expect((await file('bin.dat')).skipped).toBe('binary')
    expect((await file('big.txt')).skipped).toBe('large')
  })

  test('a branch with no commits is null before it exists', async () => {
    const { branches } = await setup()
    expect(
      await branches.changes({ threadId: 'thr_none', branch: branchFor('thr_none') }, 'main'),
    ).toBeNull()
    expect(
      (await branches.changes({ threadId: 'thr_a', branch: branchFor('thr_a') }, 'main'))?.files,
    ).toEqual([])
  })

  test('squash merge into the checked-out base updates its files', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec(
      'echo a > a.txt && git add . && git commit -qm a && echo b > b.txt && git add . && git commit -qm b',
    )
    const head = await git(thread.label, 'rev-parse', 'HEAD')
    const before = await git(dir, 'rev-parse', 'main')

    const outcome = await branches.merge!({
      threadId: 'thr_a',
      branch: branchFor('thr_a'),
      base: 'main',
      method: 'squash',
      message: 'Add a and b',
    })
    expect(outcome).toMatchObject({ kind: 'merged', head })
    const sha = (outcome as { sha: string }).sha
    expect(await git(dir, 'rev-parse', 'main')).toBe(sha)
    expect(await git(dir, 'log', '-1', '--format=%P %s %an')).toBe(`${before} Add a and b test`)
    // The user's checkout follows, with nothing left over.
    expect(await readFile(join(dir, 'b.txt'), 'utf8')).toBe('b\n')
    expect(await git(dir, 'status', '--porcelain')).toBe('')
    // The thread's worktree is untouched.
    expect(await git(thread.label, 'rev-parse', 'HEAD')).toBe(head)

    // Merging the same work again changes nothing.
    expect(
      await branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'x',
      }),
    ).toEqual({ kind: 'empty' })
  })

  test('a merge commit keeps the branch as a parent', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec('echo a > a.txt && git add . && git commit -qm a')
    const head = await git(thread.label, 'rev-parse', 'HEAD')
    await branches.merge!({
      threadId: 'thr_a',
      branch: branchFor('thr_a'),
      base: 'main',
      method: 'merge',
      message: 'Merge',
    })
    expect((await git(dir, 'log', '-1', '--format=%P')).split(' ')[1]).toBe(head)
    expect(await branches.merged({ threadId: 'thr_a', branch: branchFor('thr_a') }, 'main')).toBe(
      true,
    )
  })

  test('a base that is not checked out moves without a working tree', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec('echo a > a.txt && git add . && git commit -qm a')
    await git(dir, 'switch', '-q', '-c', 'elsewhere')
    const outcome = await branches.merge!({
      threadId: 'thr_a',
      branch: branchFor('thr_a'),
      base: 'main',
      method: 'squash',
      message: 'a',
    })
    expect(await git(dir, 'rev-parse', 'main')).toBe((outcome as { sha: string }).sha)
    expect(existsSync(join(dir, 'a.txt'))).toBe(false)
  })

  test('conflicts merge nothing and name the files', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec('echo thread > README.md && git commit -qam readme')
    await writeFile(join(dir, 'README.md'), 'user\n')
    await git(dir, 'commit', '-qam', 'user readme')
    const before = await git(dir, 'rev-parse', 'main')
    expect(
      await branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'x',
      }),
    ).toEqual({ kind: 'conflict', files: ['README.md'] })
    expect(await git(dir, 'rev-parse', 'main')).toBe(before)
  })

  test('conflicting file names come as they are, not quoted', async () => {
    const { dir, branches, thread } = await setup()
    await writeFile(join(dir, '中文.md'), 'base\n')
    await git(dir, 'add', '.')
    await git(dir, 'commit', '-qm', 'zh')
    await thread.exec('git merge -q main && echo thread > 中文.md && git commit -qam zh')
    await writeFile(join(dir, '中文.md'), 'user\n')
    await git(dir, 'commit', '-qam', 'user zh')
    expect(
      await branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'x',
      }),
    ).toEqual({ kind: 'conflict', files: ['中文.md'] })
  })

  test('a merge is taken back: the base and its checkout return, changes there are kept', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec('echo a > a.txt && git add . && git commit -qm a')
    const before = await git(dir, 'rev-parse', 'main')
    const squash = () =>
      branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'a',
      }) as Promise<{ sha: string }>
    const { sha } = await squash()
    // What the user was doing in the checkout stays as it is.
    await writeFile(join(dir, 'README.md'), '# mine\n')
    await branches.undoMerge!({ branch: branchFor('thr_a'), base: 'main', sha })
    expect(await git(dir, 'rev-parse', 'main')).toBe(before)
    expect(existsSync(join(dir, 'a.txt'))).toBe(false)
    expect(await readFile(join(dir, 'README.md'), 'utf8')).toBe('# mine\n')
    await git(dir, 'checkout', '-q', 'README.md')
    // The branch still has the work, and merges again.
    expect(
      (await branches.changes({ threadId: 'thr_a', branch: branchFor('thr_a') }, 'main'))?.files,
    ).toMatchObject([{ path: 'a.txt' }])
    const again = await squash()

    // Not checked out anywhere, only the branch moves.
    await git(dir, 'switch', '-q', '--detach')
    await branches.undoMerge!({ branch: branchFor('thr_a'), base: 'main', sha: again.sha })
    expect(await git(dir, 'rev-parse', 'main')).toBe(before)
  })

  test('a merge is not taken back once commits came after it, it was pushed, or files are in the way', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec('echo a > a.txt && git add . && git commit -qm a')
    const squash = async () =>
      (
        (await branches.merge!({
          threadId: 'thr_a',
          branch: branchFor('thr_a'),
          base: 'main',
          method: 'squash',
          message: 'a',
        })) as { sha: string }
      ).sha
    let sha = await squash()
    const undo = () => branches.undoMerge!({ branch: branchFor('thr_a'), base: 'main', sha })

    // The user changed the same file since: taking the merge back would lose that.
    await writeFile(join(dir, 'a.txt'), 'mine\n')
    await expect(undo()).rejects.toMatchObject({ reason: 'checkout' })
    await git(dir, 'checkout', '-q', 'a.txt')

    // A remote branch has it.
    await git(dir, 'update-ref', 'refs/remotes/origin/main', sha)
    await expect(undo()).rejects.toMatchObject({ reason: 'undo-pushed', detail: 'origin/main' })
    await git(dir, 'update-ref', '-d', 'refs/remotes/origin/main')

    // A thread opened after the merge has it on its branch, and would bring it back.
    await git(dir, 'branch', 'ariadne/later', 'main')
    await expect(undo()).rejects.toMatchObject({ reason: 'undo-branched', detail: 'ariadne/later' })
    await git(dir, 'branch', '-D', 'ariadne/later')
    // The merged branch itself may have it, from bringing the base in.
    await thread.exec('git merge -q main && echo b > b.txt && git add . && git commit -qm b')
    await undo()
    expect(existsSync(join(dir, 'a.txt'))).toBe(false)

    sha = await squash()
    await writeFile(join(dir, 'user.txt'), 'user\n')
    await git(dir, 'add', '.')
    await git(dir, 'commit', '-qm', 'user')
    await expect(undo()).rejects.toMatchObject({ reason: 'undo-moved' })
    expect(await git(dir, 'log', '-1', '--format=%s')).toBe('user')
    expect(await readFile(join(dir, 'a.txt'), 'utf8')).toBe('a\n')
  })

  test('a commit made in the checkout meanwhile is reported as the base moving', async () => {
    const { dir, thread } = await setup()
    await thread.exec('echo a > a.txt && git add . && git commit -qm a')
    const plain: GitRun = (args, options = {}) =>
      run(['git', ...args], { cwd: options.cwd ?? dir, env: serverGitEnv(['file']) })
    // The user commits just after the merge commit is written.
    const racing: GitRun = async (args, options) => {
      const result = await plain(args, options)
      if (args.includes('commit-tree')) {
        await writeFile(join(dir, 'user.txt'), 'user\n')
        await git(dir, 'add', '.')
        await git(dir, 'commit', '-qm', 'user')
      }
      return result
    }
    const merge = mergeBranch(racing, identity, {
      branch: 'ariadne/thr_a',
      base: 'main',
      method: 'squash',
      message: 'a',
    })
    await expect(merge).rejects.toMatchObject({ reason: 'base-moved' })
    expect(await git(dir, 'log', '-1', '--format=%s')).toBe('user')
  })

  test('a checked-out base is not updated when the repository uses filters', async () => {
    const { dir, branches, thread } = await setup()
    await writeFile(join(dir, '.gitattributes'), '*.bin filter=lfs diff=lfs merge=lfs -text\n')
    await git(dir, 'add', '.')
    await git(dir, 'commit', '-qm', 'attributes')
    await thread.exec('git merge -q main && echo a > a.txt && git add . && git commit -qm a')
    const before = await git(dir, 'rev-parse', 'main')
    await expect(
      branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'a',
      }),
    ).rejects.toMatchObject({ reason: 'filters' })
    expect(await git(dir, 'rev-parse', 'main')).toBe(before)
    // Not checked out, nothing is written to files: it merges.
    await git(dir, 'switch', '-q', '-c', 'elsewhere')
    expect(
      await branches.merge!({
        threadId: 'thr_a',
        branch: branchFor('thr_a'),
        base: 'main',
        method: 'squash',
        message: 'a',
      }),
    ).toMatchObject({ kind: 'merged' })
  })

  test('changes in the checkout that stand in the way stop the merge', async () => {
    const { dir, branches, thread } = await setup()
    await thread.exec('echo thread > README.md && git commit -qam readme')
    await writeFile(join(dir, 'README.md'), 'not committed\n')
    const before = await git(dir, 'rev-parse', 'main')
    const merge = branches.merge!({
      threadId: 'thr_a',
      branch: branchFor('thr_a'),
      base: 'main',
      method: 'squash',
      message: 'x',
    })
    await expect(merge).rejects.toBeInstanceOf(MergeRefused)
    await expect(merge).rejects.toMatchObject({ reason: 'checkout' })
    expect(await git(dir, 'rev-parse', 'main')).toBe(before)
    expect(await readFile(join(dir, 'README.md'), 'utf8')).toBe('not committed\n')
  })

  test('worktrees are listed and removed, keeping the branch; one not committed stays', async () => {
    const { dir, branches, capability, thread } = await setup()
    await capability.open({ threadId: 'thr_b' })
    const listed = await branches.worktrees()
    expect(listed.map((w) => w.threadId).toSorted()).toEqual(['thr_a', 'thr_b'])
    expect(listed[0]!.bytes).toBeGreaterThan(0)

    await thread.exec('echo wip > wip.txt')
    expect(await branches.remove('thr_a')).toBe(false)
    expect(existsSync(thread.label)).toBe(true)

    expect(await branches.remove('thr_b')).toBe(true)
    expect((await branches.worktrees()).map((w) => w.threadId)).toEqual(['thr_a'])
    expect(await git(dir, 'branch', '--list', 'ariadne/thr_b')).toContain('ariadne/thr_b')
    // Gone already: nothing to do.
    expect(await branches.remove('thr_b')).toBe(true)
    // Opening it again checks the branch out.
    const again = await capability.open({
      threadId: 'thr_b',
      branch: branchFor('thr_b'),
      base: 'main',
    })
    expect(again.branch).toBe('ariadne/thr_b')
  })
})

describe('turn snapshots', () => {
  test('write the worktree as a tree without touching its index, and compare two', async () => {
    const { branches, thread } = await setup()
    const root = (await thread.exec('pwd')).stdout.trim()
    const before = await branches.snapshot!('thr_a')
    expect(before).toMatch(/^[0-9a-f]{40,64}$/)
    // A command changes one file, adds one, removes one and leaves an ignored one.
    await thread.writeFile('README.md', '# repo\nmore\n')
    await thread.writeFile('new.txt', 'fresh\n')
    await thread.writeFile('.gitignore', 'ignored.log\n')
    await thread.writeFile('ignored.log', 'noise\n')
    await thread.exec('rm old.txt')
    const after = await branches.snapshot!('thr_a')
    expect(after).not.toBe(before)
    const files = await branches.diff!(before!, after!)
    expect(files.map((f) => [f.path, f.status, f.added, f.removed]).toSorted()).toEqual(
      [
        ['.gitignore', 'added', 1, 0],
        ['README.md', 'modified', 1, 0],
        ['new.txt', 'added', 1, 0],
        ['old.txt', 'deleted', 0, 5],
      ].toSorted(),
    )
    // The worktree's own index still has nothing staged.
    expect(await git(root, 'diff', '--cached', '--name-only')).toBe('')
    // The same files again give the same tree, and the versions can be read from it.
    expect(await branches.snapshot!('thr_a')).toBe(after)
    const versions = await branches.file({
      from: before!,
      head: after!,
      path: 'README.md',
      previousPath: null,
    })
    expect(versions).toEqual({ before: '# repo\n', after: '# repo\nmore\n', skipped: null })
  })

  test('give nothing for a thread without a worktree', async () => {
    const { branches } = await setup()
    expect(await branches.snapshot!('thr_none')).toBeNull()
  })

  test('are kept from git gc until the thread is discarded', async () => {
    const { dir, capability, branches, thread } = await setup()
    await thread.writeFile('loose.txt', 'only in a snapshot\n')
    const tree = (await branches.snapshot!('thr_a'))!
    expect(
      await git(dir, 'for-each-ref', '--format=%(objectname)', 'refs/ariadne/turns/thr_a/'),
    ).toBe(tree)
    // Removing the worktree keeps them: the thread's turns still show their changes.
    await thread.exec('rm loose.txt')
    expect(await branches.remove('thr_a')).toBe(true)
    expect(await git(dir, 'for-each-ref', 'refs/ariadne/turns/thr_a/')).not.toBe('')
    await git(dir, 'gc', '--prune=now', '--quiet')
    expect(await git(dir, 'cat-file', '-t', tree)).toBe('tree')
    // Discarding them lets them go.
    await capability.open({ threadId: 'thr_a' })
    expect(await branches.remove('thr_a', { discard: true })).toBe(true)
    expect(await git(dir, 'for-each-ref', 'refs/ariadne/turns/thr_a/')).toBe('')
  })
})
