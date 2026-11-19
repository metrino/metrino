// Links to Metabase's admin pages, from Metabase's URL (the server's METABASE_LINK_URL) and the IDs the Metabase
// sync reads. Each returns null when there is no URL or no ID, so the page shows plain text instead.
// "student_courses" → "student-courses", as Metabase writes table names in its URLs.
const slug = (name) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')

export function metabaseLinks(base) {
  const root = base ? base.replace(/\/+$/, '') : ''
  const dataModel = (database) => `${root}/admin/datamodel/database/${database}`
  // A schema's ID in Metabase's URLs is the database ID and the schema's name: "1:public".
  const table = (schema, m) =>
    `${dataModel(m.database)}/schema/${m.database}:${encodeURIComponent(schema)}/table/${m.table}`
  return {
    // A connection's settings: host, port, catalog, user.
    database: (id) => (root && id ? `${root}/admin/databases/${id}` : null),
    newDatabase: () => (root ? `${root}/admin/databases/create` : null),
    // A connection's tables and columns, as Metabase scanned them.
    dataModel: (id) => (root && id ? dataModel(id) : null),
    table: (schema, m) => (root && m ? table(schema, m) : null),
    field: (schema, m, field) => (root && m && field ? `${table(schema, m)}/field/${field}` : null),
    // A table's rows, as anyone browsing Metabase would open them. Metabase reads the ID before the first "-"; the
    // rest is only for people reading the URL.
    data: (name, m) => (root && m ? `${root}/table/${m.table}-${slug(name)}` : null),
  }
}
