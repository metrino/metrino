<script setup>
import { computed } from 'vue'
import { ago } from './ago.js'

// When the web pod last synced the groups and the column list from Metabase: each file's own modification time,
// which the sync sets after every successful run, so it survives a restart of the sync. It ticks with `now`, the
// server's clock. When Metabase last scanned each connection is on the Catalogs tab.
const props = defineProps({
  sync: { type: Object, default: null },
  now: { type: Number, required: true },
})

const parts = computed(() => {
  const files = props.sync?.files ?? {}
  const out = []
  if (files.group) out.push(`Groups synced ${ago(files.group.syncedAt, props.now)}`)
  if (files.schema) out.push(`Column list synced ${ago(files.schema.syncedAt, props.now)}`)
  return out
})
</script>

<template>
  <p v-if="parts.length" class="sync">
    {{ parts.join(' · ') }}
    · In Kubernetes, Trino's coordinator syncs its own copy of the groups with the same settings; this page doesn't see it.
  </p>
</template>

<style scoped>
.sync {
  color: var(--muted);
  font-size: 0.9rem;
}
</style>
