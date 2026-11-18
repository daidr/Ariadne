import { loadSkills, skillName, whenToUse } from '../skills'

const input = (await Bun.stdin.json()) as {
  dirs: string[]
  descriptions: string[]
  names: string[]
  reads: { name: string; path: string }[]
}
const skills = await loadSkills({ repo: null, localDirs: input.dirs })
console.info(
  JSON.stringify({
    descriptions: input.descriptions.map(whenToUse),
    names: input.names.map(skillName),
    list: skills.list(),
    review: skills.review(),
    contents: skills.list().map(({ name }) => skills.get(name)),
    files: await Promise.all(skills.list().map(({ name }) => skills.files(name))),
    reads: await Promise.all(input.reads.map(({ name, path }) => skills.read(name, path))),
    named: skills.named('review'),
  }),
)
