import { commandParts, commandVerdict, type CommandRule } from '../limits'

const input = (await Bun.stdin.json()) as { commands: string[]; rules: CommandRule[][] }
console.info(
  JSON.stringify(
    input.commands.map((command) => ({
      parts: commandParts(command),
      verdicts: input.rules.map((rules) => commandVerdict(command, rules)),
    })),
  ),
)
