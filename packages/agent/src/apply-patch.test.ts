import { describe, expect, test } from 'vitest'
import { applyHunks, editsByPatch, parsePatch, PatchError } from './apply-patch'

const update = (patch: string) => {
  const op = parsePatch(patch)[0]!
  if (op.kind !== 'update') throw new Error('not an update')
  return op.hunks
}

describe('apply_patch', () => {
  test('reads adds, deletes, moves and hunks, as models write them', () => {
    const ops = parsePatch(`apply_patch <<'EOF'
*** Begin Patch
*** Add File: src/new.ts
+export const a = 1

+export const b = 2
*** Delete File: old.ts
*** Update File: src/x.ts
*** Move to: src/y.ts
@@ function f() {
 const keep = 1
-const gone = 2
+const put = 3

*** End Patch
EOF`)
    expect(ops).toEqual([
      { kind: 'add', path: 'src/new.ts', text: 'export const a = 1\n\nexport const b = 2\n' },
      { kind: 'delete', path: 'old.ts' },
      {
        kind: 'update',
        path: 'src/x.ts',
        moveTo: 'src/y.ts',
        hunks: [
          {
            anchor: 'function f() {',
            atEnd: false,
            lines: [
              { op: ' ', text: 'const keep = 1' },
              { op: '-', text: 'const gone = 2' },
              { op: '+', text: 'const put = 3' },
            ],
          },
        ],
      },
    ])
    expect(() => parsePatch('*** Begin Patch\nhello\n*** End Patch')).toThrow(PatchError)
    expect(() => parsePatch('*** Begin Patch\n*** End Patch')).toThrow('changes no file')
  })

  test('hunks find their lines after the @@ line, loosely, and keep the line endings', () => {
    const file = 'function a() {\n  return 1\n}\nfunction b() {\n  return 1\n}\n'
    const hunks = update('*** Update File: f.ts\n@@ function b() {\n-    return 1\n+  return 2\n')
    expect(applyHunks(file, hunks, 'f.ts')).toBe(
      'function a() {\n  return 1\n}\nfunction b() {\n  return 2\n}\n',
    )
    const crlf = applyHunks('a\r\nb\r\n', update('*** Update File: f\n a\n+x\n b\n'), 'f')
    expect(crlf).toBe('a\r\nx\r\nb\r\n')
    // Typographic quotes for plain ones still match.
    expect(
      applyHunks("say('hi')\n", update('*** Update File: f\n-say(‘hi’)\n+say(1)\n'), 'f'),
    ).toBe('say(1)\n')
  })

  test('only additions go after the @@ line or at the end; a missing line says what it looked for', () => {
    expect(applyHunks('a\nb\n', update('*** Update File: f\n+c\n'), 'f')).toBe('a\nb\nc\n')
    expect(applyHunks('a\nb\n', update('*** Update File: f\n@@ a\n+x\n'), 'f')).toBe('a\nx\nb\n')
    expect(() => applyHunks('a\n', update('*** Update File: f\n-zzz\n+y\n'), 'f')).toThrow(
      'these lines are not in the file:\nzzz',
    )
  })

  test('GPT and Codex models edit by patch, others do not', () => {
    for (const model of ['gpt-5.5', 'openai/gpt-5-codex', 'o3', 'o4-mini', 'codex-mini-latest']) {
      expect(editsByPatch(model)).toBe(true)
    }
    for (const model of ['claude-opus-5-5', 'deepseek-chat', 'qwen3-coder', 'glm-4.6', 'kimi-k2']) {
      expect(editsByPatch(model)).toBe(false)
    }
  })
})
