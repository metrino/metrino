# Metabase dashboards

The dashboards are defined as files in this folder and created through the Metabase API, so every
environment gets the same ones. Nothing is exported from the Metabase UI.

## What gets created

| Collection         | Visible to  | Contents                                                                                     |
| ------------------ | ----------- | -------------------------------------------------------------------------------------------- |
| Admin Dashboards   | admins only | District at a glance (counts, top schools, staff per department, enrollment, graduation year) |
| Analyst Dashboards | all users   | Enrollment and staffing explorer, with School and School type filters                        |
| Data Models        | all users   | 7 curated models (no names, emails or phone numbers) for the query builder                   |

## Access

- **All Users** can open dashboards and ask questions on the models. No raw tables, no raw SQL.
- **Analysts** (group) can also write raw SQL. Add members under Admin > People.
- Result caching is off, because the cache is shared by everyone who runs the same query.
- Metabase only decides who may write raw SQL. Which tables that SQL can read is enforced by Trino and OPA.

## How it works

`setup.sh` (compose only) creates the admin, the Trino connection and the test users, then runs:

1. `dashboards.sh`: reads `manifest.json` and, through the Metabase API, creates the collections, the
   models, one question per SQL file in `sql/`, and the dashboards with their layout and filters.
2. `access.sh`: applies the access rules above.

Both scripts can be run again on an existing Metabase. Items are matched by name and updated in place,
so edits made to them in the UI are overwritten. Items removed from the manifest are not deleted.

## Local

`docker compose up -d --build` creates everything. `docker compose down -v` resets it.
Log in at http://localhost:8080 as `admin@example.com`, `analyst@example.com` or `teacher@example.com`
(password `q1q1q1q1`, from `metabase/test-users.json`).

## Kubernetes

Deploying the chart does not deploy the dashboards. Run `metabase/deploy-dashboards.sh`, and again after
the manifest or SQL changes.

Once, as an administrator, add the database the manifest names: Admin > Databases > Add database,
Starburst, name `trino`, host `127.0.0.1`, port `9090`, user `trino`, SSL off, Optimized prepared statements
on, catalog `example_jdbc`.

```bash
kubectl port-forward deploy/metabase-deployment 3000:3000 -n metrino &

export METABASE_URL=http://localhost:3000
export API_KEY=$(kubectl get secret metabase-api-key -n metrino -o jsonpath='{.data.api-key}' | base64 -d)
export ADMIN_SESSION=<metabase.SESSION cookie of an administrator>

sh metabase/deploy-dashboards.sh
```

`ADMIN_SESSION` is needed because the models are run once to save their columns, and Trino only answers
users that are in a group.

## Files

```
manifest.json   collections, models, dashboards, layout, filters
dashboards.sh   creates them from the manifest
access.sh       sets who may write raw SQL, turns off caching
sql/            one .sql file per tile (admin/, analyst/) and per model (models/)
```

## Adding a tile or dashboard

1. Write the query (Trino SQL, tables as `public.<table>`) and save it under `sql/admin/` or `sql/analyst/`.
2. Add a card for it in the dashboard's `cards` in `manifest.json`.
3. Run the scripts again.

### manifest.json fields

| Field                            | Where             | Meaning                                                                                  |
| -------------------------------- | ----------------- | ---------------------------------------------------------------------------------------- |
| `database`                       | top level         | Metabase database the queries run against (`trino`)                                      |
| `key`, `name`, `description`     | collection        | `key` is how other entries refer to it                                                   |
| `all_users`                      | collection        | `none` (admins only) or `read`                                                           |
| `collection`                     | dashboard, model  | `key` of the collection it goes in                                                       |
| `models`                         | top level         | `name`, `description`, `collection`, `sql` (file under `sql/models/`)                    |
| `groups`                         | top level         | Groups that may write raw SQL                                                            |
| `parameters`                     | dashboard         | Filters: `id`, `name`, `type`, optional `values_from_model` and `values_column`          |
| `sql`, `display`                 | card              | SQL file, and chart type (`scalar`, `bar`, `row`, `line`, `table`)                       |
| `settings`                       | card              | Chart settings; set `graph.dimensions` and `graph.metrics` for charts with a number axis |
| `filters`                        | card              | Filter `id`s the card responds to                                                        |
| `row`, `col`, `size_x`, `size_y` | card              | Position and size on a 24-column grid                                                    |

A card that uses a filter needs an optional clause in its SQL with a variable named like the filter id:

```sql
WHERE 1 = 1
  [[AND s.school_name = {{school}}]]
```

## Data source

The tiles query the `trino` connection, whose catalog is `example_jdbc`: Postgres filled from the sample
CSVs in `sample-connectors/`. The SQL has no catalog name, so another catalog with the same tables can be
used by changing `CATALOG` in `compose.yaml`.
