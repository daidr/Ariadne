import { SKILL_NAME } from '@ariadne/core'
import { readdir, readFile, realpath, stat } from 'node:fs/promises'
import { isAbsolute, join, sep } from 'node:path'

/**
 * Skills: ways of working written down in Markdown, one per skill: a file `<name>.md`,
 * or a folder `<name>/` holding `SKILL.md` and whatever files it works with. They are
 * read from the repository (`.ariadne/skills/`, then `.agents/skills/`) and from the
 * user's own folders for every project. A skill's first line, or the `description:` of
 * its front matter, says when to use it. Models see only names and those lines, and read
 * a whole skill, and the files beside it, when their work fits it.
 */
export interface Skill {
  /** The file's name without `.md`, or the folder's. */
  name: string
  /** When to use it: the file's first line. */
  when: string
  /** The repository's, or the user's own for every project. */
  source: 'repo' | 'local'
}

/**
 * Where a repository keeps its skills, from the root of its checkout; the earlier wins
 * a name. The second is the folder shared between tools.
 */
export const REPO_SKILLS_DIRS = ['.ariadne/skills', '.agents/skills'] as const
/** The skill that replaces the built-in review guidance. */
const REVIEW_SKILL = 'review'
/** The file a skill folder is read from. */
export const SKILL_FILE = 'SKILL.md'
/** Files beside a skill listed at most, and how deep they are looked for. */
export const MAX_SKILL_FILES = 60
const MAX_SKILL_FILE_DEPTH = 3
/** Folders of one skill looked into at most, when its files are listed. */
const MAX_SKILL_DIRS = 30
/** A longer skill is cut: it is read into the conversation whole. */
export const MAX_SKILL_CHARS = 32_000
/** Skills listed at most, per place; the rest are left out by name order. */
export const MAX_SKILLS = 40
const MAX_SKILL_BYTES = 256 * 1024
const MAX_WHEN = 200

/** What skills are read through: a workspace, or the read-only view of a source. */
export interface SkillFiles {
  list(path: string): Promise<{ name: string; kind: string }[]>
  readBytes(path: string, maxBytes: number): Promise<Uint8Array>
}

/**
 * When to use a skill, from its text: `description:` of a front matter block if it has
 * one, else its first non-empty line without heading or quote marks. `''` when empty.
 */
export function whenToUse(text: string): string {
  const lines = text.replace(/^﻿/, '').split(/\r?\n/)
  let first = 0
  if (lines[0]?.trim() === '---') {
    const end = lines.findIndex((line, i) => i > 0 && line.trim() === '---')
    if (end > 0) {
      const matter = lines.slice(1, end)
      const at = matter.findIndex((line) => /^description:/i.test(line))
      if (at >= 0) {
        let description = matter[at]!.replace(/^description:\s*/i, '').trim()
        // A block (`>`, `|`) or a value on the next lines: the indented lines that follow.
        if (!description || /^[>|][+-]?$/.test(description)) {
          const rest = matter.slice(at + 1)
          const stop = rest.findIndex((line) => line.trim() && !/^\s/.test(line))
          description = rest.slice(0, stop < 0 ? rest.length : stop).join(' ')
        }
        if (description.trim()) return oneLine(description.trim().replace(/^["']|["']$/g, ''))
      }
      first = end + 1
    }
  }
  const line = lines.slice(first).find((item) => item.trim())
  return line ? oneLine(line.replace(/^\s*(?:#+|>+|[-*])\s*/, '')) : ''
}

const oneLine = (text: string) => {
  const flat = text.replace(/\s+/g, ' ').trim()
  return flat.length > MAX_WHEN ? `${flat.slice(0, MAX_WHEN)}…` : flat
}

/** A skill file's name without `.md`, when it names a skill. */
export function skillName(file: string): string | null {
  if (!file.toLowerCase().endsWith('.md')) return null
  const name = file.slice(0, -3)
  return SKILL_NAME.test(name) ? name : null
}

const decode = (data: Uint8Array) => {
  const text = new TextDecoder().decode(data)
  return text.length > MAX_SKILL_CHARS
    ? `${text.slice(0, MAX_SKILL_CHARS)}\n[cut: the skill is longer]`
    : text
}

/** A skill as loaded: its text, and for a folder where its files are read. */
interface Loaded extends Skill {
  text: string
  /** A folder's path as the model is told it; `null` for a single file. */
  folder: string | null
  files: SkillFiles
  /** The folder's path for `files`. */
  root: string
  /** From a folder of Ariadne's own, not the one shared between tools. */
  own: boolean
}

/** One place skills are read from; `shown` is its path as the model is told it. */
interface Place {
  files: SkillFiles
  dir: string
  shown: string
  source: Skill['source']
  own: boolean
}

/**
 * A folder of this machine read as a place: paths are from the folder. A skill, file or
 * folder, may be a link to elsewhere; what is inside a skill's folder is read only where
 * it really lies in that folder, so a link in it reaches nothing outside.
 */
function directoryFiles(dir: string): SkillFiles {
  /** The real path of `path`, when it is in its skill's real folder; else it throws. */
  const within = async (path: string) => {
    const parts = path.split('/').filter((part) => part && part !== '.')
    const at = await realpath(join(dir, ...parts))
    if (parts.length < 2) return at
    const folder = await realpath(join(dir, parts[0]!))
    if (at !== folder && !at.startsWith(folder + sep)) throw new Error('outside the skill')
    return at
  }
  return {
    async list(path) {
      const top = !path.split('/').some((part) => part && part !== '.')
      const at = await within(path)
      const entries = await readdir(at, { withFileTypes: true })
      const found: { name: string; kind: string }[] = []
      for (const entry of entries) {
        let folder = entry.isDirectory()
        let file = entry.isFile()
        if (entry.isSymbolicLink()) {
          // A linked skill counts as what it points to; a broken link is left out. Inside
          // a skill, linked folders are not followed, and linked files only within it.
          const info = await stat(join(at, entry.name)).catch(() => null)
          folder = top && (info?.isDirectory() ?? false)
          file =
            (info?.isFile() ?? false) &&
            (top ||
              (await within(`${path}/${entry.name}`).then(
                () => true,
                () => false,
              )))
        }
        if (folder || file) found.push({ name: entry.name, kind: folder ? 'directory' : 'file' })
      }
      return found
    },
    async readBytes(path, maxBytes) {
      const at = await within(path)
      const info = await stat(at)
      if (!info.isFile()) throw new Error('not a file')
      if (info.size > maxBytes) throw new Error('too large')
      return readFile(at)
    },
  }
}

const under = (dir: string, name: string) => (dir === '.' ? name : `${dir}/${name}`)

/**
 * The skills of one place, by name. A folder wins a file of the same name there;
 * folders without `SKILL.md` and unreadable files are left out.
 */
async function fromPlace(place: Place): Promise<Loaded[]> {
  let entries: { name: string; kind: string }[]
  try {
    entries = await place.files.list(place.dir)
  } catch {
    return []
  }
  // A name's candidates, the folder first: it wins when it holds SKILL.md, and a folder
  // of notes beside `<name>.md` leaves that file the skill.
  const candidates = new Map<string, { path: string; folder: string | null }[]>()
  const add = (name: string, candidate: { path: string; folder: string | null }) => {
    const known = candidates.get(name) ?? []
    candidates.set(name, candidate.folder === null ? [...known, candidate] : [candidate, ...known])
  }
  for (const entry of entries) {
    if (entry.kind === 'file') {
      const name = skillName(entry.name)
      if (name) add(name, { path: under(place.dir, entry.name), folder: null })
    } else if (entry.kind === 'directory' && SKILL_NAME.test(entry.name)) {
      const folder = under(place.dir, entry.name)
      add(entry.name, { path: `${folder}/${SKILL_FILE}`, folder })
    }
  }
  const found: Loaded[] = []
  for (const [name, tried] of [...candidates].toSorted(([a], [b]) => a.localeCompare(b))) {
    if (found.length >= MAX_SKILLS) break
    for (const { path, folder } of tried) {
      try {
        const text = decode(await place.files.readBytes(path, MAX_SKILL_BYTES))
        found.push({
          name,
          when: whenToUse(text),
          source: place.source,
          text,
          folder: folder === null ? null : under(place.shown, name),
          files: place.files,
          root: folder ?? '',
          own: place.own,
        })
        break
      } catch {
        // No SKILL.md, too large, or gone meanwhile: not a skill to offer.
      }
    }
  }
  return found
}

/** A path beside a skill as asked for, with `/` between parts; `null` when it leaves the folder. */
function inside(file: string): string | null {
  const parts = file
    .replaceAll('\\', '/')
    .split('/')
    .filter((part) => part && part !== '.')
  if (!parts.length || isAbsolute(file) || /^[a-z]:/i.test(file) || parts.includes('..'))
    return null
  return parts.join('/')
}

/**
 * The skills a conversation can use, read once. One name is one skill: the repository's
 * win over the user's own, and within either the earlier place wins.
 */
export class SkillSet {
  constructor(private readonly found: Map<string, Loaded>) {}

  static empty(): SkillSet {
    return new SkillSet(new Map())
  }

  list(): Skill[] {
    return [...this.found.values()].map(({ name, when, source }) => ({ name, when, source }))
  }

  get(name: string): (Skill & { text: string; folder: string | null }) | null {
    const skill = this.found.get(name)
    if (!skill) return null
    const { name: named, when, source, text, folder } = skill
    return { name: named, when, source, text, folder }
  }

  /** The files beside a skill, as paths from its folder; none for a single file. */
  async files(name: string): Promise<{ paths: string[]; more: boolean }> {
    const skill = this.found.get(name)
    const paths: string[] = []
    let more = false
    if (!skill || skill.folder === null) return { paths, more }
    let listed = 0
    const walk = async (dir: string, depth: number) => {
      if (listed++ >= MAX_SKILL_DIRS) {
        more = true
        return
      }
      const entries = await skill.files
        .list(dir === '.' ? skill.root : `${skill.root}/${dir}`)
        .catch(() => [])
      for (const entry of entries.toSorted((a, b) => a.name.localeCompare(b.name))) {
        // Enough to tell there is more: nothing further is looked at.
        if (more && paths.length >= MAX_SKILL_FILES) return
        const path = under(dir, entry.name)
        if (entry.kind === 'directory') {
          if (depth < MAX_SKILL_FILE_DEPTH) await walk(path, depth + 1)
          else more = true
        } else if (path !== SKILL_FILE) {
          if (paths.length < MAX_SKILL_FILES) paths.push(path)
          else more = true
        }
      }
    }
    await walk('.', 1)
    return { paths, more }
  }

  /**
   * A file beside a skill, as text; `'binary'` when it is not text, `null` when there is
   * none there to read.
   */
  async read(name: string, file: string): Promise<{ text: string } | 'binary' | null> {
    const skill = this.found.get(name)
    const path = inside(file)
    if (!skill || skill.folder === null || !path) return null
    try {
      const data = await skill.files.readBytes(`${skill.root}/${path}`, MAX_SKILL_BYTES)
      return data.subarray(0, 8192).includes(0) ? 'binary' : { text: decode(data) }
    } catch {
      return null
    }
  }

  /**
   * The skill that replaces the built-in review guidance: `review`, when it comes from a
   * folder of Ariadne's own. One in a folder shared between tools was written for their
   * way of reviewing, and stays an ordinary skill.
   */
  review(): (Skill & { text: string; folder: string | null }) | null {
    return this.found.get(REVIEW_SKILL)?.own ? this.get(REVIEW_SKILL) : null
  }
}

export async function loadSkills(input: {
  repo: SkillFiles | null
  /**
   * The user's own folders for every project; the earlier wins a name. The first is
   * Ariadne's own, the rest are shared between tools.
   */
  localDirs: readonly string[]
}): Promise<SkillSet> {
  const places: Place[] = [
    ...(input.repo
      ? REPO_SKILLS_DIRS.map((dir) => ({
          files: input.repo!,
          dir,
          shown: dir,
          source: 'repo' as const,
          own: dir === REPO_SKILLS_DIRS[0],
        }))
      : []),
    ...input.localDirs.map((dir, index) => ({
      files: directoryFiles(dir),
      dir: '.',
      shown: dir,
      source: 'local' as const,
      own: index === 0,
    })),
  ]
  const all = new Map<string, Loaded>()
  for (const skills of await Promise.all(places.map(fromPlace)))
    for (const skill of skills) {
      const known = all.get(skill.name)
      // The review guidance is Ariadne's own to replace: its `review` is not lost to one
      // an earlier shared folder holds.
      if (!known || (skill.name === REVIEW_SKILL && skill.own && !known.own))
        all.set(skill.name, skill)
    }
  return new SkillSet(new Map([...all].toSorted(([a], [b]) => a.localeCompare(b))))
}
