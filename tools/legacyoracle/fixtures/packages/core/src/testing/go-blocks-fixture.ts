import { parseAnswer, parseBlock, describeAnswer, isAskBlock } from '../blocks'
import { DomainError } from '../errors'

const tests = (await Bun.stdin.json()) as { block: unknown; answer?: unknown }[]
console.info(
  JSON.stringify(
    tests.map((test) => {
      try {
        const block = parseBlock(test.block)
        const again = parseBlock(block)
        let answer = null
        let description = null
        if (Object.hasOwn(test, 'answer')) {
          if (!isAskBlock(block)) throw new Error('answer needs an ask block')
          answer = parseAnswer(block, test.answer)
          description = describeAnswer(block, answer)
        }
        return { block, again, ask: isAskBlock(block), answer, description }
      } catch (caught) {
        return {
          error: caught instanceof DomainError ? caught.toLocalized() : { message: String(caught) },
        }
      }
    }),
  ),
)
