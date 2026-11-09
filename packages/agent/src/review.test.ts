import { Ariadne } from '@ariadne/core'
import type {
  BranchCapability,
  ContextProvider,
  ModelRequest,
  SourceView,
  Workspace,
} from '@ariadne/plugin-sdk'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, test } from 'vitest'
import { PluginHost } from './plugins'
import { reviewerPrompt, skillsSection } from './prompts'
import { numberedDiff, readSkill } from './review-tools'
import { Scheduler } from './scheduler'
import { loadSkills, skillName, whenToUse } from './skills'
import { call, fakeModel, say } from './test-model'
import type { ToolContext } from './tool-helpers'
import { mentionsText } from './transcript'
import { WorkspaceManager } from './workspaces'

let app: Ariadne
let scheduler: Scheduler | null = null
let home: string

beforeEach(async () => {
  app = new Ariadne({ path: ':memory:' })
  home = await mkdtemp(join(tmpdir(), 'ariadne-skills-'))
})

afterEach(async () => {
  await scheduler?.stop()
  scheduler = null
  app.close()
  await rm(home, { recursive: true, force: true })
})

/** Files in memory, listed by directory as a checkout lists them. */
function memoryFiles(files: Record<string, string>) {
  const list = async (path: string) => {
    const dir = path === '.' ? '' : `${path.replace(/^\.\//, '').replace(/\/$/, '')}/`
    const names = new Map<string, 'file' | 'directory'>()
    for (const key of Object.keys(files)) {
      if (!key.startsWith(dir)) continue
      const [first, ...rest] = key.slice(dir.length).split('/')
      names.set(first!, rest.length ? 'directory' : 'file')
    }
    if (!names.size) throw new Error(`ENOENT: ${path}`)
    return [...names].map(([name, kind]) => ({ name, kind }))
  }
  const readBytes = async (path: string) => {
    const content = files[path.replace(/^\.\//, '')]
    if (content === undefined) throw new Error(`ENOENT: ${path}`)
    return new TextEncoder().encode(content)
  }
  return { list, readBytes }
}

function workspaceOf(files: Record<string, string>): Workspace {
  const { list, readBytes } = memoryFiles(files)
  return {
    id: 'w',
    label: '/work/thread',
    branch: 'ariadne/thread',
    base: 'main',
    readFile: async (path) => new TextDecoder().decode(await readBytes(path)),
    readBytes,
    async writeFile(path, content) {
      files[path] = content
    },
    list,
    async exec() {
      return { exitCode: 0, stdout: '', stderr: '' }
    },
    async dispose() {},
  }
}

const BEFORE = 'export function first(list) {\n  return list[0]\n}\n'
const AFTER = 'export function first(list) {\n  return list[0].id\n}\n'

async function host(files: Record<string, string>) {
  const branches = {
    changes: async () => ({
      branch: 'ariadne/thread',
      base: 'main',
      from: 'a1',
      head: 'b2',
      commits: [{ sha: 'b2', subject: '修导出' }],
      files: [{ path: 'src/a.ts', previousPath: null, status: 'modified', added: 1, removed: 1 }],
      uncommitted: [],
    }),
    file: async () => ({ before: BEFORE, after: AFTER, skipped: null }),
  } as unknown as BranchCapability
  const view = { ...memoryFiles(files), ref: null } as unknown as SourceView
  const provider: ContextProvider<never> = {
    kind: 'memory',
    title: { 'zh-CN': '内存' },
    settings: { type: 'object', properties: {} },
    provides: ['workspace'],
    async connect() {
      return {
        workspace: {
          isolated: true,
          open: async () => workspaceOf(files),
          view: async () => view,
          branches,
        },
      }
    },
  }
  const plugins = new PluginHost('/tmp/ariadne-test-plugins')
  await plugins.load({
    id: 'memory',
    name: { 'zh-CN': '内存' },
    setup: (ctx) => ctx.registerContextProvider(provider),
  })
  return plugins
}

const system = (request: ModelRequest) => request.messages[0]?.content ?? ''
const isReviewer = (request: ModelRequest) => system(request).includes('You are a reviewing thread')
const lastTool = (request: ModelRequest) => {
  const last = request.messages.at(-1)
  return last?.role === 'tool' ? last.content : null
}

describe('skills', () => {
  test('a skill says when to use it in its first line', () => {
    expect(whenToUse('# 审代码的时候\n\n正文')).toBe('审代码的时候')
    expect(whenToUse('\n\n> 写发布说明时\n')).toBe('写发布说明时')
    expect(whenToUse('---\nname: x\ndescription: "Use when releasing"\n---\n# Release')).toBe(
      'Use when releasing',
    )
    expect(whenToUse('---\nname: x\n---\n\nAfter the front matter')).toBe('After the front matter')
    expect(whenToUse('')).toBe('')
    expect(skillName('review.md')).toBe('review')
    expect(skillName('发布.md')).toBe('发布')
    expect(skillName('notes.txt')).toBeNull()
    expect(skillName('.hidden.md')).toBeNull()
    expect(skillName('a b.md')).toBeNull()
  })

  test("the repository's skills and the user's are listed, the repository's winning", async () => {
    await writeFile(join(home, 'review.md'), '本机的审法\n')
    await writeFile(join(home, 'release.md'), '发版的时候\n步骤…')
    await writeFile(join(home, 'skip.txt'), 'not a skill')
    const repo = memoryFiles({
      '.ariadne/skills/review.md': '# 审 PR 的时候\n只看 bug',
      '.ariadne/skills/bad name.md': 'x',
      'README.md': 'hi',
    })
    const skills = await loadSkills({ repo, localDirs: [home] })
    expect(skills.list()).toEqual([
      { name: 'release', when: '发版的时候', source: 'local' },
      { name: 'review', when: '审 PR 的时候', source: 'repo' },
    ])
    expect(skills.get('review')?.text).toContain('只看 bug')
    // Nowhere to read from is no skills, not an error.
    expect((await loadSkills({ repo: null, localDirs: [join(home, 'none')] })).list()).toEqual([])
  })

  test('a skill may be a folder with SKILL.md and files beside it', async () => {
    await mkdir(join(home, 'release', 'scripts'), { recursive: true })
    await writeFile(
      join(home, 'release', 'SKILL.md'),
      '---\nname: release\ndescription: >\n  发版的时候，\n  按步骤来\nlicense: x\n---\n见 scripts/tag.sh',
    )
    await writeFile(join(home, 'release', 'scripts', 'tag.sh'), 'git tag')
    // A folder without SKILL.md is not a skill; a folder wins a file of its name.
    await mkdir(join(home, 'notes'))
    await writeFile(join(home, 'release.md'), '旧的单文件')
    const repo = memoryFiles({
      '.agents/skills/deploy/SKILL.md': '部署的时候\n看 references/hosts.md',
      '.agents/skills/deploy/references/hosts.md': 'a.example',
      '.agents/skills/deploy/references/deep/a/b/c.md': 'too deep',
    })
    const skills = await loadSkills({ repo, localDirs: [home] })
    expect(skills.list()).toEqual([
      { name: 'deploy', when: '部署的时候', source: 'repo' },
      { name: 'release', when: '发版的时候， 按步骤来', source: 'local' },
    ])
    expect(skills.get('release')?.folder).toBe(join(home, 'release'))
    expect(skills.get('deploy')?.folder).toBe('.agents/skills/deploy')
    expect(await skills.files('release')).toEqual({
      paths: ['scripts/tag.sh'],
      more: false,
    })
    expect(await skills.files('deploy')).toEqual({
      paths: ['references/hosts.md'],
      more: true,
    })
    expect(await skills.read('deploy', 'references/hosts.md')).toBe('a.example')
    expect(await skills.read('release', './scripts\\tag.sh')).toBe('git tag')
    // Nothing outside the folder, and nothing for a name that is not there.
    expect(await skills.read('release', '../release.md')).toBeNull()
    expect(await skills.read('release', join(home, 'release.md'))).toBeNull()
    expect(await skills.read('release', 'none.md')).toBeNull()

    const ctx = { skills: () => skills } as unknown as ToolContext
    const whole = await readSkill.run({ name: 'deploy' }, ctx)
    expect(whole.content).toContain('看 references/hosts.md')
    expect(whole.content).toContain('.agents/skills/deploy in the repository')
    expect(whole.content).toContain('(more are not listed): references/hosts.md')
    const file = await readSkill.run({ name: 'deploy', file: 'references/hosts.md' }, ctx)
    expect(file.content).toContain('a.example')
    await expect(readSkill.run({ name: 'deploy', file: '../x' }, ctx)).rejects.toThrow(
      'Its files: references/hosts.md',
    )
  })

  test('one name is one skill: places are read in order, the earlier winning', async () => {
    const shared = join(home, 'shared')
    const own = join(home, 'own')
    await mkdir(join(shared, 'review'), { recursive: true })
    await mkdir(own)
    await writeFile(join(shared, 'review', 'SKILL.md'), '共用目录的审法')
    await writeFile(join(shared, 'lint.md'), '共用目录的 lint')
    await writeFile(join(own, 'lint.md'), '自己的 lint')
    await writeFile(join(own, 'review.md'), '自己的审法')
    const repo = memoryFiles({
      '.agents/skills/review/SKILL.md': '仓库共用目录的审法',
      '.agents/skills/build.md': '仓库共用目录的构建',
      '.ariadne/skills/build.md': '仓库自己的构建',
    })
    const skills = await loadSkills({ repo, localDirs: [own, shared] })
    expect(skills.list()).toEqual([
      { name: 'build', when: '仓库自己的构建', source: 'repo' },
      { name: 'lint', when: '自己的 lint', source: 'local' },
      { name: 'review', when: '仓库共用目录的审法', source: 'repo' },
    ])
  })

  test('the prompt lists names and when to use them, not the text', () => {
    const section = skillsSection([{ name: 'review', when: '审 PR 的时候', source: 'repo' }])
    expect(section).toContain('- review (repository): 审 PR 的时候')
    expect(section).toContain('read_skill')
    expect(skillsSection([])).toBe('')
    expect(mentionsText([{ kind: 'skill', name: 'release' }])).toContain(
      'work by these skills: release',
    )
  })

  test('the review skill replaces the built-in guidance', () => {
    const base = {
      projectName: 'P',
      locale: 'zh-CN' as const,
      author: { title: '修导出', branch: 'ariadne/x', base: 'main' },
      node: { title: '导出', description: '', acceptance: '导出 CSV' },
      maxRounds: 2,
    }
    const own = reviewerPrompt({ ...base, reviewSkill: { source: 'repo', text: '只看并发问题' } })
    expect(own).toContain('只看并发问题')
    expect(own).not.toContain('Look for what will go wrong')
    expect(own).toContain('It is accepted when: 导出 CSV')
    expect(reviewerPrompt(base)).toContain('Look for what will go wrong')
  })
})

describe('numbered diff', () => {
  test('numbers lines as the new version does', () => {
    expect(numberedDiff(BEFORE, AFTER)).toBe(
      [
        '  1 │ export function first(list) {',
        '-   │   return list[0]',
        '+ 2 │   return list[0].id',
        '  3 │ }',
        '  4 │ ',
      ].join('\n'),
    )
    const long = Array.from({ length: 30 }, (_, i) => `l${i + 1}`)
    const changed = long.map((line, i) => (i === 2 || i === 25 ? `${line}!` : line))
    const diff = numberedDiff(long.join('\n'), changed.join('\n'))!
    expect(diff).toContain('+  3 │ l3!')
    expect(diff).toContain('+ 26 │ l26!')
    expect(diff).toContain('…')
    expect(diff).not.toContain('l15')
  })
})

describe('pre-delivery review', () => {
  test('the reviewer reads the changes, comments, and the author answers before the user sees it', async () => {
    const files: Record<string, string> = {
      'src/a.ts': AFTER,
      '.ariadne/skills/review.md': '# 审改动的时候\n空数组一定要查。',
      '.ariadne/skills/release.md': '发版的时候',
    }
    await writeFile(join(home, 'mine.md'), '我自己的做法\n')
    const workspaces = new WorkspaceManager(app, await host(files))
    const project = app.createProject('P')
    app.setProjectSource(project.id, { kind: 'memory', settings: {} })
    const author = app.createThread(project.id, { title: '修导出' })
    let authorTurns = 0
    let reviews = 0
    const model = fakeModel((request, role) => {
      if (role === 'coordinator') return say('')
      const result = lastTool(request)
      if (isReviewer(request)) {
        if (result === null) {
          reviews++
          return [
            call(`r${reviews}a`, 'read_changes', {}),
            { type: 'finish', reason: 'tool-calls' },
          ]
        }
        if (result.includes('"files"')) {
          return [
            call(`r${reviews}b`, 'read_changes', { path: 'src/a.ts' }),
            { type: 'finish', reason: 'tool-calls' },
          ]
        }
        if (result.includes('│')) {
          return [
            call(
              `r${reviews}c`,
              'submit_review',
              reviews === 1
                ? {
                    verdict: 'changes',
                    summary: '空数组会崩',
                    comments: [
                      {
                        path: 'src/a.ts',
                        line: 2,
                        kind: 'bug',
                        body: '空数组时 list[0] 是 undefined',
                      },
                    ],
                  }
                : { verdict: 'pass', summary: '修好了' },
            ),
            { type: 'finish', reason: 'tool-calls' },
          ]
        }
        return say('')
      }
      if (result !== null) return say('')
      authorTurns++
      const ready = {
        kind: 'ready',
        summary: '改了 first',
        headline: '导出修好了',
        ...(authorTurns > 1
          ? { reviewReplies: [{ comment: 1, outcome: 'fixed', note: '加了判断' }] }
          : {}),
      }
      return [call(`a${authorTurns}`, 'report', ready), { type: 'finish', reason: 'tool-calls' }]
    })
    // The workspace is open: the thread worked in it before.
    app.recordWorkspace(author.id, {
      label: '/work/thread',
      branch: 'ariadne/thread',
      base: 'main',
    })
    app.postMessage(project.id, {
      conversationId: author.id,
      author: 'user',
      content: '修一下导出',
    })
    scheduler = new Scheduler({
      app,
      resolveModel: () => model,
      locale: () => 'zh-CN',
      workspaces,
      skillsDirs: [home],
    })
    scheduler.start()
    await scheduler.idle()

    const thread = app.getThread(author.id)
    expect(thread.stage).toBe('review')
    expect(thread.review).toMatchObject({ rounds: 2, reviewing: false })
    const rounds = app.listReviews(author.id)
    expect(rounds.map((r) => r.verdict)).toEqual(['changes', 'pass'])
    // The comment names the line by its text, read from the author's workspace.
    expect(rounds[0]!.comments).toEqual([
      {
        path: 'src/a.ts',
        line: 2,
        quote: '  return list[0].id',
        body: '空数组时 list[0] 是 undefined',
        kind: 'bug',
      },
    ])
    expect(rounds[0]!.replies).toEqual([{ comment: 1, outcome: 'fixed', note: '加了判断' }])

    const reviewerRequest = model.requests.find(isReviewer)!
    expect(system(reviewerRequest)).toContain('空数组一定要查')
    expect(system(reviewerRequest)).toContain('- mine: 我自己的做法')
    const tools = reviewerRequest.tools.map((tool) => tool.name).toSorted()
    expect(tools).toEqual([
      'find_files',
      'list_dir',
      'read_changes',
      'read_file',
      'read_skill',
      'search',
      'submit_review',
      'update_todo',
    ])
    const authorRequest = model.requests.find((r) => system(r).includes('You are a thread of'))!
    expect(system(authorRequest)).toContain('- release (repository): 发版的时候')
    expect(system(authorRequest)).toContain('reviews changes before they reach the user')
    expect(system(authorRequest)).not.toContain('空数组一定要查')
    // The author heard the comments with their numbers.
    const heard = model.requests
      .filter((r) => !isReviewer(r))
      .flatMap((r) => r.messages.map((m) => String(m.content)))
      .find((text) => text.startsWith('<review_comments round'))
    expect(heard).toContain('1. [bug] src/a.ts:2 `return list[0].id`')
  })
})
