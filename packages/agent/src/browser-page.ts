import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Ariadne } from '@ariadne/core'
import type { BrowserLocator } from './browser'
import { LoopbackProxy, type Tunnel } from './browser-proxy'

/** A picture of the page, as the screenshot action takes it. */
export interface PageShot {
  data: Uint8Array
  mime: 'image/png' | 'image/jpeg'
  width: number
  height: number
}

export interface PageLine {
  level: string
  text: string
}

/** A full page is cut here: longer ones are rarely looked at past it, and images stay small. */
export const MAX_PAGE_HEIGHT = 8000
/** Larger images are refused by model services; past it, a JPEG is taken instead. */
const MAX_IMAGE_BYTES = 5 * 1024 * 1024
const START_TIMEOUT_MS = 20_000
const LOAD_TIMEOUT_MS = 30_000
/** After an action that starts loading a page, how long it may take before we go on. */
const SETTLE_TIMEOUT_MS = 10_000
/** A browser nobody used for this long is closed. */
export const IDLE_MS = 10 * 60 * 1000
/** Console lines and failed requests kept, newest last. */
const MAX_LINES = 200
/** A snapshot lists at most this many lines. */
const MAX_SNAPSHOT_LINES = 400
const MAX_SNAPSHOT_CHARS = 20_000

/** A minimal DevTools protocol client over the browser's WebSocket, with flat sessions. */
class Cdp {
  private nextId = 1
  private readonly pending = new Map<
    number,
    { resolve: (value: unknown) => void; reject: (error: Error) => void }
  >()
  private readonly listeners = new Set<(method: string, params: any, session?: string) => void>()
  closed = false

  private constructor(private readonly socket: WebSocket) {
    socket.addEventListener('message', (event) => {
      const message = JSON.parse(String(event.data)) as {
        id?: number
        result?: unknown
        error?: { message: string }
        method?: string
        params?: unknown
        sessionId?: string
      }
      if (message.id !== undefined) {
        const waiting = this.pending.get(message.id)
        this.pending.delete(message.id)
        if (message.error) waiting?.reject(new Error(message.error.message))
        else waiting?.resolve(message.result)
      } else if (message.method) {
        for (const listener of this.listeners) {
          listener(message.method, message.params, message.sessionId)
        }
      }
    })
    socket.addEventListener('close', () => {
      this.closed = true
      for (const waiting of this.pending.values()) waiting.reject(new Error('the browser closed'))
      this.pending.clear()
    })
  }

  static connect(url: string): Promise<Cdp> {
    return new Promise((resolve, reject) => {
      const socket = new WebSocket(url)
      socket.addEventListener('open', () => resolve(new Cdp(socket)), { once: true })
      socket.addEventListener('error', () => reject(new Error('could not reach the browser')), {
        once: true,
      })
    })
  }

  send<T = any>(method: string, params: object = {}, sessionId?: string): Promise<T> {
    if (this.closed) return Promise.reject(new Error('the browser closed'))
    const id = this.nextId++
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (value: unknown) => void, reject })
      this.socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
    })
  }

  on(listener: (method: string, params: any, session?: string) => void): void {
    this.listeners.add(listener)
  }

  close(): void {
    this.socket.close()
  }
}

/** Waits for `promise`, or fails with `message` after `ms`, or when `signal` aborts. */
function within<T>(promise: Promise<T>, ms: number, message: string, signal?: AbortSignal) {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(message)), ms)
    const stop = () => reject(new Error('stopped'))
    signal?.addEventListener('abort', stop, { once: true })
    promise.then(resolve, reject).finally(() => {
      clearTimeout(timer)
      signal?.removeEventListener('abort', stop)
    })
  })
}

/** The DevTools address the browser prints on stderr once it listens. */
async function devtoolsUrl(stderr: ReadableStream<Uint8Array>): Promise<string> {
  const decoder = new TextDecoder()
  let seen = ''
  for await (const chunk of stderr) {
    seen += decoder.decode(chunk, { stream: true })
    const match = /DevTools listening on (ws:\/\/\S+)/.exec(seen)
    if (match) return match[1]!
    if (seen.length > 64_000) seen = seen.slice(-4000)
  }
  throw new Error(`the browser exited: ${seen.trim().split('\n').slice(-3).join(' ')}`)
}

/** Text of a console message's arguments, as the console would print them. */
function consoleText(args: { value?: unknown; description?: string }[]): string {
  return args
    .map((arg) =>
      typeof arg.value === 'string' ? arg.value : (arg.description ?? JSON.stringify(arg.value)),
    )
    .join(' ')
}

/** Keys the press action knows, as DevTools wants them. */
const KEYS: Record<string, { code: string; keyCode: number; text?: string }> = {
  Enter: { code: 'Enter', keyCode: 13, text: '\r' },
  Tab: { code: 'Tab', keyCode: 9 },
  Escape: { code: 'Escape', keyCode: 27 },
  Backspace: { code: 'Backspace', keyCode: 8 },
  Delete: { code: 'Delete', keyCode: 46 },
  Space: { code: 'Space', keyCode: 32, text: ' ' },
  ArrowUp: { code: 'ArrowUp', keyCode: 38 },
  ArrowDown: { code: 'ArrowDown', keyCode: 40 },
  ArrowLeft: { code: 'ArrowLeft', keyCode: 37 },
  ArrowRight: { code: 'ArrowRight', keyCode: 39 },
  Home: { code: 'Home', keyCode: 36 },
  End: { code: 'End', keyCode: 35 },
  PageUp: { code: 'PageUp', keyCode: 33 },
  PageDown: { code: 'PageDown', keyCode: 34 },
}
export const KEY_NAMES = Object.keys(KEYS)

const MODIFIERS: Record<string, number> = { Alt: 1, Control: 2, Meta: 4, Shift: 8 }

/**
 * Lists what a person sees and can use on the page, one line each: headings, text, and
 * controls numbered so an action can name one. The numbers hold until the next snapshot.
 */
const SNAPSHOT = `(() => {
  const MAX = ${MAX_SNAPSHOT_LINES}
  for (const el of document.querySelectorAll('[data-ariadne-ref]')) el.removeAttribute('data-ariadne-ref')
  const lines = []
  let n = 0
  const clean = (text, max) => (text || '').replace(/\\s+/g, ' ').trim().slice(0, max)
  const CONTROL = 'a[href],button,input:not([type=hidden]),select,textarea,summary,[role=button],[role=link],[role=checkbox],[role=radio],[role=tab],[role=menuitem],[role=menuitemcheckbox],[role=option],[role=switch],[role=combobox],[role=textbox],[role=slider],[contenteditable=""],[contenteditable=true],[onclick],[tabindex]:not([tabindex="-1"])'
  const roleOf = (el) => {
    const given = el.getAttribute('role')
    if (given) return given
    if (el.tagName === 'A') return 'link'
    if (el.tagName === 'BUTTON' || el.tagName === 'SUMMARY') return 'button'
    if (el.tagName === 'SELECT') return 'select'
    if (el.tagName === 'TEXTAREA' || el.isContentEditable) return 'textbox'
    if (el.tagName === 'INPUT') {
      const type = el.type
      if (type === 'checkbox' || type === 'radio') return type
      if (type === 'submit' || type === 'button' || type === 'reset') return 'button'
      return type === 'text' ? 'textbox' : type
    }
    return 'clickable'
  }
  const nameOf = (el) =>
    clean(
      el.getAttribute('aria-label') ||
        (el.labels && el.labels[0] && el.labels[0].innerText) ||
        el.getAttribute('placeholder') ||
        el.getAttribute('title') ||
        el.getAttribute('alt') ||
        (['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName) ? '' : el.innerText),
      80,
    )
  const stateOf = (el) => {
    const parts = []
    if (el.tagName === 'SELECT') {
      const chosen = el.selectedOptions[0]
      parts.push('value=' + JSON.stringify(chosen ? chosen.text : ''))
      parts.push('options=' + JSON.stringify([...el.options].slice(0, 20).map((o) => clean(o.text, 40))))
    } else if ((el.tagName === 'INPUT' && !['checkbox', 'radio', 'submit', 'button'].includes(el.type)) || el.tagName === 'TEXTAREA') {
      parts.push('value=' + JSON.stringify(clean(el.value, 80)))
    }
    if (el.checked || el.getAttribute('aria-checked') === 'true') parts.push('checked')
    if (el.getAttribute('aria-selected') === 'true') parts.push('selected')
    if (el.getAttribute('aria-expanded') === 'true') parts.push('expanded')
    if (el.disabled || el.getAttribute('aria-disabled') === 'true') parts.push('disabled')
    if (el === document.activeElement) parts.push('focused')
    return parts.length ? ' ' + parts.join(' ') : ''
  }
  const walk = (node) => {
    for (const child of node.childNodes) {
      if (lines.length >= MAX) return
      if (child.nodeType === 3) {
        const text = clean(child.textContent, 200)
        if (text.length > 1 && lines[lines.length - 1] !== 'text ' + JSON.stringify(text)) {
          lines.push('text ' + JSON.stringify(text))
        }
        continue
      }
      if (child.nodeType !== 1) continue
      const el = child
      if (['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'svg', 'SVG', 'IFRAME'].includes(el.tagName)) continue
      const style = getComputedStyle(el)
      if (style.display === 'none' || style.visibility === 'hidden' || el.getAttribute('aria-hidden') === 'true') continue
      if (el.matches(CONTROL)) {
        const box = el.getBoundingClientRect()
        if (box.width === 0 && box.height === 0) continue
        el.setAttribute('data-ariadne-ref', String(++n))
        lines.push('[' + n + '] ' + roleOf(el) + ' ' + JSON.stringify(nameOf(el)) + stateOf(el))
        continue
      }
      if (/^H[1-6]$/.test(el.tagName)) {
        lines.push('heading ' + JSON.stringify(clean(el.innerText, 120)))
        continue
      }
      if (el.tagName === 'IMG') {
        const alt = clean(el.getAttribute('alt'), 80)
        if (alt) lines.push('image ' + JSON.stringify(alt))
        continue
      }
      walk(el.shadowRoot || el)
    }
  }
  if (document.body) walk(document.body)
  const head = [
    'url: ' + location.href,
    'title: ' + JSON.stringify(document.title),
    'scroll: ' + Math.round(scrollY) + ' of ' + Math.max(0, document.documentElement.scrollHeight - innerHeight) + ' px',
  ]
  if (lines.length >= MAX) lines.push('… more below, cut here')
  return head.concat(lines).join('\\n')
})()`

/** The element a snapshot numbered `ref`, scrolled into view: its center, or why not. */
const elementAt = (ref: number) => `(() => {
  const el = document.querySelector('[data-ariadne-ref="${ref}"]')
  if (!el) return null
  el.scrollIntoView({ block: 'center', inline: 'center' })
  const box = el.getBoundingClientRect()
  return { x: box.x + box.width / 2, y: box.y + box.height / 2, w: box.width, h: box.height, top: box.y + scrollY, left: box.x + scrollX }
})()`

export class RefGoneError extends Error {
  constructor(ref: number) {
    super(`There is no element [${ref}] on the page now; take a new snapshot.`)
  }
}

/**
 * One headless browser with one page, kept between a conversation's tool calls: what the
 * page shows, what it logged and which of its requests failed carry over from call to call.
 */
export class BrowserPage {
  private console: PageLine[] = []
  private failed: string[] = []
  private consoleRead = 0
  private failedRead = 0
  private dropped = { console: 0, failed: 0 }
  private loading = false
  private loaded: (() => void) | null = null
  private dialogs: string[] = []
  private readonly requests = new Map<string, string>()

  private constructor(
    private readonly child: Bun.Subprocess<'ignore', 'ignore', 'pipe'>,
    private readonly cdp: Cdp,
    private readonly session: string,
    private readonly profile: string,
    private readonly proxy: LoopbackProxy | null,
  ) {
    cdp.on((method, params, from) => {
      if (from !== this.session) return
      switch (method) {
        case 'Page.frameStartedLoading':
          if (!params.frameId || params.frameId === this.mainFrame) this.loading = true
          break
        case 'Page.loadEventFired':
          this.loading = false
          this.loaded?.()
          break
        case 'Page.javascriptDialogOpening':
          // Nobody can answer it: it is accepted, and said so in the next result.
          this.dialogs.push(`${params.type} "${String(params.message).slice(0, 200)}"`)
          void this.send('Page.handleJavaScriptDialog', { accept: true }).catch(() => {})
          break
        case 'Runtime.consoleAPICalled':
          this.log(params.type === 'warning' ? 'warn' : params.type, consoleText(params.args ?? []))
          break
        case 'Runtime.exceptionThrown': {
          const details = params.exceptionDetails
          this.log('error', details.exception?.description ?? details.text ?? 'uncaught error')
          break
        }
        case 'Log.entryAdded':
          if (params.entry?.level === 'error' || params.entry?.level === 'warning') {
            const { entry } = params
            this.log(
              entry.level === 'warning' ? 'warn' : 'error',
              `${entry.text}${entry.url ? ` (${entry.url})` : ''}`,
            )
          }
          break
        case 'Network.requestWillBeSent':
          this.requests.set(params.requestId, `${params.request.method} ${params.request.url}`)
          if (this.requests.size > 2000) this.requests.clear()
          break
        case 'Network.responseReceived':
          if (params.response.status >= 400) {
            this.fail(
              `${this.requests.get(params.requestId) ?? params.response.url} → ${params.response.status}`,
            )
          }
          break
        case 'Network.loadingFailed':
          if (!params.canceled) {
            this.fail(
              `${this.requests.get(params.requestId) ?? params.requestId} → ${params.errorText}`,
            )
          }
          break
      }
    })
  }

  private mainFrame: string | null = null

  static async open(
    executable: string,
    options: { signal?: AbortSignal; tunnel?: Tunnel } = {},
  ): Promise<BrowserPage> {
    const { signal } = options
    // A remote workspace's loopback is reached through it: localhost goes to the proxy too.
    const proxy = options.tunnel ? LoopbackProxy.start(options.tunnel) : null
    const profile = await mkdtemp(join(tmpdir(), 'ariadne-browser-'))
    const child = Bun.spawn(
      [
        executable,
        '--headless=new',
        '--remote-debugging-port=0',
        `--user-data-dir=${profile}`,
        '--no-first-run',
        '--no-default-browser-check',
        '--disable-gpu',
        '--disable-extensions',
        '--disable-sync',
        '--hide-scrollbars',
        '--mute-audio',
        ...(proxy
          ? [`--proxy-server=socks5://127.0.0.1:${proxy.port}`, '--proxy-bypass-list=<-loopback>']
          : []),
        // A root user (a container) cannot start the sandbox.
        ...(process.getuid?.() === 0 ? ['--no-sandbox'] : []),
        'about:blank',
      ],
      { stdout: 'ignore', stderr: 'pipe', stdin: 'ignore', windowsHide: true },
    )
    try {
      const url = await within(
        devtoolsUrl(child.stderr),
        START_TIMEOUT_MS,
        'the browser did not start',
        signal,
      )
      const cdp = await Cdp.connect(url)
      const { targetId } = await cdp.send<{ targetId: string }>('Target.createTarget', {
        url: 'about:blank',
      })
      const { sessionId } = await cdp.send<{ sessionId: string }>('Target.attachToTarget', {
        targetId,
        flatten: true,
      })
      const page = new BrowserPage(child, cdp, sessionId, profile, proxy)
      await Promise.all(
        ['Page.enable', 'Runtime.enable', 'Log.enable', 'Network.enable'].map((method) =>
          page.send(method),
        ),
      )
      page.mainFrame = (await page.send('Page.getFrameTree')).frameTree.frame.id
      await page.viewport(1280, 800, null)
      return page
    } catch (error) {
      child.kill()
      proxy?.stop()
      await rm(profile, { recursive: true, force: true }).catch(() => {})
      throw error
    }
  }

  get closed(): boolean {
    return this.cdp.closed
  }

  private send(method: string, params: object = {}): Promise<any> {
    return this.cdp.send(method, params, this.session)
  }

  private log(level: string, text: string) {
    this.console.push({ level, text: text.slice(0, 1000) })
    if (this.console.length > MAX_LINES) {
      this.console.shift()
      this.dropped.console++
    }
  }

  private fail(line: string) {
    this.failed.push(line.slice(0, 500))
    if (this.failed.length > MAX_LINES) {
      this.failed.shift()
      this.dropped.failed++
    }
  }

  async viewport(width: number, height: number, colorScheme: 'light' | 'dark' | null) {
    await this.send('Emulation.setDeviceMetricsOverride', {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: false,
    })
    await this.send('Emulation.setEmulatedMedia', {
      features: [{ name: 'prefers-color-scheme', value: colorScheme ?? '' }],
    })
  }

  /** Go to `url` and wait for it to load. */
  async goto(url: string, signal?: AbortSignal): Promise<void> {
    const load = this.nextLoad()
    const navigated = await this.send('Page.navigate', { url })
    if (navigated.errorText) throw new Error(`could not open the page: ${navigated.errorText}`)
    // A link to a part of the same page loads nothing.
    if (navigated.loaderId) {
      await within(load, LOAD_TIMEOUT_MS, 'the page did not finish loading in 30 s', signal)
    }
  }

  async history(step: 'back' | 'reload', signal?: AbortSignal): Promise<void> {
    if (step === 'reload') {
      const load = this.nextLoad()
      await this.send('Page.reload')
      await within(load, LOAD_TIMEOUT_MS, 'the page did not finish loading in 30 s', signal)
      return
    }
    await this.send('Runtime.evaluate', { expression: 'history.back()' })
    await this.settle(signal)
  }

  private nextLoad(): Promise<void> {
    const { promise, resolve } = Promise.withResolvers<void>()
    this.loaded = () => {
      this.loaded = null
      resolve()
    }
    return promise
  }

  /** After an action: if it started loading a page, wait (a while) for that page. */
  private async settle(signal?: AbortSignal): Promise<void> {
    this.loading = false
    const load = this.nextLoad()
    await Bun.sleep(250)
    if (!this.loading) return
    await within(load, SETTLE_TIMEOUT_MS, 'the page is still loading', signal).catch(() => {})
  }

  async snapshot(): Promise<string> {
    const value = await this.evaluateValue(SNAPSHOT)
    const text = typeof value === 'string' ? value : ''
    return text.length > MAX_SNAPSHOT_CHARS
      ? `${text.slice(0, MAX_SNAPSHOT_CHARS)}\n… more below, cut here`
      : text
  }

  private async evaluateValue(expression: string): Promise<unknown> {
    const result = await this.send('Runtime.evaluate', {
      expression,
      returnByValue: true,
      awaitPromise: true,
    })
    if (result.exceptionDetails) {
      throw new Error(
        result.exceptionDetails.exception?.description ?? result.exceptionDetails.text,
      )
    }
    return result.result?.value
  }

  private async locate(
    ref: number,
  ): Promise<{ x: number; y: number; w: number; h: number; top: number; left: number }> {
    const box = (await this.evaluateValue(elementAt(ref))) as {
      x: number
      y: number
      w: number
      h: number
      top: number
      left: number
    } | null
    if (!box) throw new RefGoneError(ref)
    return box
  }

  private async mouse(type: string, x: number, y: number, extra: object = {}) {
    await this.send('Input.dispatchMouseEvent', { type, x, y, ...extra })
  }

  async click(ref: number, signal?: AbortSignal): Promise<void> {
    const { x, y } = await this.locate(ref)
    await this.mouse('mouseMoved', x, y)
    await this.mouse('mousePressed', x, y, { button: 'left', clickCount: 1 })
    await this.mouse('mouseReleased', x, y, { button: 'left', clickCount: 1 })
    await this.settle(signal)
  }

  async hover(ref: number): Promise<void> {
    const { x, y } = await this.locate(ref)
    await this.mouse('mouseMoved', x, y)
    await Bun.sleep(150)
  }

  /** Types into a field, in place of what it held unless `append`. */
  async type(ref: number, text: string, append: boolean, signal?: AbortSignal): Promise<void> {
    await this.locate(ref)
    const focused = await this.evaluateValue(`(() => {
      const el = document.querySelector('[data-ariadne-ref="${ref}"]')
      el.focus()
      if (${append ? 'true' : 'false'}) return document.activeElement === el
      if (typeof el.select === 'function') el.select()
      else if (el.isContentEditable) document.execCommand('selectAll')
      return document.activeElement === el || el.contains(document.activeElement)
    })()`)
    if (!focused) throw new Error(`[${ref}] cannot take text`)
    if (text) await this.send('Input.insertText', { text })
    else if (!append) await this.press('Delete', [], signal)
  }

  async select(ref: number, option: string): Promise<string> {
    await this.locate(ref)
    const chosen = await this.evaluateValue(`(() => {
      const el = document.querySelector('[data-ariadne-ref="${ref}"]')
      if (el.tagName !== 'SELECT') return null
      const want = ${JSON.stringify(option)}
      const match = [...el.options].find((o) => o.value === want || o.text.trim() === want)
      if (!match) return null
      el.value = match.value
      el.dispatchEvent(new Event('input', { bubbles: true }))
      el.dispatchEvent(new Event('change', { bubbles: true }))
      return match.text.trim()
    })()`)
    if (typeof chosen !== 'string') {
      throw new Error(`[${ref}] is not a select with an option "${option}"`)
    }
    return chosen
  }

  async press(key: string, modifiers: string[], signal?: AbortSignal): Promise<void> {
    const known = KEYS[key]
    const single = [...key].length === 1
    if (!known && !single) throw new Error(`unknown key "${key}"`)
    const flags = modifiers.reduce((sum, name) => sum | (MODIFIERS[name] ?? 0), 0)
    const event = known
      ? {
          key: key === 'Space' ? ' ' : key,
          code: known.code,
          windowsVirtualKeyCode: known.keyCode,
          ...(known.text && !flags ? { text: known.text } : {}),
        }
      : {
          key,
          code: /^[a-z]$/i.test(key) ? `Key${key.toUpperCase()}` : '',
          windowsVirtualKeyCode: key.toUpperCase().charCodeAt(0),
          ...(flags & ~MODIFIERS.Shift! ? {} : { text: key }),
        }
    await this.send('Input.dispatchKeyEvent', {
      type: 'text' in event ? 'keyDown' : 'rawKeyDown',
      modifiers: flags,
      ...event,
    })
    await this.send('Input.dispatchKeyEvent', { type: 'keyUp', modifiers: flags, ...event })
    await this.settle(signal)
  }

  /** Scrolls the page, or brings an element into view. */
  async scroll(by: number | null, ref: number | null): Promise<void> {
    if (ref !== null) {
      await this.locate(ref)
      return
    }
    await this.send('Runtime.evaluate', { expression: `scrollBy(0, ${by ?? 600})` })
    await Bun.sleep(100)
  }

  /** Waits until the page shows `text`, or for `ms`. `false` when the text never came. */
  async wait(text: string | null, ms: number, signal?: AbortSignal): Promise<boolean> {
    if (!text) {
      await within(Bun.sleep(ms), ms + 1000, 'waiting failed', signal)
      return true
    }
    const until = Date.now() + ms
    while (Date.now() < until) {
      if (signal?.aborted) throw new Error('stopped')
      const found = await this.evaluateValue(
        `!!document.body && document.body.innerText.includes(${JSON.stringify(text)})`,
      )
      if (found) return true
      await Bun.sleep(200)
    }
    return false
  }

  /** Runs a script in the page; what it returns (awaited), as JSON. */
  async evaluate(script: string): Promise<string> {
    const value = await this.evaluateValue(script)
    const text = value === undefined ? 'undefined' : JSON.stringify(value)
    return text.length > 10_000 ? `${text.slice(0, 10_000)}… (cut)` : text
  }

  async screenshot(options: { fullPage: boolean; ref: number | null }): Promise<PageShot> {
    let clip: { x: number; y: number; width: number; height: number; scale: 1 } | undefined
    if (options.ref !== null) {
      const box = await this.locate(options.ref)
      clip = {
        x: box.left,
        y: box.top,
        width: Math.max(1, box.w),
        height: Math.min(Math.max(1, box.h), MAX_PAGE_HEIGHT),
        scale: 1,
      }
    } else if (options.fullPage) {
      const metrics = await this.send('Page.getLayoutMetrics')
      const size = metrics.cssContentSize ?? metrics.contentSize
      const view = metrics.cssLayoutViewport ?? metrics.layoutViewport
      clip = {
        x: 0,
        y: 0,
        width: Math.max(view.clientWidth, Math.ceil(size.width)),
        height: Math.min(Math.max(view.clientHeight, Math.ceil(size.height)), MAX_PAGE_HEIGHT),
        scale: 1,
      }
    }
    const shot = async (format: 'png' | 'jpeg') =>
      Buffer.from(
        (
          await this.send('Page.captureScreenshot', {
            format,
            ...(format === 'jpeg' ? { quality: 75 } : {}),
            ...(clip ? { clip, captureBeyondViewport: true } : {}),
          })
        ).data,
        'base64',
      )
    let data: Uint8Array = await shot('png')
    let mime: PageShot['mime'] = 'image/png'
    if (data.byteLength > MAX_IMAGE_BYTES) {
      data = await shot('jpeg')
      mime = 'image/jpeg'
    }
    if (clip) return { data, mime, width: Math.round(clip.width), height: Math.round(clip.height) }
    const view = (await this.send('Page.getLayoutMetrics')).cssLayoutViewport
    return { data, mime, width: view?.clientWidth ?? 0, height: view?.clientHeight ?? 0 }
  }

  /** Where the page is now. */
  async where(): Promise<{ url: string; title: string }> {
    const value = await this.evaluateValue('[location.href, document.title]').catch(() => null)
    const [url, title] = Array.isArray(value) ? (value as [string, string]) : ['', '']
    return { url, title }
  }

  /** Console lines since the last read, or all that are kept. */
  readConsole(all: boolean): { lines: PageLine[]; dropped: number } {
    const lines = all ? this.console : this.console.slice(this.consoleRead - this.dropped.console)
    this.consoleRead = this.console.length + this.dropped.console
    return { lines, dropped: all ? this.dropped.console : 0 }
  }

  readFailed(all: boolean): string[] {
    const lines = all ? this.failed : this.failed.slice(this.failedRead - this.dropped.failed)
    this.failedRead = this.failed.length + this.dropped.failed
    return lines
  }

  /** What happened since the last call that the model should hear: errors and dialogs. */
  news(): { errors: number; failed: number; dialogs: string[] } {
    const fresh = this.console.slice(Math.max(0, this.consoleRead - this.dropped.console))
    const dialogs = this.dialogs
    this.dialogs = []
    return {
      errors: fresh.filter((line) => line.level === 'error').length,
      failed: this.failed.length + this.dropped.failed - this.failedRead,
      dialogs,
    }
  }

  async close(): Promise<void> {
    this.cdp.close()
    this.child.kill()
    await this.child.exited.catch(() => {})
    this.proxy?.stop()
    await rm(this.profile, { recursive: true, force: true }).catch(() => {})
  }
}

/**
 * The browsers of conversations, one each, started on first use and closed when it goes
 * idle, when the conversation is done with it, or when the server stops.
 */
export class BrowserPages {
  private readonly pages = new Map<
    string,
    { page: Promise<BrowserPage>; timer: ReturnType<typeof setTimeout> }
  >()

  constructor(
    readonly locator: BrowserLocator,
    private readonly idleMs = IDLE_MS,
  ) {}

  /** The conversation's page, opened now if it has none (or its browser went away). */
  async page(
    conversationId: string,
    options: { signal?: AbortSignal; tunnel?: Tunnel } = {},
  ): Promise<BrowserPage> {
    const { signal } = options
    const held = this.pages.get(conversationId)
    if (held) {
      const page = await held.page.catch(() => null)
      if (page && !page.closed) {
        this.touch(conversationId)
        return page
      }
      await this.close(conversationId)
    }
    const opening = (async () => {
      const executable =
        this.locator.installed() ??
        (await this.locator.download(signal ?? new AbortController().signal))
      return BrowserPage.open(executable, options)
    })()
    this.pages.set(conversationId, { page: opening, timer: this.idle(conversationId) })
    try {
      return await opening
    } catch (error) {
      this.pages.delete(conversationId)
      throw error
    }
  }

  has(conversationId: string): boolean {
    return this.pages.has(conversationId)
  }

  private idle(conversationId: string): ReturnType<typeof setTimeout> {
    const timer = setTimeout(() => void this.close(conversationId), this.idleMs)
    timer.unref?.()
    return timer
  }

  private touch(conversationId: string) {
    const held = this.pages.get(conversationId)
    if (!held) return
    clearTimeout(held.timer)
    held.timer = this.idle(conversationId)
  }

  async close(conversationId: string): Promise<boolean> {
    const held = this.pages.get(conversationId)
    if (!held) return false
    this.pages.delete(conversationId)
    clearTimeout(held.timer)
    const page = await held.page.catch(() => null)
    await page?.close()
    return true
  }

  /** Closes a thread's browser once it reports or is stopped: its work is over for now. */
  follow(app: Pick<Ariadne, 'subscribe'>): () => void {
    return app.subscribe((events) => {
      for (const event of events) {
        if (event.type === 'thread.reported' || event.type === 'thread.stopped') {
          void this.close(event.data.threadId)
        }
      }
    })
  }

  async closeAll(): Promise<void> {
    await Promise.all([...this.pages.keys()].map((id) => this.close(id)))
  }
}
