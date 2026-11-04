/**
 * Markdown split into text and code, alternating: even parts are text, odd parts are code,
 * where a link is only its characters. Code is a fenced block (``` or ~~~, closed by a
 * fence of the same character at least as long, or open to the end), an indented block
 * (four spaces or a tab past the enclosing list item's content, after a blank line), or an
 * inline span (a run of backticks up to the next run of the same length, within a
 * paragraph). The parts joined give the text back unchanged.
 */
export function splitCode(markdown: string): string[] {
  const parts: string[] = []
  let text = ''
  let code = ''
  const endCode = () => {
    if (!code) return
    parts.push(...splitSpans(text), code)
    text = ''
    code = ''
  }

  let fence: { char: string; length: number } | null = null
  /** Where the current list item's content starts, in columns; 0 outside a list. */
  let listContent = 0
  let previous: 'blank' | 'text' | 'code' = 'blank'
  for (const line of markdown.split(/(?<=\n)/)) {
    const body = line.replace(/\r?\n$/, '')
    if (fence) {
      code += line
      const close = /^\s*(`+|~+)\s*$/.exec(body)
      if (close?.[1]?.[0] === fence.char && close[1].length >= fence.length) fence = null
      continue
    }
    if (!body.trim()) {
      // Blank lines hold no links; inside an indented block they belong to it.
      if (code) code += line
      else text += line
      if (previous !== 'code') previous = 'blank'
      continue
    }
    const indent = columns(body)
    const indented = indent >= listContent + 4
    // An indented block starts after a blank line (it cannot interrupt a paragraph).
    if (indented && previous !== 'text') {
      code += line
      previous = 'code'
      continue
    }
    const opening = /^\s*(`{3,}|~{3,})(.*)$/.exec(body)
    if (opening && !indented && !(opening[1]![0] === '`' && opening[2]!.includes('`'))) {
      endCode()
      code = line
      fence = { char: opening[1]![0]!, length: opening[1]!.length }
      // Ends where the fence closes; what follows starts afresh.
      previous = 'blank'
      continue
    }
    endCode()
    const item = /^(\s*)([-+*]|\d{1,9}[.)])([ \t]+)\S/.exec(body)
    if (item) {
      const gap = columns(item[3]!)
      listContent = columns(item[1]!) + item[2]!.length + (gap > 4 ? 1 : gap)
    } else if (previous === 'blank' && indent < listContent) {
      // A paragraph that is not indented under the item ends the list.
      listContent = 0
    }
    text += line
    previous = 'text'
  }
  endCode()
  parts.push(...splitSpans(text))
  return parts
}

/** Leading whitespace in columns, a tab reaching the next multiple of four. */
function columns(line: string): number {
  let width = 0
  for (const char of line) {
    if (char === ' ') width += 1
    else if (char === '\t') width += 4 - (width % 4)
    else break
  }
  return width
}

/** Text split around inline code spans: even parts text, odd parts spans. */
function splitSpans(text: string): string[] {
  const parts: string[] = []
  let rest = ''
  let at = 0
  const runs = /`+/g
  let open: RegExpExecArray | null
  while ((open = runs.exec(text))) {
    const ticks = open[0]
    // The next run of exactly as many backticks, before the paragraph ends.
    const close = new RegExp(`(?<!\`)${ticks}(?!\`)`, 'g')
    close.lastIndex = open.index + ticks.length
    const end = close.exec(text)
    const between = end ? text.slice(open.index + ticks.length, end.index) : ''
    if (!end || /\n[ \t]*\n/.test(between)) continue
    rest += text.slice(at, open.index)
    parts.push(rest, text.slice(open.index, end.index + ticks.length))
    rest = ''
    at = end.index + ticks.length
    runs.lastIndex = at
  }
  parts.push(rest + text.slice(at))
  return parts
}
