<script setup>
import { computed } from 'vue'
import MetabaseLink from './MetabaseLink.vue'
import WarningList from './WarningList.vue'
import { ago } from './ago.js'

// One Trino catalog as a data source: whether Trino has it, the Metabase connections that read it, the rules that
// name it, and what needs attention. A warning about one rule shows under that rule; the rest show at the top.
// Connections link to their settings and table metadata in Metabase, when the server gave Metabase's URL. Each
// connection through the proxy says whether "Optimized prepared statements" is on, which queries with parameters
// need; off is also a warning. optimizedPrepare is null when the Metabase sync didn't record it.
const props = defineProps({
  catalog: { type: Object, required: true },
  warnings: { type: Array, default: () => [] },
  // A rules document we can't read as {key: [groups]}, so there are no rules to list.
  custom: { type: Boolean, default: false },
  now: { type: Number, required: true }, // the server's clock, ticking (serverClock.js)
  links: { type: Object, required: true }, // metabaseLinks()
})

const listed = computed(() => (props.custom ? [] : props.catalog.rules))
const general = computed(() => props.warnings.filter((w) => !listed.value.some((r) => r.key === w.key)))
const forRule = (key) => props.warnings.filter((w) => w.key === key)
const attention = computed(() => props.warnings.some((w) => w.severity === 'warning'))

// inTrino is null when the server doesn't know Trino's catalogs.
const inTrino = computed(() => {
  if (props.catalog.inTrino == null) return 'unknown'
  return props.catalog.inTrino ? 'configured' : 'not configured'
})

const prepared = (c) => (c.optimizedPrepare == null ? 'unknown' : c.optimizedPrepare ? 'on' : 'off')

// Rules at these levels also apply to tables that don't exist yet.
const coversLater = ['catalog', 'schema']
</script>

<template>
  <section :class="{ attention }">
    <h3>{{ catalog.name }}</h3>
    <dl>
      <dt>In Trino</dt>
      <dd>{{ inTrino }}</dd>
      <dt>Metabase</dt>
      <dd v-if="catalog.connections.length">
        <div v-for="c in catalog.connections" :key="c.id">
          connection #{{ c.id }} <strong>{{ c.name }}</strong>&nbsp;<span class="muted">(scanned {{ ago(c.lastScan, now) }})</span>
          <span :class="['prepared', prepared(c)]">optimized prepared statements: {{ prepared(c) }}</span>
          <span v-if="links.database(c.id)" class="actions">
            <MetabaseLink :href="links.database(c.id)">Settings</MetabaseLink>
            <MetabaseLink :href="links.dataModel(c.id)">Table metadata</MetabaseLink>
          </span>
        </div>
      </dd>
      <dd v-else>
        no connection through the reverse proxy
        <span v-if="links.newDatabase()" class="actions">
          <MetabaseLink :href="links.newDatabase()">Add a database in Metabase</MetabaseLink>
        </span>
      </dd>
      <template v-if="catalog.bypassing.length">
        <dt>Bypassing the proxy</dt>
        <dd>
          <div v-for="c in catalog.bypassing" :key="c.id" class="bypassing">
            connection #{{ c.id }} <strong>{{ c.name }}</strong>
            <span v-if="links.database(c.id)" class="actions">
              <MetabaseLink :href="links.database(c.id)">Settings</MetabaseLink>
            </span>
          </div>
        </dd>
      </template>
      <dt>Tables</dt>
      <dd>
        {{ catalog.tables.length }} known to Metabase<template v-if="catalog.tables.length">:
          see <a href="#tables">Tables</a></template>
      </dd>
    </dl>
    <WarningList :warnings="general" />

    <template v-if="!custom">
      <h4>Rules</h4>
      <p v-if="!catalog.rules.length" class="muted">No rules name this catalog, so only Administrators can read it.</p>
      <ul v-else class="rules">
        <li v-for="rule in catalog.rules" :key="rule.key">
          <code>{{ rule.key }}</code>: {{ rule.groups.length ? rule.groups.join(', ') : 'no groups' }}
          <span v-if="coversLater.includes(rule.level)" class="muted">(also covers tables added later)</span>
          <WarningList :warnings="forRule(rule.key)" />
        </li>
      </ul>
    </template>
  </section>
</template>

<style scoped>
section {
  margin-bottom: 1.25rem;
  padding: 0.75rem 1rem;
  border: 1px solid var(--border);
  border-radius: 0.5rem;
}

section.attention {
  border-color: var(--danger);
}

h3 {
  margin: 0 0 0.5rem;
}

h4 {
  margin: 1rem 0 0.25rem;
}

dl {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 0.25rem 1rem;
  margin: 0 0 0.75rem;
}

dt {
  color: var(--muted);
}

dd {
  margin: 0;
}

.muted {
  color: var(--muted);
  font-size: 0.9rem;
}

.actions {
  margin-left: 0.5rem;
  font-size: 0.9rem;
}

.actions > * + * {
  margin-left: 0.75rem;
}

.bypassing {
  color: var(--danger);
}

.prepared {
  margin-left: 0.5rem;
  color: var(--muted);
  font-size: 0.9rem;
}

.prepared.off {
  color: var(--danger);
  font-weight: 600;
}

.rules {
  margin: 0;
  padding-left: 1.25rem;
}

.rules li {
  margin-bottom: 0.25rem;
}

.rules :deep(.warnings) {
  margin: 0.25rem 0 0.5rem;
}

code {
  font-size: 0.9rem;
}
</style>
