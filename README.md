# Metrino

Metrino (Metabase + Trino) puts Metabase dashboards on top of Trino, with per-user access rules enforced in Trino by OPA.

## Deployment
Trino has no ingress. Only the Metabase pod can reach it, inside the cluster, because Trino believes whatever user name a client sends.

The chart's settings, and how to add catalogs and plugins or write a values file for your cluster, are in [docs/deployment.md](docs/deployment.md).

These sections assume you can access your cluster via `kubectl`. Set up a [kubeconfig](https://kubernetes.io/docs/concepts/configuration/organize-cluster-access-kubeconfig/) if you have not already.

### Installing
Write a values file for your cluster (see [docs/deployment.md](docs/deployment.md)) and a rules file for who can read what (see `opa/rules.rego` for the format), then:

```
helm dependency build ./helm
helm upgrade --install metrino ./helm -n metrino --create-namespace \
  -f <your-values.yaml> \
  --set-file accessPolicy.rules=<your-rules.rego> \
  --set trino.identityImage.tag=<tag>
```

Also set `sampleConnectors.image.tag` and `web.image.tag` if you turn those on.

### Metabase API key
The reverse proxy (a sidecar in the Metabase pod) uses a Metabase API key to look up which user ran each query, and the Metabase sync (a sidecar in the Trino coordinator pod) uses it to read users and groups. The web frontend's pod (when `web.enabled` is true) runs its own Metabase sync with it too. Each reads the key from the `metabase-api-key` Secret, and none of these pods starts until that Secret exists. Create it by hand before the first deploy:

```
kubectl create secret generic metabase-api-key -n metrino \
  --from-literal=api-key=<metabase-api-key>
```

Create the key in Metabase under Admin > Settings > Authentication > API keys, in the Administrators group. On a fresh install Metabase isn't running yet, so create the Secret with a placeholder value first. Until the real key is in place, queries through the proxy fail. Once Metabase is up, create the key, replace the placeholder (also how to rotate the key), and restart Metabase, the Trino coordinator and, where the web frontend is on, the web pod so the proxy and the Metabase syncs read it. A Metabase sync that can't read Metabase with its old key keeps the last group file, so a pod that isn't restarted serves stale groups:

```
kubectl create secret generic metabase-api-key -n metrino \
  --from-literal=api-key=<metabase-api-key> --dry-run=client -o yaml | kubectl apply -f -
kubectl rollout restart deployment/metabase-deployment -n metrino
kubectl rollout restart deployment/metrino-trino-coordinator -n metrino
kubectl rollout restart deployment/web -n metrino
```

### Metabase's Trino connection
Metabase must reach Trino through the reverse proxy, so that each query runs in Trino as the Metabase user who ran it. Trino's access policy gives the connection's own user (`trino`) metadata only, so if the connection still points straight at Trino, every dashboard query is denied.

The connection is stored in Metabase's database, not in the Helm chart, so set it by hand once after the first deploy that includes the proxy. In Metabase, go to Admin > Databases > trino and change only:

- Host: `127.0.0.1`
- Port: `9090` (the chart's `metabase.reverseProxyPort`; use its value if you changed it)
- Optimized prepared statements: on

The last one matters for any query with a parameter, such as a dashboard filter or a `{{variable}}` in a SQL question. With it off, Trino's JDBC driver sends such a query as a `PREPARE` followed by a separate `EXECUTE`, and only the `PREPARE` carries the Metabase user, so the proxy refuses the `PREPARE` with an error telling you to turn this on. With it on, the query and its user go in one `EXECUTE IMMEDIATE` request.

Leave everything else as it is: Username `trino` (the user the policy expects for Metabase's metadata queries), the catalog, and SSL off. Use `127.0.0.1` rather than `localhost`: the proxy listens only on IPv4 loopback, and `localhost` can also resolve to `::1`.

To check it worked, open a dashboard and confirm its cards load. Then look for the proxy's lookups in its logs:

```
kubectl logs deploy/metabase-deployment -c trino-reverse-proxy -n metrino
```

Each query should log a line like `Metabase user 7 is alice@example.com`.

Every Trino connection in Metabase must use this address. The admin page (see Web frontend) compares each connection's host and port with the proxy's, and for one that doesn't match it warns that the connection bypasses the proxy and leaves its tables out.

Each Metabase database reads one Trino catalog. To use the sample data sources (`sampleConnectors.enabled`), add a database for it under Admin > Databases > Add database: Starburst, Host `127.0.0.1`, Port `9090` (as above), Username `trino`, SSL off, Optimized prepared statements on, and Catalog `example_jdbc`. Add another with Catalog `openapi` for the HTTP source.

### Users and roles
Users and their roles live in Metabase (see `docs/identity-and-access.md`). An admin adds each person under Admin > People, with the email they sign in with, and puts them in the group for their role. The Metabase sync copies every group except All Users to Trino every 30 seconds, under the group's Metabase name:

Administrators read everything. Every other group reads what the deployment's rules file allows. With compose's sample rules (`opa/rules.rego`):

| Metabase group | Data in compose |
|---|---|
| Administrators | everything |
| Analysts | all the sample data, and raw SQL in Metabase |
| Teachers | district tables, without student names, contact details or grades |

Once, create the **Analysts** and **Teachers** groups in Metabase under Admin > People > Groups (Administrators is built in). Group names must match the access rules exactly, case and spaces included. A group the rules don't name reads no data, so a misspelled or renamed group denies access rather than granting it.

To check the sync, read its log. Each change to the groups logs a line with the number of users per group:

```
kubectl logs deploy/metrino-trino-coordinator -c metabase-sync -n metrino
```

It should show `metabase-sync: wrote /groups/group.txt (Administrators: 1, Analysts: 1, Teachers: 1)`. Deactivating someone in Metabase stops their queries straight away (the proxy checks Metabase on every query) and removes them from the next file.

To change what a group can read, edit your deployment's rules file (`opa/rules.rego` for compose) and update its tests to match. The comment at the top of `rules.rego` explains the format, and `docs/identity-and-access.md` has examples.

### Web frontend
The docs and admin website (`web/`) shows docs to anyone in a Metabase group and the admin pages to Administrators. On a cluster people sign in with Google through oauth2-proxy, and the web server trusts the email it passes in `X-Forwarded-Email`. In compose there is no oauth2-proxy: the site is at `localhost:8000`, and you set the header yourself, e.g. `curl -H 'X-Forwarded-Email: admin@example.com' localhost:8000/admin/`. Compose publishes the port on `127.0.0.1` only, since anyone who could reach it could send any `X-Forwarded-Email`. The compose web OPA doesn't watch its files, so after changing `helm/opa/policy.rego` or `opa/rules.rego`, run `docker compose restart web-opa`. What the admin access page shows and warns about is in `docs/identity-and-access.md`.

#### Deploying
The web pod needs two things before the first deploy that includes it. Without the Secret, the pod doesn't start.

1. Create a Google OAuth client in the same Cloud project as Metabase's, with the redirect URI `https://<web-host>/oauth2/callback`, where `<web-host>` is the chart's `web.host`.
2. Create the Secret oauth2-proxy reads, with the client's ID and secret:

```
kubectl create secret generic web-oauth2-proxy -n metrino \
  --from-literal=client-id=<id> --from-literal=client-secret=<secret> \
  --from-literal=cookie-secret="$(openssl rand -base64 32 | tr -- '+/' '-_')"
```

The pod's Metabase sync reads the `metabase-api-key` Secret too, so restart the web pod after rotating that key (see Metabase API key).

To check it worked, sign in at `https://<web-host>`, then read each container's log. The Metabase sync logs the same line as the coordinator's, and oauth2-proxy logs each sign-in:

```
kubectl logs deploy/web -c metabase-sync -n metrino
kubectl logs deploy/web -c oauth2-proxy -n metrino
kubectl logs deploy/web -c web -n metrino
kubectl logs deploy/web -c opa -n metrino
```

Then check that nobody can pass themselves off as an admin. The web server believes `X-Forwarded-Email`, and oauth2-proxy must replace any value the client sends. Sign in as someone in a Metabase group other than Administrators (Teachers, say) and copy the session cookies from the browser: every cookie whose name starts with `_oauth2_proxy` (a long session is split into `_oauth2_proxy_0`, `_oauth2_proxy_1` and so on), written as `name=value; name=value`. First send the cookies alone, to show it works and that the person has a group:

```
curl -s -o /dev/null -w '%{http_code}\n' --cookie '<cookies>' https://<web-host>/
```

It must answer 200. Anything else (302, 401, 403) means the cookies weren't accepted or the person has no group, so the next request proves nothing; sign in again and redo this one. Then forge the header with the email of a real Administrator (someone who would see `/admin/` if the forgery worked):

```
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Forwarded-Email: <administrator-email>' --cookie '<cookies>' https://<web-host>/admin/
```

It must answer 403. A 200 means the forged header got through; scale the web Deployment to zero until it's fixed:

```
kubectl scale deployment/web --replicas=0 -n metrino
```
