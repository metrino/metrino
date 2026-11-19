<script setup>
// What the server says needs attention. The words "Warning" and "Note" are there for people who can't see
// the colours.
defineProps({ warnings: { type: Array, required: true } })
</script>

<template>
  <ul v-if="warnings.length" class="warnings">
    <li v-for="(warning, i) in warnings" :key="i" :class="warning.severity">
      <strong>{{ warning.severity === 'warning' ? 'Warning' : 'Note' }}:</strong> {{ warning.message }}
    </li>
  </ul>
</template>

<style scoped>
.warnings {
  margin: 0 0 1rem;
  padding: 0;
  list-style: none;
}

li {
  margin-bottom: 0.4rem;
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 0.4rem;
}

.warning {
  border-color: var(--danger);
  background: var(--danger-bg);
  color: var(--danger);
}

.note {
  color: var(--muted);
}
</style>
