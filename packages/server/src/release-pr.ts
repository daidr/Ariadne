import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * The release workflow's steps that need more than a shell line (`.github/workflows/release.yml`).
 *
 *   bun packages/server/src/release-pr.ts plan      is the last commit a merged release?
 *   bun packages/server/src/release-pr.ts prepare   write the next alpha's version and notes
 *   bun packages/server/src/release-pr.ts notes <v> print what CHANGELOG.md says about <v>
 *
 * The version lives in the root package.json. A release is the commit that changed it: the
 * commits after the last such change are what the next release carries.
 */

const root = join(import.meta.dirname, '../../..')
const PACKAGE = join(root, 'package.json')
const CHANGELOG = join(root, 'CHANGELOG.md')
/** What a release commit's subject starts with, so the next notes leave it out. */
export const RELEASE_SUBJECT = '发布 '

const ALPHA = /^(\d+)\.(\d+)\.(\d+)-alpha\.(\d+)$/
const STABLE = /^(\d+)\.(\d+)\.(\d+)$/

/** The alpha after `version`: the next number, or the first alpha of the next minor version. */
export function nextAlpha(version: string): string {
  const alpha = ALPHA.exec(version)
  if (alpha) return `${alpha[1]}.${alpha[2]}.${alpha[3]}-alpha.${Number(alpha[4]) + 1}`
  const stable = STABLE.exec(version)
  if (stable) return `${stable[1]}.${Number(stable[2]) + 1}.0-alpha.1`
  throw new Error(`not a version this workflow can follow: ${version}`)
}

/**
 * The notes for a release: one line per commit, newest first, without release commits and
 * without what an earlier release in `changelog` already listed.
 */
export function releaseNotes(subjects: string[], changelog = ''): string {
  const listed = new Set(changelog.split('\n').map((line) => line.trim()))
  return subjects
    .filter((subject) => subject.trim() && !subject.startsWith(RELEASE_SUBJECT))
    .map((subject) => `- ${subject.trim()}`)
    .filter((line) => !listed.has(line))
    .join('\n')
}

/** CHANGELOG.md with this release on top. */
export function withRelease(changelog: string, version: string, date: string, notes: string) {
  const section = `## ${version} (${date})\n\n${notes}\n`
  const body = changelog.replace(/^# .*\n+/, '')
  return `# 更新日志\n\n${section}${body ? `\n${body}` : ''}`
}

/** What CHANGELOG.md says about `version`, without its heading. */
export function notesOf(changelog: string, version: string): string {
  const sections = changelog.split(/^## /m).slice(1)
  const section = sections.find((s) => s.startsWith(`${version} `) || s.startsWith(`${version}\n`))
  return section ? section.slice(section.indexOf('\n') + 1).trim() : ''
}

/** package.json with another version, its formatting kept. */
export function withVersion(json: string, version: string): string {
  const next = json.replace(/^(\s*"version":\s*)"[^"]*"/m, `$1${JSON.stringify(version)}`)
  if (next === json) throw new Error('package.json has no version')
  return next
}

function git(...args: string[]): string {
  const result = Bun.spawnSync(['git', ...args], { cwd: root, windowsHide: true })
  if (result.exitCode !== 0) throw new Error(`git ${args.join(' ')}: ${result.stderr.toString()}`)
  return result.stdout.toString().trim()
}

function versionAt(ref: string): string | null {
  try {
    return (JSON.parse(git('show', `${ref}:package.json`)) as { version?: string }).version ?? null
  } catch {
    return null
  }
}

/** Hand values to the next steps of the workflow. */
function output(values: Record<string, string>) {
  const lines = Object.entries(values).map(([key, value]) => `${key}=${value}`)
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `${lines.join('\n')}\n`)
  console.info(lines.join('\n'))
}

/** A merged release PR changed the version: build and publish it. Anything else updates the PR. */
function plan() {
  const version = versionAt('HEAD')
  const before = versionAt('HEAD^')
  const publish = !!version && !!before && version !== before
  output({ publish: String(publish), version: version ?? '' })
}

/** The next alpha's version and notes, written into the working tree. */
function prepare() {
  const json = readFileSync(PACKAGE, 'utf8')
  const current = (JSON.parse(json) as { version?: string }).version
  if (!current) throw new Error('package.json has no version')
  // From the release before the last one: a release PR merged before it caught up with master
  // leaves commits behind the last release that no notes listed yet. What the changelog
  // already lists is left out.
  const since = git('log', '-n2', '--format=%H', '-G"version":', '--', 'package.json')
    .split('\n')
    .at(-1)!
  const subjects = git('log', '--format=%s', `${since}..HEAD`).split('\n')
  const changelog = existsSync(CHANGELOG) ? readFileSync(CHANGELOG, 'utf8') : ''
  const notes = releaseNotes(subjects, changelog)
  if (!notes) {
    output({ changed: 'false', version: current })
    return
  }
  const version = nextAlpha(current)
  const date = new Date().toISOString().slice(0, 10)
  writeFileSync(PACKAGE, withVersion(json, version))
  writeFileSync(CHANGELOG, withRelease(changelog, version, date, notes))
  output({ changed: 'true', version, title: `${RELEASE_SUBJECT}${version}` })
}

if (import.meta.main) {
  const [command, arg] = process.argv.slice(2)
  if (command === 'plan') plan()
  else if (command === 'prepare') prepare()
  else if (command === 'notes' && arg) console.info(notesOf(readFileSync(CHANGELOG, 'utf8'), arg))
  else {
    console.error('usage: release-pr.ts plan | prepare | notes <version>')
    process.exit(2)
  }
}
