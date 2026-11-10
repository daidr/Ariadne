import { KEY_NAMES, MAX_PAGE_HEIGHT, RefGoneError, type BrowserPage } from './browser-page'
import {
  optionalBool,
  optionalInt,
  optionalStr,
  str,
  ToolInputError,
  type Tool,
  type ToolContext,
  type ToolResult,
} from './tool-helpers'

const ACTIONS = [
  'open',
  'snapshot',
  'click',
  'type',
  'press',
  'select',
  'hover',
  'scroll',
  'wait',
  'screenshot',
  'console',
  'network',
  'evaluate',
  'back',
  'reload',
  'close',
] as const
type Action = (typeof ACTIONS)[number]

const SIZE = { width: [320, 3840], height: [240, 2160] } as const

function size(value: number | null, [min, max]: readonly [number, number], fallback: number) {
  if (value === null) return fallback
  if (value < min || value > max) throw new ToolInputError(`size must be ${min} to ${max}`)
  return value
}

function ref(args: Record<string, unknown>): number {
  const value = optionalInt(args, 'ref')
  if (value === null || value < 1) {
    throw new ToolInputError('"ref" must be the number of an element in the last snapshot')
  }
  return value
}

export const browserTool = (remote: boolean): Tool => ({
  spec: {
    name: 'browser',
    description: `A headless browser of your own, to use a web page as a person would: check a page or UI you built, go through a flow, read a site. The page stays open between calls until you close it (it also closes after 10 idle minutes or when your turn's work is reported).
${
  remote
    ? 'Your workspace is on another machine: http://localhost:<port> and http://127.0.0.1:<port> reach that machine, so a server you started in the workspace opens as it is.'
    : 'A server you started in the workspace is at http://localhost:<port>.'
}
Actions:
- open {url, width?, height?, colorScheme?}: go to a page (window 1280×800 by default).
- snapshot: the page as text, controls numbered [n]. Take one before click/type/select/hover, and again after the page changed: numbers hold only until the next snapshot.
- click {ref}, hover {ref}, type {ref, text, append?} (replaces the field's text unless append), select {ref, option}, press {key, modifiers?} (keys: ${KEY_NAMES.join(', ')}, or one character; modifiers: Alt, Control, Meta, Shift), scroll {by? px | ref?}.
- wait {text?, ms?}: until the page shows the text (up to ms, default 5000), or just ms.
- screenshot {fullPage?, ref?}: a picture of the window, the whole page (up to ${MAX_PAGE_HEIGHT} px) or one element. To show the user, pass its file id to send_message's "files".
- console {all?}: what the page logged since you last asked. network {all?}: requests that failed or answered 4xx/5xx.
- evaluate {script}: run JavaScript in the page and get what it returns (as JSON). Prefer the actions above for what a person would do.
- back, reload, close.
Each result says where the page is and whether new errors came. The first use may take a minute to fetch a browser.`,
    parameters: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ACTIONS },
        url: { type: 'string' },
        width: { type: 'integer' },
        height: { type: 'integer' },
        colorScheme: { type: 'string', enum: ['light', 'dark'] },
        ref: { type: 'integer' },
        text: { type: 'string' },
        append: { type: 'boolean' },
        option: { type: 'string' },
        key: { type: 'string' },
        modifiers: { type: 'array', items: { type: 'string' } },
        by: { type: 'integer' },
        ms: { type: 'integer' },
        fullPage: { type: 'boolean' },
        all: { type: 'boolean' },
        script: { type: 'string' },
      },
      required: ['action'],
    },
  },
  async run(args, ctx) {
    const pages = ctx.browser
    if (!pages) throw new ToolInputError('No browser is available here.')
    const action = str(args, 'action') as Action
    if (!ACTIONS.includes(action)) {
      throw new ToolInputError(`"action" must be one of: ${ACTIONS.join(', ')}`)
    }
    if (action === 'close') {
      return {
        content: (await pages.close(ctx.conversationId)) ? 'Closed.' : 'No page was open.',
      }
    }
    if (action !== 'open' && !pages.has(ctx.conversationId)) {
      throw new ToolInputError('No page is open: start with action "open".')
    }
    let page: BrowserPage
    try {
      page = await pages.page(ctx.conversationId, {
        signal: ctx.signal,
        ...(ctx.tunnel ? { tunnel: ctx.tunnel } : {}),
      })
    } catch (error) {
      if (ctx.signal.aborted) throw error
      return {
        content: `Could not start a browser: ${message(error)}. If none could be fetched, ask the user to install Chrome or Edge.`,
        isError: true,
      }
    }
    try {
      const result = await act(page, action, args, ctx)
      return { ...result, content: `${result.content}${await footer(page, action)}` }
    } catch (error) {
      if (ctx.signal.aborted) throw error
      if (error instanceof ToolInputError) throw error
      const content =
        error instanceof RefGoneError ? error.message : `${action} failed: ${message(error)}`
      return { content: `${content}${await footer(page, action)}`, isError: true }
    }
  },
})

const message = (error: unknown) => (error instanceof Error ? error.message : String(error))

/** Where the page is, and what went wrong on it since the last call, after each action. */
async function footer(page: BrowserPage, action: Action): Promise<string> {
  if (page.closed) return '\nThe browser closed; "open" starts a new one.'
  const { url, title } = await page.where()
  const news = page.news()
  const lines = [action === 'snapshot' ? '' : `\nPage: ${url}${title ? ` "${title}"` : ''}`]
  for (const dialog of news.dialogs) lines.push(`A ${dialog} dialog opened and was accepted.`)
  if (news.errors && action !== 'console') {
    lines.push(`${news.errors} new console error(s): see action "console".`)
  }
  if (news.failed && action !== 'network') {
    lines.push(`${news.failed} new failed request(s): see action "network".`)
  }
  return lines.filter(Boolean).length ? lines.join('\n') : ''
}

async function act(
  page: BrowserPage,
  action: Exclude<Action, 'close'>,
  args: Record<string, unknown>,
  ctx: ToolContext,
): Promise<ToolResult> {
  const { signal } = ctx
  switch (action) {
    case 'open': {
      const url = str(args, 'url').trim()
      if (!/^https?:\/\/\S+$/i.test(url) && url !== 'about:blank') {
        throw new ToolInputError('"url" must be an http(s) address')
      }
      const scheme = optionalStr(args, 'colorScheme')
      if (scheme !== undefined && scheme !== 'light' && scheme !== 'dark') {
        throw new ToolInputError('"colorScheme" must be "light" or "dark"')
      }
      await page.viewport(
        size(optionalInt(args, 'width'), SIZE.width, 1280),
        size(optionalInt(args, 'height'), SIZE.height, 800),
        scheme ?? null,
      )
      await page.goto(url, signal)
      return { content: 'Opened.' }
    }
    case 'snapshot':
      return { content: await page.snapshot() }
    case 'click':
      await page.click(ref(args), signal)
      return { content: 'Clicked.' }
    case 'hover':
      await page.hover(ref(args))
      return { content: 'Hovering.' }
    case 'type': {
      if (typeof args.text !== 'string') throw new ToolInputError('"text" must be a string')
      await page.type(ref(args), args.text, optionalBool(args, 'append'), signal)
      return { content: 'Typed.' }
    }
    case 'select':
      return { content: `Selected "${await page.select(ref(args), str(args, 'option'))}".` }
    case 'press': {
      const modifiers = args.modifiers ?? []
      if (!Array.isArray(modifiers) || modifiers.some((m) => typeof m !== 'string')) {
        throw new ToolInputError('"modifiers" must be a list of Alt, Control, Meta, Shift')
      }
      await page.press(str(args, 'key'), modifiers as string[], signal)
      return { content: 'Pressed.' }
    }
    case 'scroll':
      await page.scroll(optionalInt(args, 'by'), optionalInt(args, 'ref'))
      return { content: 'Scrolled.' }
    case 'wait': {
      const text = optionalStr(args, 'text') ?? null
      const ms = optionalInt(args, 'ms') ?? (text ? 5000 : 1000)
      if (ms < 0 || ms > 30_000) throw new ToolInputError('"ms" must be 0 to 30000')
      const found = await page.wait(text, ms, signal)
      return found
        ? { content: text ? `"${text}" is on the page.` : 'Waited.' }
        : { content: `"${text}" did not show within ${ms} ms.`, isError: true }
    }
    case 'screenshot': {
      const at = args.ref === undefined || args.ref === null ? null : ref(args)
      const shot = await page.screenshot({ fullPage: optionalBool(args, 'fullPage'), ref: at })
      const file = ctx.app.saveFile(ctx.projectId, { mime: shot.mime, data: shot.data })
      return {
        content: `Screenshot, ${shot.width}×${shot.height}, attached as ${file.id}.`,
        files: [{ fileId: file.id, name: 'screenshot', mime: file.mime, size: file.size }],
      }
    }
    case 'console': {
      const { lines, dropped } = page.readConsole(optionalBool(args, 'all'))
      if (!lines.length) return { content: 'Nothing logged.' }
      return {
        content: [
          ...(dropped ? [`(${dropped} older lines dropped)`] : []),
          ...lines.map((line) => `[${line.level}] ${line.text}`),
        ].join('\n'),
      }
    }
    case 'network': {
      const lines = page.readFailed(optionalBool(args, 'all'))
      return { content: lines.length ? lines.join('\n') : 'No request failed.' }
    }
    case 'evaluate':
      return { content: await page.evaluate(str(args, 'script')) }
    case 'back':
    case 'reload':
      await page.history(action, signal)
      return { content: action === 'back' ? 'Went back.' : 'Reloaded.' }
  }
}
