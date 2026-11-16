/** A small LRU cache whose expired values are released even while nobody reads it. */
export class ExpiringCache<K, V> {
  private readonly entries = new Map<K, { value: V; until: number }>()
  private timer: ReturnType<typeof setTimeout> | undefined
  private timerAt = Infinity

  constructor(
    readonly limit: number,
    readonly ttlMs: number,
    private readonly removed?: (key: K, value: V) => void,
  ) {
    if (!Number.isInteger(limit) || limit < 1 || !Number.isFinite(ttlMs) || ttlMs <= 0)
      throw new Error('A cache needs a positive capacity and lifetime')
  }

  get size(): number {
    return this.entries.size
  }

  peek(key: K): V | undefined {
    const entry = this.entries.get(key)
    if (!entry) return undefined
    if (entry.until <= Date.now()) {
      this.delete(key)
      return undefined
    }
    return entry.value
  }

  get(key: K): V | undefined {
    const value = this.peek(key)
    const entry = this.entries.get(key)
    if (entry) {
      this.entries.delete(key)
      this.entries.set(key, entry)
    }
    return value
  }

  set(key: K, value: V): void {
    this.delete(key)
    const until = Date.now() + this.ttlMs
    this.entries.set(key, { value, until })
    while (this.entries.size > this.limit) this.delete(this.entries.keys().next().value!)
    this.schedule(until)
  }

  delete(key: K): boolean {
    const entry = this.entries.get(key)
    if (!entry) return false
    this.entries.delete(key)
    if (!this.entries.size) {
      clearTimeout(this.timer)
      this.timer = undefined
      this.timerAt = Infinity
    }
    this.removed?.(key, entry.value)
    return true
  }

  clear(): void {
    for (const key of this.entries.keys()) this.delete(key)
  }

  keys(): IterableIterator<K> {
    return this.entries.keys()
  }

  private schedule(until: number) {
    if (this.timer && until >= this.timerAt) return
    clearTimeout(this.timer)
    this.timerAt = until
    this.timer = setTimeout(
      () => {
        this.timer = undefined
        this.timerAt = Infinity
        const now = Date.now()
        let next = Infinity
        for (const [key, entry] of this.entries) {
          if (entry.until <= now) this.delete(key)
          else next = Math.min(next, entry.until)
        }
        if (next !== Infinity) this.schedule(next)
      },
      Math.min(2_147_483_647, Math.max(0, until - Date.now())),
    )
    this.timer.unref?.()
  }
}
