<script setup>
import { computed } from 'vue'

// One group's access to one table or column, as OPA decided it. An error, or an answer we don't recognise, is
// never shown as access or as no access, because we don't know which it is.
const props = defineProps({ cell: { type: Object, default: undefined } })

const cell = computed(() => props.cell ?? { state: 'error', error: 'no answer for this group' })

const shown = computed(() => {
  switch (cell.value.state) {
    case 'read':
      return { kind: 'read', glyph: '✓', label: 'can read' }
    case 'masked':
      return { kind: 'masked', glyph: 'NULL', label: 'can read, but the value comes back empty (NULL)' }
    case 'denied':
      return { kind: 'denied', glyph: '–', label: 'cannot read' }
    default:
      return { kind: 'error', glyph: '⚠', label: `unknown: ${cell.value.error || 'the lookup failed'}` }
  }
})
</script>

<template>
  <span
    role="img"
    :class="shown.kind"
    :aria-label="shown.label"
    :title="shown.kind === 'error' ? shown.label : undefined"
  >{{ shown.glyph }}</span>
</template>

<style scoped>
.masked {
  font-family: monospace;
  font-size: 0.8rem;
}

.denied {
  color: var(--muted);
}

.error {
  color: var(--danger);
}
</style>
