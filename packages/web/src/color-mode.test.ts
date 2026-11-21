// @vitest-environment happy-dom
import { afterEach, describe, expect, test } from 'vitest'
import { syncThemeColor, THEME_COLORS, themeColorFor, watchThemeColor } from './color-mode'

describe('theme-color synchronization', () => {
  afterEach(() => {
    document.head.innerHTML = ''
    document.documentElement.removeAttribute('data-dou-theme')
  })

  test('maps light and dark themes to dou-ui canvas colors', () => {
    expect(themeColorFor('light')).toBe(THEME_COLORS.light)
    expect(themeColorFor('dark')).toBe(THEME_COLORS.dark)
    expect(themeColorFor(null)).toBe(THEME_COLORS.light)
  })

  test('syncs and watches data-dou-theme mutations on <html>', async () => {
    const meta = document.createElement('meta')
    meta.name = 'theme-color'
    meta.content = THEME_COLORS.light
    document.head.append(meta)

    document.documentElement.setAttribute('data-dou-theme', 'dark')
    const stop = watchThemeColor()
    expect(meta.content).toBe(THEME_COLORS.dark)

    document.documentElement.setAttribute('data-dou-theme', 'light')
    await Promise.resolve()
    expect(meta.content).toBe(THEME_COLORS.light)

    stop()
    document.documentElement.setAttribute('data-dou-theme', 'dark')
    await Promise.resolve()
    expect(meta.content).toBe(THEME_COLORS.light)
    expect(syncThemeColor()).toBe(THEME_COLORS.dark)
    expect(meta.content).toBe(THEME_COLORS.dark)
  })
})
