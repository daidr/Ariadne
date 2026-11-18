import { repositoryOf } from '../hosting'

const input = (await Bun.stdin.json()) as string[]
await Bun.write(Bun.stdout, JSON.stringify(input.map(repositoryOf)))
