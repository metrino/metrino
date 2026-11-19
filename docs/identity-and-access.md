# Identity and access plan

How people sign in, where their roles live, and how Trino decides what each person can read. This is the
agreed design and the order we build it in. Update it when something changes.

## Goals

- One place to manage people and their roles: Metabase. Trino uses the same groups.
- ACPS doesn't have to change anything in their own directory to give someone a role.
- People sign in with Google. We never store their passwords.
- Trino sees the real person running each query, not a shared service account.
- Few moving parts. Whoever maintains this after us should be able to change who can see what by editing
  one short file, without learning the rest of the system.

## How it works

```
                     Google (sign-in only)
                           │
Browser ──► Metabase ──────┴──► reverse proxy ──► Trino ──► OPA
            (people, groups,    (sidecar,           ▲       (rules.rego: which group
             dashboards)         localhost only)    │        reads which data)
               ▲      ▲               │             │
               │      └─ "who is ─────┘             │ group file
               │          user 7?"                  │
               │                                    │
               └────────── Metabase sync ───────────┘
                 (reads people and groups, writes Trino's group file)
```

| Question | Answered by |
|---|---|
| Who is this person? | Google |
| Who may use the site, and which groups are they in? | Metabase (Admin > People) |
| Which person is Metabase user 7? | Metabase (the reverse proxy asks `/api/user/7` on every query) |
| Which groups is alice@… in, for Trino? | Trino's group file, which the Metabase sync writes |
| Which tables and columns can a group read? | OPA, from the deployment's rules file (`accessPolicy.rules`) |
| Which dashboards and Metabase features (raw SQL) can a group use? | Metabase (`access.sh`, `manifest.json`) |

The email address ties it together. Google identifies the person by email, Metabase matches the Google
account to a user by email, the proxy sends the email to Trino as the user, and the group file lists users by
email.

### Components

- **Metabase**: the list of people and their groups. An admin adds each person (Google sign-in, with
  automatic account creation off) and puts them in the groups for their role. Groups also decide which
  collections and dashboards a person sees.
- **Metabase sync** (`trino/identity/cmd/metabase-sync`): every 30 seconds, copies what Trino and the web pod
  need from Metabase into files. It only ever reads from Metabase. It writes:
  - the group file, which Trino's built-in `file` group provider reads: Metabase's active users and their
    groups, which keep their Metabase names;
  - in the web pod only, the schema file: Metabase's Trino connections, and every column of the tables it knows
    through the ones that use the reverse proxy, which the admin page lists with who can read each, asking the
    web pod's OPA as Trino would (see The admin access page);
  - in the web pod only, the status file: the sync interval and each of the other files' last error, so the
    admin page can warn when a sync is failing or stale.

  After each successful sync of a file, it sets the file's modification time to when the run began, even if
  nothing changed, so the file itself says when it last synced, across restarts of the sync.
- **Reverse proxy** (`trino/identity/cmd/reverse-proxy`): reads the `-- Metabase:: userID: N` comment
  Metabase adds as the last line of each query, asks Metabase which email that user has, and sends the query
  to Trino as that email (`X-Trino-User`).
- **Trino**: rereads the group file every 5 seconds and passes each query's user and groups to OPA.
- **OPA**: decides every access check. `rules.rego` lists which groups can read what. `policy.rego` applies
  the rules and never needs editing to change access.

The Metabase sync and the proxy are one Go module (`trino/identity`) built into one image.

## Managing access

### Add a person, or change their role
In Metabase, Admin > People: add them with the email they sign in with, and put them in their role's group.
The Metabase sync copies the change to Trino within about 35 seconds (the sync's 30 seconds plus Trino's 5).

Deactivating someone in Metabase stops their queries straight away: the proxy checks Metabase on every query
and rejects deactivated users. They also drop out of the group file at the next sync.

A person in no group (other than Metabase's built-in All Users) can sign in but reads no data.

### Change what a group can read
Edit the deployment's rules file (the one passed as `accessPolicy.rules`), or `opa/rules.rego`, the sample
rules compose uses. Each entry names a catalog, schema, table or column and lists
the Metabase groups that may read it:

```rego
rules := {
	"example_jdbc": ["Analysts"],                                   # a catalog
	"example_jdbc.public.students": ["Analysts", "Teachers"],       # a table
	"example_jdbc.public.students.first_name": ["Analysts"],        # a column
}
```

- **The most specific entry wins.** For a table, OPA uses the table's own entry if there is one, otherwise
  its schema's, otherwise its catalog's. In the example, Teachers can read `students` but not the rest of
  `example_jdbc`.
- **No entry means only Administrators.** Administrators can read everything, so they never need listing.
- **A column you can't read comes back as `NULL`**, instead of failing the query, so the same dashboard works
  for every role. A column entry can only take access away: to read a column you also need its table.
- **Being in any listed group is enough.** A person in several groups can read whatever any of them can.
- **Group names are Metabase's, exactly** (case and spaces included). A misspelled group matches nobody, so
  it denies rather than grants.
- **A name containing "." can't be named, so only Administrators read it.** An entry joins names with ".", so
  it can't tell `"c.s.t.a"` meaning column `a` of table `t` from the whole table `t.a`. Rather than guess, no
  entry applies to a schema, table or column whose name has a ".": such a schema or table is for Administrators
  only, and such a column comes back `NULL` for everyone else. So an entry has at most four parts. The admin
  page warns about both. To share such data, rename it upstream or expose it through a view.

Then update the `rules_test.rego` next to it to match. Those tests state what the real rules promise (for
example, teachers never see student names), so widening access shows up in the review as a changed test.

### Add a role
Create the group in Metabase (Admin > People > Groups), then add entries naming it to `rules.rego`. Until it
appears in `rules.rego`, its members read nothing.

## The admin access page

The web pod's admin page (`/admin/`, Administrators only) shows who can read what, so an administrator can
check the rules without reading Rego. It also warns about anything that makes the answer wrong or incomplete.
This section says what it shows and what its warnings mean; `web/` has the code.

### What it shows
The page has two tabs, under the warnings that aren't about one catalog and the sync times:

- **Tables**: each catalog's tables and columns that Metabase knows, with a cell for each group and the rule
  that decides it. A catalog with warnings says how many, and catalogs without tables are named at the end.
  Each table and column name links to it in Metabase's table metadata, with its description from there under it,
  and each table has a "view data" link to its rows in Metabase. A table an admin hid in Metabase's table
  metadata is marked "Hidden in Metabase": people can't browse it or start a question from it there, though the
  rules still decide who reads it through SQL.
- **Catalogs**: every catalog, whether Trino has it, the Metabase connections that read it (with when Metabase
  last scanned each, and whether it has "Optimized prepared statements" on) and any that bypass the proxy, the rules that name it, and its warnings. Each connection
  links to its settings in Metabase, and a catalog without one links to adding a database. A warning about one
  rule shows under that rule.
  Catalogs with a warning come first, then those with only notes. The tab shows how many need attention.

The tab is in the URL (`/admin/#tables` or `/admin/#catalogs`). The page lists every catalog in the union of
Trino's (`TRINO_CATALOGS`, names only), those of Metabase's Trino connections and those the rules name, so a
catalog nobody has set up in Metabase still shows. Administrators read everything, so they are left out.

Each cell is one of four states:

| State | Meaning |
|---|---|
| read | the group can read it |
| masked | a column of a table the group can read, which Trino returns as `NULL` |
| denied | the group can't read the table, or any of its columns |
| error | a lookup failed, so the page doesn't know. Never shown as read or denied |

The page never works these out from the rules. It asks the web pod's OPA, which runs the same policy as Trino's
(`helm/opa/policy.rego`), the questions Trino would ask, so it can't disagree with Trino. For the same reason
the rules key that decided each table and column ("Decided by") comes from OPA, from `deciding_keys` in the
policy: an array with, for each path in `input.paths` in order, the key `most_specific_key` picks, or `null` if
none does (including a path with a "." in a name). An array, not an object keyed by the joined path, since two
paths can join to the same text. Trino never asks for it. It lives in `policy.rego`, not a web-only file, so that
a deployment's own policy without `most_specific_key` still compiles in the web pod. If OPA can't answer it, or a
custom policy has no `deciding_keys`, "Decided by" says "unknown" rather than "no rule". A row no rule decides
says "no rule: Administrators only", or "no rule (built-in policy)" if some group can still read it because the
policy grants it on its own, as it does `information_schema`. A catalog or schema rule is marked "also covers
tables added later", since a new table is visible to its groups straight away.

The page's data comes from one document, `GET /api/admin/rules`. Its top-level fields:

- `now`: the server's time. The page keeps the gap between it and the browser's clock, and measures ages from
  the server's time from then on, ticking every second.
- `metabaseUrl`: where the page's links to Metabase start (`METABASE_LINK_URL`), or `""` for no links.
- `rules`: the rules document from OPA, as it is.
- `rulesFormat`: `standard` for `{key: [groups]}`, or `custom`.
- `explained`: whether OPA could say which rule decides each path. If false, an empty `decidedBy` means unknown,
  not "no rule".
- `groups`: the groups shown as columns, without Administrators.
- `sync`: `{"intervalSeconds": 30, "files": {"group": {"syncedAt": "..."}, "schema": {...}}}`: when each file
  the page reads last synced (`null` if never), from the file's modification time, and its `lastError` and
  `lastErrorAt` if its last sync failed. `schema` is there when `SCHEMA_FILE` is set. Without a status file,
  `intervalSeconds` is 0 and there are no errors.
- `warnings`: what needs attention (see What the warnings mean).
- `catalogs`: each catalog with its connections through the proxy (`connections`), those that bypass it
  (`bypassing`), its rules and its tables. Every table and column has its `decidedBy` and each group's cell,
  and Metabase's IDs (`metabase` on a table, `metabaseField` on a column) and `description` when the schema
  file has them.

A rules document that isn't the standard `{key: [groups]}` form (a deployment's own policy may keep its rules
another way) shows a banner. The cells still come from OPA's decisions, but the page doesn't list or check the
rules.

### How it knows
The web pod reads three files, each written by its own Metabase sync, and the web pod's OPA:

- **The group file**, as Trino reads it, for the groups.
- **The schema file** (`SCHEMA_FILE`), JSON from Metabase:

  ```json
  {"connections": [{"id": 1, "name": "trino", "catalog": "example_jdbc", "address": "127.0.0.1:9090",
                    "viaProxy": true, "optimizedPrepare": true, "lastScan": "2026-11-08T10:00:00Z"}],
   "catalogs": {"example_jdbc": {"public": {"students": [{"name": "student_id", "type": "bigint"}]}}},
   "metabase": {"example_jdbc": {"public": {"students": {"database": 1, "table": 6,
                                                         "description": "One row per enrolled student.",
                                                         "fields": {"student_id": {"id": 28}}}}}}}
  ```

  `connections` is every Trino database in Metabase, with the address it connects to (`catalog` is `""` if it
  has none), whether that is the reverse proxy, whether it has "Optimized prepared statements" on
  (`optimizedPrepare`: the Starburst driver's `prepared-optimized`, or `explicitPrepare=false` in its additional
  JDBC options), and when Metabase last scanned it successfully (`lastScan` is left out if unknown). `catalogs` is catalog, schema, table, then the table's columns with their Trino types,
  and only has tables of connections that use the proxy: through any other, Trino sees the connection's own
  user, not the person, so the rules don't decide who reads them. The sync needs `TRINO_PROXY_ADDRESSES` for
  this (required with `SCHEMA_FILE`): the proxy's `host:port` spellings, comma-separated. A connection counts
  as using the proxy only if its address is exactly one of them. The scan times come from Metabase's
  `/api/task`; if that call fails the file is still written, without them. `metabase` has, for each table in
  `catalogs`, Metabase's IDs for its database, the table and each column, for the page's links into Metabase,
  the descriptions an admin wrote in Metabase's table metadata (left out where there are none), and
  `"hidden": true` for a table an admin hid there (any `visibility_type` but none). A file
  from an older sync has none of this, and the page then shows the names without links or descriptions.
- **The status file** (`STATUS_FILE`), JSON, rewritten on every run:

  ```json
  {"intervalSeconds": 30,
   "files": {"group":  {},
             "schema": {"lastError": "...", "lastErrorAt": "2026-11-08T10:00:00Z"}}}
  ```

  Each file the sync writes has an entry once it has tried. A failure sets `lastError`; the next success
  clears it. When each file last synced isn't here: it is the file's own modification time, which the sync
  sets after each successful run. The web server reads a file's contents and its time from one open file, so
  they agree. The empty group file the sync writes at startup has the Unix epoch as its time, meaning it has
  never synced. The status file is kept in memory and rewritten, so a restarted sync starts with no errors,
  while the files' times carry on.

The schema file is only as current as Metabase's own scan of its databases (hourly by default), so a table
added upstream appears after that. The page shows each connection's last scan and how long ago each file last
synced, ticking live; the sync interval appears only in the `sync-stale` warning. The sync warnings
(`sync-failing`, `sync-stale`) need the status file: without one there are none, though the times still show.

**In Kubernetes, the page shows the web pod's sync, not Trino's.** The coordinator runs its own Metabase sync and
group file with the same settings, and the page reads nothing from it: it has no schema or status file, and
its files aren't mounted in the web pod. So a clean page says nothing about the coordinator's sync. If the
web pod's is current and the coordinator's is stuck, the page is right about what the rules say and wrong about
who Trino thinks is in a group. Check the coordinator's log with the `kubectl logs ... -c metabase-sync`
command in the README, and restart both pods after rotating the API key, as the README says. In compose there
is one `metabase-sync`, which feeds both Trino and the web server, so this doesn't arise.

### What the warnings mean
A warning is something wrong or missing. A note is worth knowing but may be intended. Each warning appears under
its catalog if it has one.

| Kind | Level | Meaning |
|---|---|---|
| `sync-failing` | warning | The last sync of the groups or column list failed, or the file has never synced. The page shows the last good copy, if there is one. |
| `sync-stale` | warning | A file hasn't synced for three intervals, by its modification time, so the sync may have stopped. |
| `rules-custom` | warning | The rules aren't `{key: [groups]}`, so the page doesn't list or check them. |
| `opa-failures` | warning | Some lookups in OPA failed; cells marked ⚠ are unknown. |
| `schema-old-format` | warning | The schema file is from an older Metabase sync, so the page has no column list or connections until the sync image is updated. The rules and groups still show. |
| `catalog-not-in-metabase` | warning | A Trino catalog that no Metabase connection reaches through the proxy, so people can't use it in Metabase. |
| `catalog-not-in-trino` | warning | A Metabase connection through the proxy uses a catalog Trino doesn't have, so its queries fail. |
| `duplicate-connections` | warning | Two or more connections through the proxy use one catalog. The page shows the columns from the lowest ID. |
| `connection-bypasses-proxy` | warning | A connection's address isn't the proxy's, so its queries run as the Metabase connection's own Trino user and its tables aren't listed. Point it at the proxy (README). |
| `connection-without-catalog` | warning | A connection has no catalog set, so its tables aren't listed. |
| `connection-explicit-prepare` | warning | A connection through the proxy has "Optimized prepared statements" off, so its queries with parameters (dashboard filters, SQL variables) fail: they reach Trino as a `PREPARE` and a separate `EXECUTE`, and only the `PREPARE` says who ran it. Turn it on in the connection's settings. |
| `rule-unknown-catalog` | warning | A rule names a catalog Trino doesn't have, so it grants nothing. |
| `rule-unknown-group` | warning | A rule lists a group with no active Metabase user in it, or that doesn't exist, so it grants nothing for that group. |
| `rule-narrows` | warning | A schema or table rule leaves out a group its parent rule lists, which takes that group's access away. May be meant. |
| `rule-column-without-table` | warning | A column rule lists a group that can't read the column's table, so it grants nothing. |
| `rule-not-in-metabase` | note | A rule names a schema, table or column Metabase hasn't found. Check the spelling, or wait for Metabase's next scan. |
| `catalog-without-rules` | note | No rule names a catalog, so only Administrators can read it. |
| `rule-too-many-parts` | warning | A rule key has more than four parts. No name the policy matches contains ".", so it matches nothing and grants nothing. |
| `name-with-dot` | warning | A table's schema or table name has a ".", so only Administrators can read it, or some of its columns' names do, so they come back `NULL` for everyone else. Rules can't name them; rename them to share them. |

The rule checks compare the rules' group lists. Which key is a rule's parent comes from `deciding_keys`, like
every other decision on the page. The `system` catalog is built in and holds no data, so it's exempt from the
catalog checks.

## What the rules allow

These hold for every user, whatever `rules.rego` says:

- **Read-only.** Nobody can create, change or drop anything, or impersonate another user, through Trino.
  Administrators included.
- **Names of catalogs, schemas and functions are visible to everyone.** Table and column names are only
  visible where the person can read the table.
- **Metadata tables** (`information_schema`, `system.jdbc`, `system.metadata`) can be queried by everyone.
  Trino filters their rows down to what the person can see.
- **Metabase's own connection user (`trino`)** can list every table and column, so Metabase can sync its
  schema, but reads no data. Metabase's background scans (field values for filter dropdowns, fingerprints)
  run as this user, so they store nothing from the data in Metabase.

## Sample rules

Without row-level security a teacher would see every row in the district, not just their own school or
students. So the sample rules give teachers the directory and catalog tables but hide student names, contact
details and grades, and staff phone numbers. That's still enough for district-level counts and trends (e.g.
students per school, enrollment by year).

| Table (`example_jdbc.public`) | Teachers | Hidden from Teachers |
|---|---|---|
| `schools`, `courses`, `departments` | read | nothing |
| `staff` | read | `phone` |
| `students` | read | `first_name`, `last_name`, `email` |
| `student_courses` | read | `grade` |

Analysts read everything in `example_jdbc` and `openapi`. Teachers can't read `openapi`, which serves the
same student and staff data over HTTP and isn't used by any dashboard. Revisit the hidden student columns
once row-level security can limit a teacher to their own students.

Teachers get tables one by one rather than the whole `public` schema, so a new table isn't visible to them
until someone adds it.

## Why it works this way

The alternatives we rejected, so they don't get relitigated.

- **People and roles live in Metabase.** Admins already work there, and Metabase admins are already trusted
  with all data (they can edit the Trino connection), so this doesn't widen who has to be trusted.
  - *lldap* (the first version of this plan): a directory server, its storage and secrets, seeding, two bind
    accounts, a sync that created and deactivated Metabase accounts, and oauth2-proxy in front of its UI. Too
    much for a few admins, managed by the same people who administer Metabase anyway.
  - *A role file in the repo*: a pull request for every role change. Fine for developers, not for admins.
  - *Google Workspace Secure LDAP*: ACPS would have to manage our roles in their directory, and it depends on
    their Workspace edition and needs their admin to issue certificates.
- **Trino reads groups from a file the Metabase sync writes.** The `file` group provider ships with Trino. The
  file must exist when Trino starts, so the sync writes an empty one first. It has one `group:user,user` line
  per group, listing users by email exactly as Metabase spells it, the same spelling the proxy sends. Group
  names Trino would read differently (with `:`, `,` or a line break, starting with `#`, or with spaces at
  either end) are skipped and logged.
  - *A custom Java group provider that calls Metabase*: maintaining Java against Trino's plugin interface.
  - *The `ldap` group provider*: needs a directory (see above). *Ranger*: a whole platform. Too heavy.
  - *Sending groups in a request header*: Trino has no such header, and anyone who could reach Trino could
    claim to be an admin.
  - *Looking groups up from OPA* (`http.send`): the chart's OPA sidecar can't receive secrets, and it puts
    lookup logic in the policy.
- **The Metabase sync only reads.** If a run fails, Trino keeps the last file. The worst case is stale or
  missing groups, which denies access. It never changes a Metabase account.
- **Groups keep their Metabase names in Trino.** No mapping file to keep in step with Metabase. Renaming a
  group in Metabase takes away its access until `rules.rego` uses the new name, which fails safe. All Users
  is left out, so a person with no role reads nothing.
- **The admin page asks OPA, and warns instead of guessing.** It never reads the rules to decide access, or
  which rule applies: OPA does both, so the page can't drift from Trino. The page shows every catalog in the
  union of Trino's, Metabase's and the rules', not only the ones that look wrong, so a gap is visible. A failed
  lookup is its own state, so a broken OPA can't make the page look fine.
  - *Merging duplicate Metabase connections to one catalog*: hides a misconfiguration. They are listed together
    with a warning that says which one the columns came from.
  - *Giving the web pod the catalog files*: they hold passwords. It gets the names only (`TRINO_CATALOGS`).
  - *Still listing the tables of a connection that bypasses the proxy*: those queries run as Metabase's own
    Trino user, so the rules aren't what decides them and the page would mislead. They are warned about instead.
  - *A web-only policy file for `deciding_keys`*: a custom policy would then lack `most_specific_key` there, and
    not compile.
- **The web port is on loopback.** In Kubernetes the pod listens on loopback behind oauth2-proxy. Compose publishes
  the port on `127.0.0.1` only, since without oauth2-proxy anyone who reached it could send any
  `X-Forwarded-Email`.
- **The web server sends security headers on every response.** A Content-Security-Policy allows only the site's
  own scripts, styles and requests (the built pages have nothing inline) and no framing; with it go
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: same-origin` and
  `Cross-Origin-Opener-Policy: same-origin`. The JSON answers are `Cache-Control: no-store`. HSTS is left to the
  ingress, where TLS ends.
- **Names with "." are for Administrators only.** Rules keys join names with ".", which can't tell a dotted
  name from two names. Matching them anyway could let a column's rule grant a whole table of the same text.
  - *Escaping or quoting in keys*: more syntax in every key for names nobody is expected to use.
- **The proxy asks Metabase about every query, with no cache.** Metabase assigns the IDs, so it's the only
  source that's always right, and it reports deactivated users. The lookup is a local call inside the pod. A
  cache can be added if it's ever shown to be needed. Without one, deactivation is immediate.
- **The proxy runs as a sidecar in the Metabase pod, listening only on localhost.** Metabase is its only
  client, and it needs no Service.
- **Only Metabase can reach Trino.** Trino believes the `X-Trino-User` header from any client, so the
  identities OPA sees are only as trustworthy as Trino's network access. There is no public Trino ingress,
  and a NetworkPolicy lets only the Metabase pod and Trino's own pods connect.
- **The proxy reads the user ID only from the query's last line.** Metabase appends its comment last.
  Anything before it is SQL the person may have written, including a forged comment.
- **The Metabase API key comes from a Kubernetes Secret.** The proxy and the Metabase sync both use it. Compose
  shares the key setup creates through a volume.
- **The access rules are Rego, edited by pull request.** Rego allows comments, so each rule can say why it
  exists, and pull requests give a reviewed history of who could see what. A generator or admin UI can come
  later if editing the file by hand becomes a burden.
  - *A JSON permissions matrix read by generic Rego* (the previous version): it listed every table and
    column, which was more to maintain and harder to read than the rules themselves.
  - *Defining the rules in Metabase and syncing them to Trino*: per-group column rules are a paid Metabase
    feature, OSS can't express a mask, and raw SQL bypasses Metabase's rules anyway. Metabase's table
    permissions answer a different question (may this group build queries on a table in Metabase?), and
    `access.sh` sets them for usability, not security.
- **Columns are hidden, not denied.** A denied column makes the whole query fail, and Metabase doesn't retry
  without it, so any shared card using it would break for that group. Hiding happens before filters,
  group-bys and joins, so dashboards for restricted roles shouldn't filter or group by hidden columns. Give
  each audience its own curated models instead.
- **New columns are visible wherever their table is.** This keeps the rules short: a table entry covers its
  columns. The cost is that a new sensitive column added upstream is visible straight away to every group
  that can read its table. To limit that, grant restricted groups (Teachers) single tables, not schemas, and
  keep tests that the known sensitive columns stay hidden. If that isn't enough, a per-table "hide new
  columns" setting would be the next step.
- **Metabase's result caching stays off.** Its cache is keyed by the query, not the user, so one role could
  see results cached from another role's run.
- **Batch column masking.** Trino asks OPA for all of a query's column masks in one request instead of one
  per column. The Trino chart has no setting for that endpoint, so Helm writes Trino's access-control
  properties by hand.
- **Many users are added with an import script, not the Metabase UI.** The UI is fine for a few admins. For
  more, a script reads a CSV (`email, first name, last name, role`) and calls the Metabase API, prints its
  plan first, only adds, and is safe to rerun. Not built yet.
- **One image for the proxy and the Metabase sync, on Alpine.** They share the Metabase client code, and one
  image means one build and one tag. Alpine rather than distroless so the sidecar's startup probe and an
  admin debugging access can use a shell (`cat /groups/group.txt` shows exactly what Trino sees).

## Tests

- `opa test` (CI, no stack needed):
  - `helm/opa/tests/policy_test.rego`: how rules are applied, against made-up rules (most specific entry
    wins, column hiding, read-only, the connection user, metadata tables). `dotted_names_test.rego` and
    `deciding_keys_test.rego` cover names with "." and the admin page's `deciding_keys`.
  - `opa/rules_test.rego`: what the sample rules file promises, written by hand.
- `go test` in `trino/identity` (in the image build): the proxy's handler and user-ID parsing, and the group,
  schema and status files the sync writes from Metabase's responses.
- `go test` in `web`: the admin page's document, its cell states and its warnings, against a fake OPA.
- Postman, per role (admin, analyst, teacher): every dashboard card each role can see succeeds, and
  `student_courses.grade` is hidden for teachers but not analysts. A role whose groups didn't reach Trino
  fails these.

## Implementation

Built as a stack of small pull requests.

### Next: Google sign-in
- [ ] Create a Google OAuth client in our own Google Cloud project, with scopes `openid` and `email`, and move
      it out of "testing" mode before real users arrive.
- [ ] Metabase: enable Google sign-in and **turn off automatic account creation**, so only people an admin
      has added can get in.
- [ ] Add the first admins in Metabase and put them in Administrators.

### Later (not scheduled)
- The user import script, once there is a real list of users to bring over.
- Row-level security, e.g. a teacher sees only their own students. It needs data we don't have yet (which
  school a principal is at, which students a teacher has). Trino only passes OPA the user and their groups,
  so that data would have to be a table Trino can read or data loaded into OPA.
- Direct Trino access for analysts: Trino OAuth2 with Google (web UI and CLI, needs HTTPS). Metabase would
  then sign in as a service account, and the proxy's `X-Trino-User` becomes impersonation, which needs an
  OPA impersonation rule. At that point the proxy should stop overriding `X-Trino-Original-User`.

## Open questions

- Confirm on Metabase v0.63 OSS: Google sign-in can be used with automatic account creation turned off. This
  design relies on it: with automatic creation on, anyone with a Google account in an allowed domain gets in.
- Cluster: whether the CNI enforces NetworkPolicy. Without it, any pod can reach Trino and claim to be anyone.
