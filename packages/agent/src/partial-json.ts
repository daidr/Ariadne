/**
 * JSON read while it is still being written: the arguments of a tool call as the model
 * sends them, cut off anywhere. What is there is read as far as it goes: a string ends
 * where the text does, a number or a word (`true`, `null`) that the cut may have shortened
 * is left out with its key, and so is a key without a value yet.
 */

/** Where in the value the text stops: keys and indexes down to what is still open. */
export type OpenPath = (string | number)[]

export interface PartialJson {
  value: unknown
  /**
   * The innermost value the text stops inside (a string, an object or an array), by its
   * path from the top; `null` when the text is whole.
   */
  open: OpenPath | null
}

const MISSING = Symbol('missing')

/** `null` when the text does not start as JSON at all. */
export function readPartialJson(text: string): PartialJson | null {
  let at = 0
  let open: OpenPath | null = null
  const path: OpenPath = []
  const stopHere = () => {
    open ??= [...path]
  }
  const space = () => {
    while (at < text.length && ' \t\n\r'.includes(text[at]!)) at++
  }

  function string(): string {
    at++ // the opening quote
    let out = ''
    while (at < text.length) {
      const char = text[at]!
      if (char === '"') {
        at++
        return out
      }
      if (char !== '\\') {
        out += char
        at++
        continue
      }
      const escape = text[at + 1]
      // An escape the cut split is not read: it comes whole with the next piece.
      if (escape === undefined) break
      if (escape === 'u') {
        const hex = text.slice(at + 2, at + 6)
        if (hex.length < 4) break
        const code = Number.parseInt(hex, 16)
        if (Number.isNaN(code)) throw new SyntaxError('bad escape')
        out += String.fromCharCode(code)
        at += 6
        continue
      }
      const plain = { n: '\n', t: '\t', r: '\r', b: '\b', f: '\f' }[escape]
      out += plain ?? escape
      at += 2
    }
    at = text.length
    // Half of a pair of surrogates is no character yet.
    if (/[\uD800-\uDBFF]$/.test(out)) out = out.slice(0, -1)
    stopHere()
    return out
  }

  function value(): unknown {
    space()
    if (at >= text.length) return MISSING
    const char = text[at]!
    if (char === '"') return string()
    if (char === '{') {
      at++
      const object: Record<string, unknown> = {}
      for (;;) {
        space()
        if (at >= text.length) break
        if (text[at] === '}') {
          at++
          return object
        }
        if (text[at] === ',') {
          at++
          continue
        }
        if (text[at] !== '"') throw new SyntaxError('expected a key')
        const before = open
        const key = string()
        // A key the text stops in is not one yet.
        if (open !== before) {
          open = before
          break
        }
        space()
        if (at >= text.length) break
        if (text[at] !== ':') throw new SyntaxError('expected a colon')
        at++
        path.push(key)
        const item = value()
        if (item !== MISSING) object[key] = item
        path.pop()
        if (open || item === MISSING) break
      }
      stopHere()
      return object
    }
    if (char === '[') {
      at++
      const array: unknown[] = []
      for (;;) {
        space()
        if (at >= text.length) break
        if (text[at] === ']') {
          at++
          return array
        }
        if (text[at] === ',') {
          at++
          continue
        }
        path.push(array.length)
        const item = value()
        if (item !== MISSING) array.push(item)
        path.pop()
        if (open || item === MISSING) break
      }
      stopHere()
      return array
    }
    const word = /^[-+0-9.eE]+|^[a-z]+/.exec(text.slice(at))?.[0]
    if (!word) throw new SyntaxError('unexpected character')
    // At the very end it may be cut short: `12` of `123`, `tru` of `true`.
    if (at + word.length >= text.length) {
      at = text.length
      return MISSING
    }
    at += word.length
    return JSON.parse(word)
  }

  try {
    const read = value()
    if (read === MISSING) return null
    return { value: read, open }
  } catch {
    return null
  }
}

/** Whether `open` is at or under `path`: that part of the value is still being written. */
export function isOpen(open: OpenPath | null, ...path: OpenPath): boolean {
  return !!open && path.every((key, index) => open[index] === key)
}
