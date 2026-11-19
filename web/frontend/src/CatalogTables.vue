<script setup>
import AccessCell from './AccessCell.vue'
import MetabaseLink from './MetabaseLink.vue'

// One Trino catalog's tables and columns, as Metabase knows them, and which groups can read each. The catalog's
// rules and warnings are on the Catalogs tab; `warnings` only says how many there are. Table and column names
// link to them in Metabase's table metadata, when the server gave Metabase's URL and their IDs. Under each name is
// the description an admin wrote for it there, if any, as plain text. "view data" opens the table's rows in
// Metabase, which shows them as the admin's own Metabase user sees them. A table an admin hid in Metabase says so:
// people can't browse it or start a question from it there, though Trino's rules still decide who reads it.
defineProps({
  catalog: { type: Object, required: true },
  groups: { type: Array, required: true },
  warnings: { type: Number, default: 0 },
  // A rules document we can't read as {key: [groups]}, so no row can be explained.
  custom: { type: Boolean, default: false },
  // Whether OPA could say which rule decides each row. If not, an empty decidedBy means "unknown", not "no rule".
  explained: { type: Boolean, default: false },
  links: { type: Object, required: true }, // metabaseLinks()
})

// What to say in "Decided by" when no rule does: with no rule only Administrators can read the row, unless the
// policy grants it on its own (information_schema, for one), which shows as a group that can read it. A masked
// cell isn't that: it is a column with a "." in its name, which no rule can name, in a table the group reads.
function noRule(row, groups) {
  const builtIn = groups.some((g) => row.access[g]?.state === 'read')
  return builtIn ? 'no rule (built-in policy)' : 'no rule: Administrators only'
}
</script>

<template>
  <section>
    <h3>{{ catalog.name }}</h3>
    <p v-if="catalog.connections.length" class="muted">
      <span v-for="(c, i) in catalog.connections" :key="c.id">
        {{ i ? ' · ' : '' }}Metabase connection #{{ c.id }} {{ c.name }}<template v-if="links.database(c.id)">:
          <MetabaseLink :href="links.database(c.id)">settings</MetabaseLink>,
          <MetabaseLink :href="links.dataModel(c.id)">table metadata</MetabaseLink></template>
      </span>
    </p>
    <p v-if="warnings" class="attention">
      {{ warnings }} {{ warnings === 1 ? 'warning' : 'warnings' }} about this catalog:
      see <a href="#catalogs">Catalogs</a>.
    </p>
    <table>
      <thead>
        <tr>
          <th scope="col">Table / column</th>
          <th scope="col">Type</th>
          <th v-for="group in groups" :key="group" scope="col" class="group">{{ group }}</th>
          <th v-if="!custom" scope="col">Decided by</th>
        </tr>
      </thead>
      <tbody v-for="table in catalog.tables" :key="`${table.schema}.${table.name}`">
        <tr class="table-row">
          <th scope="row">
            <MetabaseLink :href="links.table(table.schema, table.metabase)"><code>{{ table.schema }}.{{ table.name }}</code></MetabaseLink>
            <span v-if="links.data(table.name, table.metabase)" class="view">
              <MetabaseLink :href="links.data(table.name, table.metabase)">view data</MetabaseLink>
            </span>
            <span
              v-if="table.hiddenInMetabase"
              class="hidden-badge"
              title="People can't browse this table or start a question from it in Metabase. Change its visibility in Metabase's table metadata (the table's name links there)."
            >⚠ Hidden in Metabase</span>
            <p v-if="table.description" class="description">{{ table.description }}</p>
          </th>
          <td class="muted">table</td>
          <td v-for="group in groups" :key="group" class="group">
            <AccessCell :cell="table.access[group]" />
          </td>
          <td v-if="!custom" class="muted">
            <code v-if="table.decidedBy">{{ table.decidedBy }}</code>
            <template v-else-if="explained">{{ noRule(table, groups) }}</template>
            <template v-else>unknown</template>
          </td>
        </tr>
        <tr v-for="column in table.columns" :key="column.name">
          <th scope="row" class="column">
            <MetabaseLink :href="links.field(table.schema, table.metabase, column.metabaseField)"><code>{{ column.name }}</code></MetabaseLink>
            <p v-if="column.description" class="description">{{ column.description }}</p>
          </th>
          <td class="muted">{{ column.type }}</td>
          <td v-for="group in groups" :key="group" class="group">
            <AccessCell :cell="column.access[group]" />
          </td>
          <td v-if="!custom" class="muted">
            <code v-if="column.decidedBy">{{ column.decidedBy }}</code>
            <template v-else-if="explained">{{ noRule(column, groups) }}</template>
            <template v-else>unknown</template>
          </td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.muted {
  color: var(--muted);
  font-size: 0.9rem;
}

.attention {
  color: var(--danger);
}

section {
  margin-bottom: 2rem;
  overflow-x: auto;
}

table {
  width: 100%;
  border-collapse: collapse;
}

th,
td {
  padding: 0.4rem 0.75rem;
  border-bottom: 1px solid var(--border);
  text-align: left;
}

thead th {
  color: var(--muted);
  font-size: 0.85rem;
  font-weight: 600;
}

tbody th {
  font-weight: 400;
}

.table-row {
  background: var(--hover);
}

.table-row th {
  font-weight: 600;
}

.column {
  padding-left: 2rem;
}

/* Metabase descriptions are often Markdown with line breaks; shown as text, they keep the breaks. */
.description {
  max-width: 28rem;
  margin: 0.15rem 0 0;
  color: var(--muted);
  font-size: 0.85rem;
  font-weight: 400;
  white-space: pre-line;
  /* A long word or URL breaks anywhere rather than widen the table until it scrolls. */
  overflow-wrap: anywhere;
}

.view {
  margin-left: 0.5rem;
  white-space: nowrap;
  font-size: 0.85rem;
  font-weight: 400;
}

.hidden-badge {
  margin-left: 0.5rem;
  padding: 0.05rem 0.4rem;
  border: 1px solid var(--danger);
  border-radius: 0.3rem;
  background: var(--danger-bg);
  color: var(--danger);
  font-size: 0.8rem;
  font-weight: 600;
  white-space: nowrap;
}

tbody tr:hover {
  outline: 1px solid var(--border);
}

.group {
  text-align: center;
}

code {
  font-size: 0.9rem;
}
</style>
