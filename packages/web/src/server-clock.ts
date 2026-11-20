import { computed, onScopeDispose, shallowRef } from 'vue'

const origin = shallowRef(Date.now() - performance.now())
const tick = shallowRef(0)
const now = computed(() => {
  void tick.value
  return Math.floor(origin.value + performance.now())
})
let clock: ReturnType<typeof setInterval>
let readers = 0

export function synchronizeServerClock(value: string | null) {
  const time = Number(value)
  if (!Number.isFinite(time) || time <= 0) return
  const sample = time - performance.now()
  // Short requests can arrive a few milliseconds apart. Keep the shared ticker stable.
  if (Math.abs(sample - origin.value) >= 1_000) origin.value = sample
}

export function useServerNow() {
  if (++readers === 1) {
    tick.value++
    clock = setInterval(() => tick.value++, 10_000)
  }
  onScopeDispose(() => {
    if (--readers === 0) clearInterval(clock)
  })
  return now
}
