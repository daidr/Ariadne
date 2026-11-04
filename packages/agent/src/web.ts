import { lookup as dnsLookup } from 'node:dns/promises'
import { isIP } from 'node:net'

/**
 * Reading the web for agents: fetch a page on the server and hand the model its text.
 * Only public addresses are fetched: the server runs on the user's machine, next to
 * Ariadne itself and whatever else listens on it or on the local network, which a page
 * the model was pointed at must not reach through it.
 */

/** Resolves a host name to its addresses, as `dns.lookup(host, { all: true })` does. */
export type Lookup = (host: string) => Promise<{ address: string }[]>

export interface WebAccess {
  lookup?: Lookup
  fetch?: typeof fetch
}

/** Why a page was not fetched, for the model. */
export class FetchRefused extends Error {}

const defaultLookup: Lookup = (host) => dnsLookup(host, { all: true, verbatim: true })

function ipv4Private(address: string): boolean {
  const [a = 0, b = 0, c = 0] = address.split('.').map(Number)
  return (
    a === 0 ||
    a === 10 ||
    a === 127 ||
    (a === 100 && b >= 64 && b <= 127) ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 0 && c === 0) ||
    (a === 192 && b === 168) ||
    a >= 224
  )
}

/** The IPv4 address an IPv6 one carries (mapped `::ffff:a.b.c.d`, or NAT64), if any. */
function embeddedIpv4(address: string): string | null {
  const lower = address.toLowerCase()
  const dotted = /^(?:::ffff:|64:ff9b::)(\d+\.\d+\.\d+\.\d+)$/.exec(lower)
  if (dotted) return dotted[1]!
  const hex = /^(?:::ffff:|64:ff9b::)([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(lower)
  if (!hex) return null
  const high = parseInt(hex[1]!, 16)
  const low = parseInt(hex[2]!, 16)
  return `${high >> 8}.${high & 255}.${low >> 8}.${low & 255}`
}

/**
 * Loopback, private, link-local, shared, multicast or unspecified: not the public web.
 * 198.18.0.0/15 (benchmarking) is left open: proxies in fake-IP mode hand it out for every
 * name, so refusing it would refuse the whole web behind them.
 */
export function isPrivateAddress(address: string): boolean {
  const kind = isIP(address)
  if (kind === 4) return ipv4Private(address)
  if (kind !== 6) return true
  const embedded = embeddedIpv4(address)
  if (embedded) return ipv4Private(embedded)
  const lower = address.toLowerCase()
  if (lower === '::' || lower === '::1') return true
  const first = parseInt(lower.split(':')[0] || '0', 16)
  return (
    (first & 0xfe00) === 0xfc00 || // unique local
    (first & 0xffc0) === 0xfe80 || // link-local
    (first & 0xff00) === 0xff00 // multicast
  )
}

async function assertPublic(url: URL, lookup: Lookup): Promise<void> {
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new FetchRefused(`Only http and https addresses can be fetched, not ${url.protocol}`)
  }
  if (url.username || url.password) {
    throw new FetchRefused('Addresses with a user name or password are not fetched.')
  }
  const host = url.hostname.replace(/^\[|\]$/g, '')
  let addresses: { address: string }[]
  try {
    addresses = isIP(host) ? [{ address: host }] : await lookup(host)
  } catch {
    throw new FetchRefused(`The host ${host} could not be resolved.`)
  }
  if (!addresses.length) throw new FetchRefused(`The host ${host} could not be resolved.`)
  if (addresses.some((entry) => isPrivateAddress(entry.address))) {
    throw new FetchRefused(
      `${host} is a local or private address. fetch_url reads only the public web; in a thread, use run_command (e.g. curl) for local services.`,
    )
  }
}

const MAX_REDIRECTS = 5
/** More than this of a response is not read. */
export const MAX_BODY_BYTES = 5 * 1024 * 1024
export const FETCH_TIMEOUT_MS = 30_000

export interface FetchedPage {
  url: string
  status: number
  /** The media type, without parameters. */
  type: string
  /** The `charset` parameter of the content type, if any. */
  charset: string | null
  body: Uint8Array
  /** The body was longer than `MAX_BODY_BYTES` and was cut. */
  cut: boolean
}

/**
 * Fetch a public address, following redirects one by one so every address on the way is
 * checked. No cookies or credentials of any kind are sent.
 */
export async function fetchPage(
  address: string,
  options: { signal?: AbortSignal } & WebAccess = {},
): Promise<FetchedPage> {
  const lookup = options.lookup ?? defaultLookup
  const fetchImpl = options.fetch ?? fetch
  const signal = AbortSignal.any([
    AbortSignal.timeout(FETCH_TIMEOUT_MS),
    ...(options.signal ? [options.signal] : []),
  ])
  let url: URL
  try {
    url = new URL(address)
  } catch {
    throw new FetchRefused(`"${address}" is not an address.`)
  }
  for (let hop = 0; ; hop++) {
    await assertPublic(url, lookup)
    const response = await fetchImpl(url, {
      redirect: 'manual',
      signal,
      headers: {
        'user-agent': 'Ariadne',
        accept: 'text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.8',
      },
    })
    const location = response.headers.get('location')
    if (response.status >= 300 && response.status < 400 && location) {
      await response.body?.cancel()
      if (hop >= MAX_REDIRECTS) throw new FetchRefused('Too many redirects.')
      url = new URL(location, url)
      continue
    }
    const [type = '', ...params] = (response.headers.get('content-type') ?? '').split(';')
    const charset =
      params.map((param) => /^\s*charset\s*=\s*"?([^";\s]+)"?/i.exec(param)?.[1]).find(Boolean) ??
      null
    const { body, cut } = await readCapped(response, MAX_BODY_BYTES)
    return {
      url: url.href,
      status: response.status,
      type: type.trim().toLowerCase(),
      charset,
      body,
      cut,
    }
  }
}

async function readCapped(
  response: Response,
  max: number,
): Promise<{ body: Uint8Array; cut: boolean }> {
  if (!response.body) return { body: new Uint8Array(), cut: false }
  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let size = 0
  let cut = false
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value)
    size += value.byteLength
    if (size > max) {
      cut = true
      await reader.cancel()
      break
    }
  }
  const body = new Uint8Array(Math.min(size, max))
  let at = 0
  for (const chunk of chunks) {
    const part = chunk.subarray(0, Math.min(chunk.byteLength, body.byteLength - at))
    body.set(part, at)
    at += part.byteLength
    if (at >= body.byteLength) break
  }
  return { body, cut }
}

export const isHtml = (type: string) => type === 'text/html' || type === 'application/xhtml+xml'

/** Types read as text as they are: plain text, Markdown, JSON, XML, CSV, scripts. */
export function isTextType(type: string): boolean {
  return (
    type.startsWith('text/') ||
    /^application\/(?:json|xml|javascript|ecmascript|x-yaml|yaml|toml|x-sh|csv)$/.test(type) ||
    /\+(?:json|xml)$/.test(type)
  )
}

/** Decode with the page's charset, from the header or (for HTML) a `<meta>` near the top. */
export function decodeText(page: Pick<FetchedPage, 'body' | 'charset' | 'type'>): string {
  let charset = page.charset
  if (!charset && isHtml(page.type)) {
    const head = new TextDecoder('latin1').decode(page.body.subarray(0, 4096))
    charset = /<meta[^>]+charset\s*=\s*["']?([\w-]+)/i.exec(head)?.[1] ?? null
  }
  try {
    return new TextDecoder(charset ?? 'utf-8').decode(page.body)
  } catch {
    return new TextDecoder('utf-8').decode(page.body)
  }
}

const ENTITIES: Record<string, string> = {
  amp: '&',
  lt: '<',
  gt: '>',
  quot: '"',
  apos: "'",
  nbsp: ' ',
  ensp: ' ',
  emsp: ' ',
  thinsp: ' ',
  shy: '',
  zwnj: '',
  zwj: '',
  copy: '©',
  reg: '®',
  trade: '™',
  hellip: '…',
  mdash: '—',
  ndash: '–',
  lsquo: '‘',
  rsquo: '’',
  ldquo: '“',
  rdquo: '”',
  laquo: '«',
  raquo: '»',
  middot: '·',
  bull: '•',
  times: '×',
  divide: '÷',
  deg: '°',
  euro: '€',
  pound: '£',
  yen: '¥',
  cent: '¢',
  sect: '§',
  para: '¶',
  larr: '←',
  rarr: '→',
  uarr: '↑',
  darr: '↓',
}

export function decodeEntities(text: string): string {
  return text.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (whole, name: string) => {
    if (name[0] === '#') {
      const code =
        name[1] === 'x' || name[1] === 'X' ? parseInt(name.slice(2), 16) : Number(name.slice(1))
      return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : whole
    }
    return ENTITIES[name.toLowerCase()] ?? whole
  })
}

/** Left out with all they hold: no text a reader sees, or the site's menus around it. */
const SKIPPED = new Set([
  'script',
  'style',
  'noscript',
  'template',
  'svg',
  'head',
  'iframe',
  'object',
  'canvas',
  'nav',
  'footer',
])
const VOID = new Set([
  'area',
  'base',
  'br',
  'col',
  'embed',
  'hr',
  'img',
  'input',
  'link',
  'meta',
  'source',
  'track',
  'wbr',
])
const BLOCKS = new Set([
  'p',
  'div',
  'section',
  'article',
  'main',
  'header',
  'aside',
  'ul',
  'ol',
  'table',
  'thead',
  'tbody',
  'blockquote',
  'dl',
  'dt',
  'dd',
  'figure',
  'figcaption',
  'details',
  'summary',
  'address',
])

/**
 * A page's readable text, kept close to Markdown: headings, lists, links with their
 * addresses (made absolute against `base`), code blocks, images by their alt text and
 * address. Scripts, styles and the site's navigation are left out.
 */
export async function htmlToText(
  html: string,
  base: string,
): Promise<{ title: string; text: string }> {
  let out = ''
  let title = ''
  let inTitle = false
  let skip = 0
  let pre = 0
  const absolute = (href: string | null) => {
    if (!href) return null
    try {
      const url = new URL(decodeEntities(href), base)
      return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : null
    } catch {
      return null
    }
  }
  const write = (text: string) => {
    if (!skip) out += text
  }
  const block = (prefix = '') => write(`\n\n${prefix}`)
  const rewriter = new HTMLRewriter()
    .on('title', {
      element(el) {
        inTitle = true
        el.onEndTag(() => {
          inTitle = false
        })
      },
    })
    .on('*', {
      element(el) {
        const tag = el.tagName.toLowerCase()
        const end = (fn: () => void) => {
          if (!VOID.has(tag) && !el.selfClosing) el.onEndTag(fn)
        }
        if (SKIPPED.has(tag)) {
          skip++
          end(() => {
            skip--
          })
          return
        }
        if (/^h[1-6]$/.test(tag)) {
          block(`${'#'.repeat(Number(tag[1]))} `)
          end(() => block())
        } else if (tag === 'li') {
          write('\n- ')
        } else if (tag === 'tr') {
          write('\n')
        } else if (tag === 'td' || tag === 'th') {
          write(' | ')
        } else if (tag === 'br') {
          write('\n')
        } else if (tag === 'hr') {
          block('---')
          block()
        } else if (tag === 'pre') {
          block('```\n')
          pre++
          end(() => {
            pre--
            write('\n```')
            block()
          })
        } else if (tag === 'code' && !pre) {
          write('`')
          end(() => write('`'))
        } else if (tag === 'a') {
          const href = absolute(el.getAttribute('href'))
          if (href) {
            write('[')
            end(() => write(`](${href})`))
          }
        } else if (tag === 'img') {
          const src = absolute(el.getAttribute('src'))
          const alt = decodeEntities(el.getAttribute('alt') ?? '').trim()
          if (src) write(`![${alt}](${src})`)
          else if (alt) write(`[image: ${alt}]`)
        } else if (BLOCKS.has(tag)) {
          block()
          end(() => block())
        }
      },
    })
    .onDocument({
      text(chunk) {
        const text = decodeEntities(chunk.text)
        if (inTitle) {
          title += text
          return
        }
        if (skip) return
        out += pre ? text : text.replace(/\s+/g, ' ')
      },
    })
  await rewriter.transform(new Response(html)).text()
  return { title: title.replace(/\s+/g, ' ').trim(), text: tidy(out) }
}

/** Spaces at line ends and starts go, and blank lines collapse, except inside code. */
function tidy(text: string): string {
  const lines: string[] = []
  let code = false
  for (const raw of text.split('\n')) {
    if (raw.trim() === '```') {
      code = !code
      lines.push('```')
      continue
    }
    if (code) {
      lines.push(raw)
      continue
    }
    const line = raw.replace(/[ \t]+/g, ' ').trim()
    if (!line && !lines.at(-1)) continue
    lines.push(line)
  }
  return lines.join('\n').trim()
}
