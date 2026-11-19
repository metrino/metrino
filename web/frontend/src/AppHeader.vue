<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useJson } from './useJson.js'

// The header on every page. It asks the server who you are and where Metabase and Prometheus are, so the tabs a person sees
// follow the server's idea of them; the server enforces access either way.
const { data: me } = useJson('/api/me')
const { data: site } = useJson('/api/config')

// Each page is its own HTML file, so the current tab is just the path: admin pages start with /admin.
const onAdmin = location.pathname.startsWith('/admin')

const menuOpen = ref(false)
const profile = ref(null)
function closeOnOutsideClick(event) {
  if (profile.value && !profile.value.contains(event.target)) menuOpen.value = false
}
onMounted(() => document.addEventListener('click', closeOnOutsideClick))
onBeforeUnmount(() => document.removeEventListener('click', closeOnOutsideClick))
</script>

<template>
  <header class="bar">
    <nav aria-label="Main">
      <a href="/" :class="{ active: !onAdmin }" :aria-current="onAdmin ? undefined : 'page'">Documentation</a>
      <a v-if="site?.metabaseUrl" :href="site.metabaseUrl">Dashboards</a>
      <!-- The server only sends Prometheus's URL to Administrators. -->
      <a v-if="me?.admin && site?.prometheusUrl" :href="site.prometheusUrl">Prometheus</a>
      <a v-if="me?.admin" href="/admin/" :class="{ active: onAdmin }" :aria-current="onAdmin ? 'page' : undefined">Admin</a>
    </nav>

    <div v-if="me" ref="profile" class="profile">
      <span class="email">{{ me.email }}</span>
      <button
        type="button"
        class="icon"
        aria-label="Account menu"
        aria-haspopup="true"
        :aria-expanded="menuOpen"
        @click="menuOpen = !menuOpen"
      >
        <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true">
          <circle cx="12" cy="8" r="4" fill="currentColor" />
          <path d="M4 21c0-4.4 3.6-7 8-7s8 2.6 8 7z" fill="currentColor" />
        </svg>
      </button>
      <div v-if="menuOpen" class="menu">
        <p class="who">{{ me.email }}</p>
        <p v-if="me.groups.length" class="groups">{{ me.groups.join(', ') }}</p>
        <a href="/oauth2/sign_out?rd=/oauth2/sign_in">Sign out</a>
      </div>
    </div>
  </header>
</template>

<style scoped>
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 1rem;
  border-bottom: 1px solid var(--border);
}

nav {
  display: flex;
}

nav a {
  padding: 0.9rem 1rem;
  color: var(--fg);
  text-decoration: none;
  border-bottom: 2px solid transparent;
}

nav a:hover {
  background: var(--hover);
}

nav a.active {
  border-bottom-color: var(--accent);
  font-weight: 600;
}

.profile {
  position: relative;
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.email {
  color: var(--muted);
  font-size: 0.9rem;
}

.icon {
  display: flex;
  padding: 0.25rem;
  border: 0;
  border-radius: 50%;
  background: none;
  color: var(--fg);
  cursor: pointer;
}

.icon:hover {
  background: var(--hover);
}

.menu {
  position: absolute;
  top: 100%;
  right: 0;
  min-width: 14rem;
  padding: 0.75rem 1rem;
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  background: var(--bg);
  box-shadow: 0 4px 12px rgb(0 0 0 / 0.15);
}

.menu p {
  margin: 0;
}

.who {
  font-weight: 600;
  overflow-wrap: anywhere;
}

.groups {
  margin-bottom: 0.75rem;
  color: var(--muted);
  font-size: 0.9rem;
}

.menu a {
  display: block;
  padding-top: 0.5rem;
  border-top: 1px solid var(--border);
}

@media (max-width: 40rem) {
  .email {
    display: none;
  }
}
</style>
