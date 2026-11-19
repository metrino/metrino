#!/bin/sh
# Decides who may write raw SQL and who may query the raw tables, and turns off Metabase's result cache.
# Called from setup.sh after dashboards.sh; needs $API_KEY (admin).
#
#   - Everyone (All Users): dashboards and the curated models only. No raw SQL, no raw tables.
#   - Groups listed under "groups" in manifest.json: also raw SQL (and so the raw tables).
#
# Metabase only controls WHO may write native SQL. A native query can still reach every catalog the
# Trino user can read; limiting that is Trino's job (OPA / access control), not Metabase's.
set -eu

BASE=${METABASE_URL:-http://metabase:3000}
DIR=$(dirname "$0")
MANIFEST="$DIR/manifest.json"
TMP=$(mktemp -d)

# api METHOD PATH [JSON_BODY]: prints the response body, fails loudly on an HTTP error.
api() {
    if [ $# -ge 3 ]; then
        curl -sS --fail-with-body -X "$1" "$BASE$2" \
            -H 'Content-Type: application/json' -H "X-API-Key: $API_KEY" --data "$3"
    else
        curl -sS --fail-with-body -X "$1" "$BASE$2" -H "X-API-Key: $API_KEY"
    fi
}

DB_NAME=$(jq -r .database "$MANIFEST")
DB_ID=$(api GET /api/database | jq -r --arg n "$DB_NAME" '.data[] | select(.name == $n) | .id')
if [ -z "$DB_ID" ]; then
    echo "access: Metabase database '$DB_NAME' not found" >&2
    exit 1
fi

# The raw tables only exist in Metabase after the first sync, so wait for it.
tries=0
until [ "$(api GET "/api/database/$DB_ID" | jq -r .initial_sync_status)" = "complete" ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge 40 ]; then
        echo "access: database '$DB_NAME' did not finish syncing" >&2
        exit 1
    fi
    sleep 3
done
# Hidden tables are left out unless asked for, and a rerun would find none.
api GET "/api/database/$DB_ID/metadata?include_hidden=true" > "$TMP/metadata.json"
SCHEMAS=$(jq -c '[.tables[].schema] | unique' "$TMP/metadata.json")

# set_query_access GROUP_ID LEVEL: LEVEL is "query-builder-and-native" (raw SQL), or "none" which
# switches off querying the raw tables in every schema while models and dashboards keep working.
set_query_access() {
    graph=$(api GET /api/permissions/graph | jq -c --arg g "$1" --arg d "$DB_ID" --arg l "$2" --argjson s "$SCHEMAS" '
        .groups[$g][$d]["view-data"] = "unrestricted"
        | .groups[$g][$d]["create-queries"] =
            (if $l == "none" then ($s | map({(.): "no"}) | add) else $l end)')
    api PUT /api/permissions/graph "$graph" > /dev/null
}

# Everyone (group 1, "All Users"): no raw SQL, no raw tables.
set_query_access 1 none
echo "access: All Users -> dashboards and models only"

# Groups that may also write raw SQL. setup.sh has created them. Their members are managed in Metabase
# (Admin > People).
api GET /api/permissions/group > "$TMP/all-groups.json"
jq -c '.groups[]?' "$MANIFEST" > "$TMP/groups.jsonl"
while read -r group; do
    name=$(echo "$group" | jq -r .name)
    group_id=$(jq -r --arg n "$name" '.[] | select(.name == $n) | .id' "$TMP/all-groups.json")
    if [ -z "$group_id" ]; then
        group_id=$(api POST /api/permissions/group "$(jq -nc --arg n "$name" '{name: $n}')" | jq -r .id)
        echo "access: created group '$name'"
    fi
    set_query_access "$group_id" query-builder-and-native
    echo "access: '$name' -> raw SQL"
done < "$TMP/groups.jsonl"

# No result caching: the cache is keyed by the query, not the user, so one role could be
# served results cached from another role's run. OSS has one policy for all databases, the root one.
# Metabase caches nothing without a policy today; "nocache" is set explicitly so a new default can't turn
# it on, then read back.
api PUT /api/cache '{"model": "root", "model_id": 0, "strategy": {"type": "nocache"}}' > /dev/null
policies=$(api GET /api/cache)
if [ "$(echo "$policies" | jq -c '[.data[] | {model, type: .strategy.type}]')" != '[{"model":"root","type":"nocache"}]' ]; then
    echo "access: expected only the root cache policy, set to nocache; got $policies" >&2
    exit 1
fi
echo "access: result caching off"

rm -rf "$TMP"
