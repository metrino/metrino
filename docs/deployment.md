# Deploying the chart

The Helm chart in `helm/` deploys Metabase, its PostgreSQL database and Trino. Trino comes from the
[Trino chart](https://github.com/trinodb/charts), so everything under `trino:` in a values file is that
chart's setting.

## Values files

`helm/values.yaml` holds defaults that work on most clusters. Trino uses the official `trinodb/trino` image,
and there is no ingress. Write your own values file for your cluster, with at least your catalogs and, if you
want one, an ingress:

```yaml
ingress:
  enabled: true
  host: metabase.example.org
```

Trino runs the official `trinodb/trino` image. To use another, set `trino.image` (the Trino chart's setting).

Every cluster also needs:

- **The identity image** (`trino/identity`), which runs the reverse proxy next to Metabase. Set
  `trino.identityImage.tag`, and point `trino.identityImage` at your own registry if you build it yourself.
  If the registry needs credentials, add a pull secret under `metabase.imagePullSecrets`. The chart won't
  render without the tag.
- **The `metabase-api-key` Secret**, created by hand as the README describes. Neither the Metabase pod nor
  the Trino coordinator (whose Metabase sync uses it) starts without it.

## Adding a catalog

Each entry under `trino.catalogs` becomes a catalog properties file. Keep passwords in a Secret and refer to
them with Trino's `${ENV:...}` syntax:

```yaml
trino:
  catalogs:
    district: |
      connector.name=postgresql
      connection-url=jdbc:postgresql://district-db:5432/district
      connection-user=trino_reader
      connection-password=${ENV:DISTRICT_DB_PASSWORD}
  envFrom:
    - secretRef:
        name: district-db # a Secret with a DISTRICT_DB_PASSWORD key, created by hand
```

The Trino chart adds its `tpch` and `tpcds` test catalogs unless you set them to `null`:

```yaml
trino:
  catalogs:
    tpch: null
    tpcds: null
```

## Sample data sources

`sampleConnectors.enabled` deploys the sample data sources from `sample-connectors/`, the same synthetic data
compose runs: `example-jdbc`, a Postgres database for the `example_jdbc` catalog, and `example-http`, an HTTP
server for the `openapi` catalog. The Services have the same names as compose's hosts, so the catalog files in
`trino/catalog` work unchanged. A NetworkPolicy lets only Trino's pods reach them. The data is built into the
example-jdbc image, so the database starts empty and reloads it on every start; nothing is persisted.

Turning them on also takes their images and their catalogs, plus the openapi plugin on every Trino pod (see
Plugins). Build the images from `sample-connectors/` and set `sampleConnectors.image.tag`; the chart won't
render without it.

## Access rules

Trino asks an OPA sidecar about every query. The chart ships the policy, `helm/opa/policy.rego`: read-only
for everyone, Administrators read everything, and everyone else reads what the deployment's rules file lists.
The rules file is Rego in `package trino` that defines `rules` (see `opa/rules.rego`, the sample, for the
format). Pass yours with:

```
helm upgrade ... --set-file accessPolicy.rules=path/to/rules.rego
```

Without a rules file, only Administrators read data, so leaving the flag off a deploy takes everyone else's
access away until the next deploy that passes it.

OPA loads the rules when the coordinator pod starts. A deploy that changes an image tag starts new pods. After
one that keeps the same tags, restart the coordinator so the new rules apply:

```
kubectl rollout restart deployment/metrino-trino-coordinator -n metrino
```

To bring your own policy instead, create a ConfigMap holding your `.rego` files and set:

```yaml
accessPolicy:
  create: false
trino:
  opa:
    policyConfigMap: my-policy
```

The chart then ignores `accessPolicy.rules`, so put your rules in that ConfigMap too.

## Web frontend

`web.enabled` deploys the docs and admin website (`web/`) in its own pod: oauth2-proxy for Google sign-in, the
web server, a Metabase sync and a read-only OPA. It is off unless `web.enabled` is true, and then `web.host` is
required. The settings under `web:`:

- `host`: the hostname it is served on. Its ingress needs a valid certificate for this name. A wildcard
  certificate covers only names one level down: `*.example.org` covers `metrino-web.example.org` but not
  `web.metrino.example.org`.
- `image`: the web server's image, built from `web/`. `image.tag` defaults to the chart's appVersion; point `image.registry` and
  `image.repository` at your own registry if you build it yourself.
- `metabaseLinkUrl`: where the header's Metabase tab points, e.g. the Metabase ingress. The admin page's links
  to Metabase's admin pages (connection settings, table metadata) start there too. It is only a link for
  people's browsers, and the pod's own Metabase sync reaches Metabase another way. Leave it empty for no tab
  and no links.
- `imagePullSecrets` and `ingressAnnotations`: as for Metabase's ingress.
- `emailDomains`: which Google accounts may sign in, one `--email-domain` for oauth2-proxy each. `["*"]`, the
  default, lets any account sign in; the web server shows nothing to anyone without a Metabase group.
- `oauth2Proxy.image` and `oauth2Proxy.secretName`: the oauth2-proxy image, and the Secret with `client-id`,
  `client-secret` and `cookie-secret`, created by hand as the README describes. The pod doesn't start without it.

Administrators also see Prometheus on the site, at `/prometheus/` (the header's Prometheus tab). The web server
passes those requests to the Prometheus server's Service, without the person's session cookie or email, and passes
on POSTs only to the query API, never to `/-/quit` or `/-/reload`. Prometheus has no ingress of its own, and a
NetworkPolicy (`helm/templates/prometheus-network-policy.yaml`) admits only the web pod, so without the site
nobody reaches it. Only connections to Prometheus are restricted: it still scrapes Metabase and Trino. The path
goes to Prometheus unchanged, so `helm/values.yaml` serves Prometheus under `/prometheus` too
(`prometheus.server.prefixURL` and `baseURL`, and the self-scrape's `metrics_path`). The chart refuses to render
the site if `prefixURL` isn't `/prometheus` or `baseURL` doesn't end in it.

The pod's Metabase sync and OPA sidecars repeat the coordinator's settings: the sync is configured like the one in
`trino.initContainers` in `helm/values.yaml`, and the OPA reads the same policy ConfigMap
(`trino.opa.policyConfigMap`). If you change either there, change `helm/templates/web.yaml` to match. The pod also needs the `metabase-api-key` Secret.

Three things reach the web pod from elsewhere in the values, so there is nothing to set under `web:` for them:

- **`metabase.reverseProxyPort`** (default 9090) is the port the reverse proxy listens on, on loopback in
  Metabase's pod. The proxy's `REVERSE_PROXY_PORT` and the web pod's sync's `TRINO_PROXY_ADDRESSES`
  (`127.0.0.1:<port>,localhost:<port>`) both come from it, so they can't disagree. The sync uses the addresses to
  tell which of Metabase's Trino connections go through the proxy. Metabase's own connection is set by hand
  (see the README), so after changing the port, change the connection too, or the admin page warns that it
  bypasses the proxy.
- **`TRINO_CATALOGS`**, the names of Trino's catalogs, is set automatically from `trino.catalogs` (and the
  deprecated `trino.additionalCatalogs`), the Trino chart's own `tpch` and `tpcds` included unless you set them
  to `null`. Only the names go to the web pod. The catalogs' properties hold passwords, so they never do.
- **`STATUS_FILE`**, where the sync records how its runs are going, is set by the chart. The admin page reads it
  to warn when the groups or the column list stop updating.

## Plugins

Connector plugins that don't ship with Trino are not in the image. Each one goes on a volume mounted at
`/usr/lib/trino/plugin/<name>` on the coordinator and on every worker, through the Trino chart's
`coordinator.additionalVolumes`, `worker.additionalVolumes` and their `additionalVolumeMounts`.

There are two ways to fill the volume:

- **Download it when the pod starts.** An init container downloads a pinned release into an `emptyDir`
  and checks its sha256. The pods need to reach the download host.
- **A PersistentVolumeClaim you fill yourself,** for plugins you build or can't download. Copy the
  plugin's jars onto the volume, then mount it read-only at `/usr/lib/trino/plugin/<name>`. If workers
  run on more than one node, the volume needs the ReadOnlyMany or ReadWriteMany access mode.

Helm replaces lists rather than merging them. A values file that sets `coordinator.additionalVolumes`,
`additionalVolumeMounts` or `initContainers` replaces the entries `helm/values.yaml` sets there, so copy
those in too.
