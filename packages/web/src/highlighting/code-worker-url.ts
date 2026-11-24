/** Resolve the `@dou-ui/code` Web Worker module URL (replaced with the shared prebuilt worker chunk in production builds). */
export function codeWorkerUrl(): URL {
  return new URL('./code.worker.ts', import.meta.url)
}
