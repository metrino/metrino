<script setup>
import AppHeader from './AppHeader.vue'
import CatalogCard from './CatalogCard.vue'
import CatalogTables from './CatalogTables.vue'
import SyncStatus from './SyncStatus.vue'
import WarningList from './WarningList.vue'
import { useJson } from './useJson.js'
import { metabaseLinks } from './metabaseLinks.js'
import { useServerClock } from './serverClock.js'
import { computed, onBeforeUnmount, ref } from 'vue'

// The server only answers /api/admin/ for Administrators, so this page shows an error to anyone else.
// It returns every Trino catalog with its rules, the Metabase connections for it, and each table and column
// with the groups that can read it, as OPA decides for Trino, plus what needs attention.
const { data, error } = useJson('/api/admin/rules')

const groups = computed(() => data.value?.groups ?? [])
const catalogs = computed(() => data.value?.catalogs ?? [])
const custom = computed(() => data.value?.rulesFormat === 'custom')
const links = computed(() => metabaseLinks(data.value?.metabaseUrl))
// The server's time, ticking, for the sync and scan times.
const clock = useServerClock(computed(() => data.value?.now))

// Warnings about one catalog show with it; the rest are a banner above both tabs.
const banner = computed(() => (data.value?.warnings ?? []).filter((w) => !w.catalog))
const warningsFor = (name) => (data.value?.warnings ?? []).filter((w) => w.catalog === name)
const needsAttention = (name) => warningsFor(name).some((w) => w.severity === 'warning')

// Catalogs with a warning first, then those with only notes, then the rest, each in the server's (name) order.
const rank = (name) => (needsAttention(name) ? 0 : warningsFor(name).length ? 1 : 2)
const byAttention = computed(() => [...catalogs.value].sort((a, b) => rank(a.name) - rank(b.name)))
const attentionCount = computed(() => catalogs.value.filter((c) => needsAttention(c.name)).length)

const withTables = computed(() => catalogs.value.filter((c) => c.tables.length))
const withoutTables = computed(() => catalogs.value.filter((c) => !c.tables.length).map((c) => c.name))

// The tab is in the URL's fragment (#tables or #catalogs), so a link or the back button can choose it.
const tabs = [
  { id: 'tables', label: 'Tables' },
  { id: 'catalogs', label: 'Catalogs' },
]
const fromHash = () => (location.hash === '#catalogs' ? 'catalogs' : 'tables')
const tab = ref(fromHash())
const onHash = () => {
  tab.value = fromHash()
}
window.addEventListener('hashchange', onHash)
onBeforeUnmount(() => window.removeEventListener('hashchange', onHash))

function select(id) {
  location.hash = id
}

// Left and right arrows move between the tabs, as in any tab list.
function onKey(event) {
  const step = { ArrowLeft: -1, ArrowRight: 1 }[event.key]
  if (!step) return
  const i = tabs.findIndex((t) => t.id === tab.value)
  const next = tabs[(i + step + tabs.length) % tabs.length].id
  select(next)
  document.getElementById(`tab-${next}`)?.focus()
}
</script>

<template>
  <AppHeader />
  <main>
    <h1>Admin</h1>
    <h2>Who can read what</h2>
    <p v-if="error">Could not load the rules: {{ error }}</p>
    <p v-else-if="!data">Loading...</p>
    <template v-else>
      <WarningList :warnings="banner" />
      <SyncStatus :sync="data.sync" :now="clock" />

      <div class="tabs" role="tablist" aria-label="Admin views" @keydown="onKey">
        <button
          v-for="t in tabs"
          :id="`tab-${t.id}`"
          :key="t.id"
          type="button"
          role="tab"
          :aria-selected="tab === t.id"
          :aria-controls="`panel-${t.id}`"
          :tabindex="tab === t.id ? 0 : -1"
          @click="select(t.id)"
        >
          {{ t.label }}
          <span v-if="t.id === 'catalogs' && attentionCount" class="badge">
            {{ attentionCount }} need{{ attentionCount === 1 ? 's' : '' }} attention
          </span>
        </button>
      </div>

      <div v-if="tab === 'tables'" id="panel-tables" role="tabpanel" aria-labelledby="tab-tables">
        <p class="note">
          Every table and column Metabase knows, and which groups can read it, as Trino's access policy decides.
          Administrators can read everything. NULL means the group can read the column but gets empty values.
        </p>
        <p v-if="!withTables.length" class="note">
          Metabase knows no tables yet: it has no Trino connections through the reverse proxy, or hasn't scanned them.
        </p>
        <CatalogTables
          v-for="catalog in withTables"
          :key="catalog.name"
          :catalog="catalog"
          :groups="groups"
          :warnings="warningsFor(catalog.name).length"
          :custom="custom"
          :explained="data.explained"
          :links="links"
        />
        <p v-if="withoutTables.length" class="note">
          No tables listed for {{ withoutTables.join(', ') }}: Metabase doesn't know any. See
          <a href="#catalogs">Catalogs</a>.
        </p>
      </div>

      <div v-else id="panel-catalogs" role="tabpanel" aria-labelledby="tab-catalogs">
        <p class="note">
          Every catalog Trino has, Metabase connects to or the rules name, with the rules for it. Those that need
          attention come first.
        </p>
        <p v-if="!catalogs.length" class="note">
          No catalogs yet: Trino has none configured, Metabase has no Trino databases, and there are no rules.
        </p>
        <CatalogCard
          v-for="catalog in byAttention"
          :key="catalog.name"
          :catalog="catalog"
          :warnings="warningsFor(catalog.name)"
          :custom="custom"
          :now="clock"
          :links="links"
        />
      </div>
    </template>
  </main>
</template>

<style scoped>
.note {
  color: var(--muted);
}

.tabs {
  display: flex;
  gap: 0.25rem;
  margin: 1rem 0;
  border-bottom: 1px solid var(--border);
}

.tabs button {
  margin-bottom: -1px;
  padding: 0.5rem 1rem;
  border: 1px solid transparent;
  border-radius: 0.4rem 0.4rem 0 0;
  background: none;
  color: var(--muted);
  font: inherit;
  cursor: pointer;
}

.tabs button[aria-selected='true'] {
  border-color: var(--border);
  border-bottom-color: var(--bg);
  background: var(--bg);
  color: var(--fg);
  font-weight: 600;
}

.tabs button:hover {
  color: var(--fg);
}

.badge {
  margin-left: 0.4rem;
  padding: 0.1rem 0.45rem;
  border-radius: 0.75rem;
  background: var(--danger-bg);
  color: var(--danger);
  font-size: 0.8rem;
  font-weight: 600;
}
</style>
