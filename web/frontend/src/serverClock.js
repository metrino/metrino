import { onBeforeUnmount, ref, watch } from 'vue'

// useServerClock returns the server's time now, in milliseconds, ticking every second, or null until serverNow
// (a ref of the server's "now", in RFC 3339) has a value. It keeps the gap between the server's clock and the
// browser's that it saw when serverNow arrived, so a wrong clock in the browser can't make a sync look stale.
export function useServerClock(serverNow) {
  const now = ref(null)
  let offset = 0
  const tick = () => {
    now.value = Date.now() + offset
  }
  watch(
    serverNow,
    (value) => {
      if (!value) return
      offset = Date.parse(value) - Date.now()
      tick()
    },
    { immediate: true },
  )
  const timer = setInterval(() => {
    if (now.value !== null) tick()
  }, 1000)
  onBeforeUnmount(() => clearInterval(timer))
  return now
}
