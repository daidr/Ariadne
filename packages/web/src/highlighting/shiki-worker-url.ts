/** Resolve the Shiki Web Worker module URL (replaced with the shared prebuilt worker chunk in production builds). */
export function shikiWorkerUrl(): URL {
  return new URL('./shiki.worker.ts', import.meta.url)
}
