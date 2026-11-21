export const COLOR_MODE_KEY = 'ariadne.color-mode'

/** Matches dou-ui canvas background tokens so the PWA title bar blends with the active theme. */
export const THEME_COLORS = {
  light: '#fafafa',
  dark: '#0a0a0a',
} as const

export function themeColorFor(theme: string | null | undefined): string {
  return theme === 'dark' ? THEME_COLORS.dark : THEME_COLORS.light
}

/** Sync `<meta name="theme-color">` with `<html data-dou-theme>`. */
export function syncThemeColor(root: HTMLElement = document.documentElement): string {
  const color = themeColorFor(root.getAttribute('data-dou-theme'))
  const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (meta && meta.content !== color) {
    meta.content = color
  }
  return color
}

/** Keep `<meta name="theme-color">` in sync whenever `data-dou-theme` changes. */
export function watchThemeColor(root: HTMLElement = document.documentElement): () => void {
  syncThemeColor(root)
  if (typeof MutationObserver === 'undefined') return () => {}
  const observer = new MutationObserver(() => {
    syncThemeColor(root)
  })
  observer.observe(root, { attributes: true, attributeFilter: ['data-dou-theme'] })
  return () => observer.disconnect()
}

/** Inline snippet injected into `<head>` so standalone PWA windows paint the right bar colour immediately. */
export function themeColorScript(): string {
  return `(()=>{var m=document.querySelector('meta[name="theme-color"]');if(m)m.content=document.documentElement.getAttribute('data-dou-theme')==='dark'?'${THEME_COLORS.dark}':'${THEME_COLORS.light}';})();`
}
