#!/bin/sh
# Creates or updates the dashboards in manifest.json on a running Metabase, such as one deployed by the chart.
# Safe to run again: collections, models, questions and dashboards are matched by name and updated in place.
#
#   METABASE_URL     where Metabase answers, e.g. http://localhost:3000 through kubectl port-forward
#   API_KEY          an admin API key (the one in the metabase-api-key Secret works)
#   ADMIN_SESSION    the metabase.SESSION cookie of someone in the Administrators group, copied from the
#                    browser after signing in. Queries need it because Trino's access policy only answers
#                    people with a group, and the API key's user has none.
#
# The Metabase database named in manifest.json ("trino") must exist already; see dashboards/README.md.
set -eu

DIR=$(dirname "$0")

for var in METABASE_URL API_KEY ADMIN_SESSION; do
    eval "value=\${$var:-}"
    if [ -z "$value" ]; then
        echo "deploy-dashboards: $var is not set (see the comment at the top of this file)" >&2
        exit 1
    fi
done
export METABASE_URL API_KEY ADMIN_SESSION

sh "$DIR/dashboards/dashboards.sh"
sh "$DIR/dashboards/access.sh"
echo "deploy-dashboards: done"
