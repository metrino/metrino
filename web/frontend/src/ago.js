// How long before `now` (the server's clock, in milliseconds; see serverClock.js) `time` was, in words; "never" if
// there is no time.
export function ago(time, now) {
  if (!time) return 'never'
  const seconds = Math.max(0, Math.round((now - Date.parse(time)) / 1000))
  if (seconds < 60) return `${seconds} s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours} h ago`
  return `${Math.round(hours / 24)} days ago`
}
