// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { DouChatMessage, DouConfigProvider, type ComponentDefaults } from 'dou-ui'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { describe, expect, test, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { sanitizeUrl } from '../markdown/links'
import { createCodeBlockHighlighter, plainCodeLinesHtml } from './code-block'
import { createShikiService } from './shiki'

async function flushHighlights() {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('DouCodeBlock Shiki highlighter', () => {
  test('returns escaped plain lines synchronously for plain or unsupported languages without invoking Shiki', () => {
    const loader = vi.fn(async () => '<pre><code><span class="line">unused</span></code></pre>')
    const highlight = createCodeBlockHighlighter(loader)

    expect(highlight('', 'ts')).toBe('')

    const unsafe = '<script>alert("x")</script>\na & b > c\n\nlast'
    for (const lang of ['', 'text', 'plaintext', 'mermaid', 'not-a-language']) {
      const result = highlight(unsafe, lang)
      expect(typeof result, `expected synchronous plain lines for ${lang || '(empty)'}`).toBe(
        'string',
      )
      expect(result).toBe(
        [
          '<span class="line">&lt;script&gt;alert("x")&lt;/script&gt;</span>',
          '<span class="line">a &amp; b &gt; c</span>',
          '<span class="line"></span>',
          '<span class="line">last</span>',
        ].join('\n'),
      )
    }

    expect(loader).not.toHaveBeenCalled()
  })

  test('falls back to escaped plain lines if the underlying highlighter returns an empty string', async () => {
    const highlight = createCodeBlockHighlighter(async () => '')
    const result = await highlight('const a = 1 < 2', 'ts')
    expect(result).toBe(plainCodeLinesHtml('const a = 1 < 2'))
  })

  test('highlights multi-language code blocks inside assistant DouChatMessage via DouConfigProvider defaults', async () => {
    const shiki = createShikiService(() => createJavaScriptRegexEngine())
    const defaults: ComponentDefaults = {
      DouCodeBlock: {
        highlight: createCodeBlockHighlighter(shiki.highlightToHtml),
      },
    }

    const content = [
      'Here are examples in several languages:',
      '',
      '```ts',
      'export const answer: number = 42',
      '```',
      '',
      '```go',
      'package main',
      'func main() { println("ok") }',
      '```',
      '',
      '```bash',
      'echo "hello world"',
      '```',
      '',
      '```',
      'plain <tag> & line 1',
      'line 2',
      '```',
      '',
      '```unknownlang',
      'fallback <script>alert(1)</script>',
      '```',
    ].join('\n')

    const Host = defineComponent({
      setup() {
        return () =>
          h(DouConfigProvider, { defaults }, () =>
            h(DouChatMessage, {
              sender: 'assistant',
              parts: [{ type: 'text', text: content }],
              markdown: { linkTarget: '_blank', sanitizeUrl },
            }),
          )
      },
    })

    const wrapper = mount(Host)
    await vi.waitFor(async () => {
      await flushHighlights()
      const tsSpans = wrapper.findAll(
        '.dou-code-block__body[data-lang="ts"] .dou-code-block__line span[style*="--shiki-light:"]',
      )
      const goSpans = wrapper.findAll(
        '.dou-code-block__body[data-lang="go"] .dou-code-block__line span[style*="--shiki-light:"]',
      )
      const bashSpans = wrapper.findAll(
        '.dou-code-block__body[data-lang="bash"] .dou-code-block__line span[style*="--shiki-light:"]',
      )
      expect(tsSpans.length).toBeGreaterThan(0)
      expect(goSpans.length).toBeGreaterThan(0)
      expect(bashSpans.length).toBeGreaterThan(0)
    })

    for (const [lang, expected] of [
      ['ts', 'export const answer: number = 42'],
      ['go', 'package mainfunc main() { println("ok") }'],
      ['bash', 'echo "hello world"'],
    ]) {
      const body = wrapper.find(`.dou-code-block__body[data-lang="${lang}"]`)
      expect(body.exists(), `expected code block for ${lang}`).toBe(true)
      expect(body.text()).toBe(expected)
      expect(body.html()).toContain('--shiki-light:')
      expect(body.html()).toContain('--shiki-dark:')
    }

    const plainBody = wrapper.find('.dou-code-block__body[data-lang="text"]')
    expect(plainBody.exists()).toBe(true)
    expect(plainBody.text()).toBe('plain <tag> & line 1line 2')
    expect(plainBody.findAll('.dou-code-block__row')).toHaveLength(2)

    const unknownBody = wrapper.find('.dou-code-block__body[data-lang="unknownlang"]')
    expect(unknownBody.exists()).toBe(true)
    expect(unknownBody.text()).toBe('fallback <script>alert(1)</script>')
    expect(unknownBody.find('script').exists()).toBe(false)

    wrapper.unmount()
  })

  test('highlights streaming code blocks as lines land and when the fence closes', async () => {
    const shiki = createShikiService(() => createJavaScriptRegexEngine())
    const defaults: ComponentDefaults = {
      DouCodeBlock: {
        highlight: createCodeBlockHighlighter(shiki.highlightToHtml),
      },
    }

    const Host = defineComponent({
      props: {
        text: { type: String, required: true },
        streaming: { type: Boolean, required: true },
      },
      setup(props) {
        return () =>
          h(DouConfigProvider, { defaults }, () =>
            h(DouChatMessage, {
              sender: 'assistant',
              parts: [{ type: 'text', text: props.text, streaming: props.streaming }],
              markdown: { linkTarget: '_blank', sanitizeUrl },
            }),
          )
      },
    })

    const wrapper = mount(Host, {
      props: {
        text: '```ts\nconst a = 1\nconst b',
        streaming: true,
      },
    })

    await vi.waitFor(async () => {
      await flushHighlights()
      const body = wrapper.find('.dou-code-block__body[data-lang="ts"]')
      expect(body.exists()).toBe(true)
      expect(body.html()).toContain('--shiki-light:')
    })

    await wrapper.setProps({
      text: '```ts\nconst a = 1\nconst b = 2\n```',
      streaming: false,
    })

    await vi.waitFor(async () => {
      await flushHighlights()
      const rows = wrapper.findAll('.dou-code-block__body[data-lang="ts"] .dou-code-block__row')
      expect(rows).toHaveLength(2)
      expect(rows[0]!.html()).toContain('--shiki-light:')
      expect(rows[1]!.html()).toContain('--shiki-light:')
      expect(rows[1]!.text()).toBe('const b = 2')
    })

    wrapper.unmount()
  })
})
