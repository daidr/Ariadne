import { ensureLocale } from './i18n'

// Preload both locale dictionaries in Vitest so component tests that call `t()` or
// flip `locale.value` synchronously without bootstrapping `main.ts` work immediately.
await Promise.all([ensureLocale('zh-CN'), ensureLocale('en')])
