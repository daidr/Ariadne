import { describe, expect, test } from 'vitest'
import { nextAlpha, notesOf, releaseNotes, withRelease, withVersion } from './release-pr'

describe('release PR', () => {
  test('the next alpha', () => {
    expect(nextAlpha('0.1.0-alpha.0')).toBe('0.1.0-alpha.1')
    expect(nextAlpha('0.1.0-alpha.9')).toBe('0.1.0-alpha.10')
    // After a stable release the alphas lead to the next minor version.
    expect(nextAlpha('0.1.0')).toBe('0.2.0-alpha.1')
    expect(() => nextAlpha('0.1.0-beta.1')).toThrow()
  })

  test('notes leave out earlier releases', () => {
    expect(
      releaseNotes(['新增导出 (#130)', '发布 0.1.0-alpha.1 (#129)', '修复登录 (#128)', '']),
    ).toBe('- 新增导出 (#130)\n- 修复登录 (#128)')
    expect(releaseNotes(['发布 0.1.0-alpha.1 (#129)'])).toBe('')
    // Commits an earlier release already listed are not listed again.
    const changelog = '# 更新日志\n\n## 0.1.0-alpha.1 (2026-11-11)\n\n- 修复登录 (#128)\n'
    expect(releaseNotes(['新增导出 (#130)', '修复登录 (#128)'], changelog)).toBe(
      '- 新增导出 (#130)',
    )
  })

  test('the changelog grows from the top and each release reads back', () => {
    const first = withRelease('', '0.1.0-alpha.1', '2026-11-11', '- a')
    expect(first).toBe('# 更新日志\n\n## 0.1.0-alpha.1 (2026-11-11)\n\n- a\n')
    const second = withRelease(first, '0.1.0-alpha.2', '2026-11-12', '- b\n- c')
    expect(second).toBe(
      '# 更新日志\n\n## 0.1.0-alpha.2 (2026-11-12)\n\n- b\n- c\n\n## 0.1.0-alpha.1 (2026-11-11)\n\n- a\n',
    )
    expect(notesOf(second, '0.1.0-alpha.2')).toBe('- b\n- c')
    expect(notesOf(second, '0.1.0-alpha.1')).toBe('- a')
    expect(notesOf(second, '0.1.0-alpha.3')).toBe('')
  })

  test('the version changes and nothing else in package.json', () => {
    const json = '{\n  "name": "x",\n  "version": "0.1.0-alpha.0",\n  "private": true\n}\n'
    expect(withVersion(json, '0.1.0-alpha.1')).toBe(json.replace('alpha.0', 'alpha.1'))
    expect(() => withVersion('{}', '1.0.0')).toThrow()
  })
})
