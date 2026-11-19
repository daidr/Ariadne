/** Keep one request running and merge all work that arrives while it runs. */
export function coalescedRefresh<Key, Value>(load: (keys: ReadonlySet<Key>) => Promise<Value>) {
  let running = false
  let pending:
    | {
        keys: Set<Key>
        promise: Promise<Value>
        resolve: (value: Value) => void
        reject: (error: unknown) => void
      }
    | undefined

  async function drain() {
    while (pending) {
      const { keys, resolve, reject } = pending
      pending = undefined
      try {
        resolve(await load(keys))
      } catch (error) {
        reject(error)
      }
    }
    running = false
  }

  return (keys: Iterable<Key> = []) => {
    if (!pending) {
      let resolve!: (value: Value) => void
      let reject!: (error: unknown) => void
      const promise = new Promise<Value>((done, fail) => {
        resolve = done
        reject = fail
      })
      pending = { keys: new Set(), promise, resolve, reject }
    }
    for (const key of keys) pending.keys.add(key)
    if (!running) {
      running = true
      queueMicrotask(() => void drain())
    }
    return pending.promise
  }
}
