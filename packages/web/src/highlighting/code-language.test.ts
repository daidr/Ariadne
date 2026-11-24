// @vitest-environment happy-dom
import { lineParts, tokenizeLine, type CodeLanguage } from '@dou-ui/code'
import { shikiLanguage } from '@dou-ui/code/shiki'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import {
  builtInCodeLanguage,
  loadCodeLanguage,
  peekCodeLanguage,
  remoteShikiCodeLanguage,
  resetCodeLanguageCache,
  useCodeLanguage,
} from './code-language'
import { languageHintOf } from './languages'
import { createShikiService } from './shiki'

describe('code language resolution and highlighting', () => {
  afterEach(() => {
    resetCodeLanguageCache()
  })

  test('extracts language hints from extensions, special filenames, dotfiles and Windows paths', () => {
    expect(languageHintOf('packages/web/src/main.ts')).toBe('ts')
    expect(languageHintOf('internal\\core\\Server.GO')).toBe('go')
    expect(languageHintOf('proto/ariadne/v1/file.proto')).toBe('proto')
    expect(languageHintOf('deploy/Dockerfile')).toBe('dockerfile')
    expect(languageHintOf('deploy/Dockerfile.prod')).toBe('dockerfile')
    expect(languageHintOf('app.dockerfile')).toBe('dockerfile')
    expect(languageHintOf('Makefile')).toBe('makefile')
    expect(languageHintOf('GNUmakefile')).toBe('makefile')
    expect(languageHintOf('build/Makefile.ci')).toBe('makefile')
    expect(languageHintOf('scripts/rules.mk')).toBe('mk')
    expect(languageHintOf('.env')).toBe('ini')
    expect(languageHintOf('.env.local')).toBe('ini')
    expect(languageHintOf('.editorconfig')).toBe('ini')
    expect(languageHintOf('.bashrc')).toBe('bash')
    expect(languageHintOf('docs/guide.mdx')).toBe('markdown')

    expect(languageHintOf('README')).toBeNull()
    expect(languageHintOf('.gitignore')).toBeNull()
    expect(languageHintOf('trailing.')).toBeNull()
    expect(languageHintOf('')).toBeNull()
    expect(languageHintOf('foo.constructor')).toBe('constructor')
    expect(languageHintOf('bar.__proto__')).toBe('__proto__')
    expect(languageHintOf('baz.toString')).toBe('tostring')
  })

  test('resolves built-in @dou-ui/code languages synchronously without invoking Shiki', async () => {
    const loader = vi.fn(async () => null)
    const builtInPaths = [
      'src/app.ts',
      'src/view.tsx',
      'src/index.mjs',
      'src/App.vue',
      'src/style.css',
      'src/theme.scss',
      'index.html',
      'package.json',
      'config.yaml',
      'config.yml',
      'README.md',
      'docs/page.mdx',
      'scripts/deploy.sh',
      '.zshrc',
      'migrations/001.sql',
      'scripts/tool.py',
    ]

    for (const path of builtInPaths) {
      const builtIn = builtInCodeLanguage(path)
      expect(builtIn, `expected built-in language for ${path}`).not.toBeNull()
      expect(await loadCodeLanguage(path, loader)).toBe(builtIn)
    }

    expect(loader).not.toHaveBeenCalled()
  })

  test('skips Shiki loading for unsupported or plain files', async () => {
    const loader = vi.fn(async () => null)
    expect(builtInCodeLanguage('foo.constructor')).toBeNull()
    expect(builtInCodeLanguage('bar.__proto__')).toBeNull()
    expect(await loadCodeLanguage('notes.txt', loader)).toBeNull()
    expect(await loadCodeLanguage('LICENSE', loader)).toBeNull()
    expect(await loadCodeLanguage('go.sum', loader)).toBeNull()
    expect(await loadCodeLanguage('foo.constructor', loader)).toBeNull()
    expect(await loadCodeLanguage('bar.__proto__', loader)).toBeNull()
    expect(loader).not.toHaveBeenCalled()
  })

  test('tokenizes non-built-in languages through Shiki fallback', async () => {
    const shiki = createShikiService(() => createJavaScriptRegexEngine())
    const loader = async (key: string) => {
      const loaded = await shiki.loadLanguage(key)
      return loaded ? shikiLanguage(loaded.engine, loaded.name) : null
    }

    const samples: [string, string][] = [
      ['internal/core/server.go', 'package core\nfunc Start(port int) error { return nil }'],
      ['proto/ariadne/v1/file.proto', 'syntax = "proto3";\nmessage Ping { string id = 1; }'],
      ['Dockerfile', 'FROM golang:1.24-alpine\nRUN go build ./...'],
      ['Makefile', '# build\nbuild:\n\tgo build ./...'],
      ['bunfig.toml', '[test]\npreload = ["./setup.ts"]'],
      ['src/lib.rs', 'pub fn add(a: i32, b: i32) -> i32 { a + b }'],
      ['.env.local', 'ARIADNE_PORT=7428'],
      ['include/app.h', 'int main(void);'],
    ]

    for (const [path, code] of samples) {
      expect(builtInCodeLanguage(path), `${path} should use Shiki fallback`).toBeNull()
      const lang = await loadCodeLanguage(path, loader)
      expect(lang, `expected Shiki language for ${path}`).not.toBeNull()
      const state = lang!.startState()
      const colored = code
        .split('\n')
        .flatMap((line) => lineParts(line, tokenizeLine(lang!, line, state)))
        .filter(([, kind]) => kind !== null)
      expect(colored.length, `expected highlighted tokens for ${path}`).toBeGreaterThan(0)
    }
  })

  test('ignores stale async Shiki resolution when file path changes in useCodeLanguage', async () => {
    let resolveGo: ((lang: CodeLanguage | null) => void) | undefined
    const fakeGo = { name: 'go' } as CodeLanguage
    const loader = vi.fn(
      () =>
        new Promise<CodeLanguage | null>((resolve) => {
          resolveGo = resolve
        }),
    )

    const path = ref('internal/core/server.go')
    const scope = effectScope()
    const binding = scope.run(() => useCodeLanguage(() => path.value, loader))!

    expect(binding.language.value).toBeNull()
    expect(binding.isBuiltIn.value).toBe(false)
    expect(loader).toHaveBeenCalledTimes(1)

    path.value = 'packages/web/src/main.ts'
    await nextTick()
    expect(binding.isBuiltIn.value).toBe(true)
    expect(binding.language.value?.name).toBe('typescript')

    resolveGo?.(fakeGo)
    await Promise.resolve()
    await nextTick()
    expect(binding.language.value?.name).toBe('typescript')

    path.value = 'notes.txt'
    await nextTick()
    expect(binding.language.value).toBeNull()
    expect(peekCodeLanguage('notes.txt')).toBeNull()

    scope.stop()
  })

  test('resolves non-built-in Shiki languages to remoteCodeLanguage synchronously when Worker is available and falls back on Worker unavailability', async () => {
    expect(remoteShikiCodeLanguage('src/app.ts')).toBeNull()
    expect(remoteShikiCodeLanguage('notes.txt')).toBeNull()

    const goRemote = remoteShikiCodeLanguage('internal/core/server.go')
    expect(goRemote).not.toBeNull()
    expect(goRemote!.name).toBe('go')
    expect(goRemote!.workerMode).toBe('tokens')
    expect(remoteShikiCodeLanguage('cmd/main.go')).toBe(goRemote)

    const fakeGo = { name: 'go' } as CodeLanguage
    const loader = vi.fn(async () => fakeGo)
    const workerHealthy = ref(true)
    const path = ref('internal/core/server.go')
    const scope = effectScope()
    const binding = scope.run(() =>
      useCodeLanguage(() => path.value, { loader, remote: () => workerHealthy.value }),
    )!

    // Synchronous remote descriptor — no main-thread Shiki load required.
    expect(binding.isBuiltIn.value).toBe(false)
    expect(binding.language.value).toBe(goRemote)
    expect(loader).not.toHaveBeenCalled()

    path.value = 'packages/web/src/main.ts'
    await nextTick()
    expect(binding.isBuiltIn.value).toBe(true)
    expect(binding.language.value?.name).toBe('typescript')

    path.value = 'deploy/Dockerfile.prod'
    await nextTick()
    expect(binding.isBuiltIn.value).toBe(false)
    expect(binding.language.value?.name).toBe('dockerfile')
    expect(binding.language.value?.workerMode).toBe('tokens')

    // If the worker becomes unavailable at runtime, reactively falls back to main-thread Shiki.
    path.value = 'internal/core/server.go'
    await nextTick()
    expect(binding.language.value).toBe(goRemote)
    workerHealthy.value = false
    await vi.waitFor(() => {
      expect(binding.language.value).toBe(fakeGo)
    })
    expect(loader).toHaveBeenCalledTimes(1)

    scope.stop()
  })
})
