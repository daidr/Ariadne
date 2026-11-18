import { localRefusal, remoteRefusal } from '../reference-guard'

const input = (await Bun.stdin.json()) as {
  home: string
  own: string[]
  local: string[]
  remote: string[]
}
console.info(
  JSON.stringify({
    local: input.local.map((path) => localRefusal(path, input.own, input.home)),
    remote: input.remote.map(remoteRefusal),
  }),
)
