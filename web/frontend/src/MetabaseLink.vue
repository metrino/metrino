<script setup>
// A link into Metabase, which is its own site, so it opens in a new tab. Without an href it is the text alone.
defineProps({ href: { type: String, default: null } })
</script>

<template>
  <a v-if="href" :href="href" target="_blank" rel="noopener" class="metabase"><slot /></a>
  <template v-else><slot /></template>
</template>

<style scoped>
.metabase {
  color: inherit;
  text-decoration: underline dotted;
  /* Low enough to leave the underscores in column names visible. */
  text-underline-offset: 0.4em;
}

.metabase:hover {
  color: var(--accent);
  text-decoration-style: solid;
}
</style>
