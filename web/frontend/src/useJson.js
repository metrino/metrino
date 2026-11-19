import { ref } from 'vue'

// useJson fetches url once and exposes the parsed body as `data`, or a message as `error`.
export function useJson(url) {
  const data = ref(null)
  const error = ref(null)
  fetch(url)
    .then((resp) => {
      if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`)
      return resp.json()
    })
    .then((body) => { data.value = body })
    .catch((err) => { error.value = err.message })
  return { data, error }
}
