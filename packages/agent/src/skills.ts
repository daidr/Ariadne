import { SKILL_NAME } from '@ariadne/core'
import { readdir, readFile, stat } from 'node:fs/promises'
import { join } from 'node:path'

/**
 * Skills: ways of working written down as Markdown files, one per skill, in the
 * repository's `.ariadne/skills/` or in Ariadne's home (`<home>/skills/`) for every
 * project. A file's first line says when to use it. Models see only names and those
 * lines, and read a whole skill when their work fits it.
 */
export interface Skill {
  name: string
  /** When to use it: the file's first line. */
  when: string
  /** The repository's, or the user's own for every project. */
  source: 'repo' | 'local'
}

/** Where the repository keeps its skills, from the root of its checkout. */
export const REPO_SKILLS_DIR = '.ariadne/skills'
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
      const description = lines
        .slice(1, end)
        .map((line) => /^description:\s*(.*)$/i.exec(line.trim())?.[1])
        .find((value) => value !== undefined)
      if (description?.trim()) return oneLine(description.replace(/^["']|["']$/g, ''))
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

/** The skills of one place, by name, with their text. Unreadable files are left out. */
type Found = Map<string, { when: string; text: string }>

async function fromRepository(files: SkillFiles | null): Promise<Found> {
  const found: Found = new Map()
  if (!files) return found
  let entries: { name: string; kind: string }[]
  try {
    entries = await files.list(REPO_SKILLS_DIR)
  } catch {
    return found
  }
  const names = entries
    .filter((entry) => entry.kind === 'file')
    .flatMap((entry) => (skillName(entry.name) ? [entry.name] : []))
    .toSorted()
    .slice(0, MAX_SKILLS)
  for (const file of names) {
    try {
      const text = decode(await files.readBytes(`${REPO_SKILLS_DIR}/${file}`, MAX_SKILL_BYTES))
      found.set(skillName(file)!, { when: whenToUse(text), text })
    } catch {
      // Too large, or gone meanwhile: not a skill to offer.
    }
  }
  return found
}

async function fromDirectory(dir: string | null): Promise<Found> {
  const found: Found = new Map()
  if (!dir) return found
  let names: string[]
  try {
    names = (await readdir(dir)).filter((file) => skillName(file)).toSorted()
  } catch {
    return found
  }
  for (const file of names.slice(0, MAX_SKILLS)) {
    try {
      const path = join(dir, file)
      const info = await stat(path)
      if (!info.isFile() || info.size > MAX_SKILL_BYTES) continue
      const text = decode(await readFile(path))
      found.set(skillName(file)!, { when: whenToUse(text), text })
    } catch {
      // Unreadable: left out.
    }
  }
  return found
}

/** The skills a conversation can use, read once; the repository's win over same-named local ones. */
export class SkillSet {
  constructor(private readonly found: Map<string, Skill & { text: string }>) {}

  static empty(): SkillSet {
    return new SkillSet(new Map())
  }

  list(): Skill[] {
    return [...this.found.values()].map(({ name, when, source }) => ({ name, when, source }))
  }

  get(name: string): (Skill & { text: string }) | null {
    return this.found.get(name) ?? null
  }
}

export async function loadSkills(input: {
  repo: SkillFiles | null
  localDir: string | null
}): Promise<SkillSet> {
  const [repo, local] = await Promise.all([
    fromRepository(input.repo),
    fromDirectory(input.localDir),
  ])
  const all = new Map<string, Skill & { text: string }>()
  for (const [name, skill] of local) all.set(name, { name, source: 'local', ...skill })
  for (const [name, skill] of repo) all.set(name, { name, source: 'repo', ...skill })
  return new SkillSet(new Map([...all].toSorted(([a], [b]) => a.localeCompare(b))))
}
