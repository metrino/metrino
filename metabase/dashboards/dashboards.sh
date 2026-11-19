#!/bin/sh
# Creates the collections, questions and dashboards described in manifest.json, or updates them in place
# when they already exist (matched by name), so it is safe to run again against a live Metabase.
# Called from setup.sh once the Trino connections exist, or by deploy-dashboards.sh against another
# instance. Needs $API_KEY (admin) and $ADMIN_SESSION, the
# session of a Metabase user in the Administrators group, for the queries (OPA denies users without groups,
# and the API key's user has none).
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

# find_item COLLECTION_ID TYPE NAME: id of the item called NAME in the collection, or nothing.
# TYPE is card (question), dataset (model) or dashboard.
find_item() {
    api GET "/api/collection/$1/items?models=$2" | jq -r --arg n "$3" '[.data[] | select(.name == $n) | .id][0] // empty'
}

# upsert_card COLLECTION_ID TYPE NAME BODY: updates the card with that name, or creates it; prints its id.
upsert_card() {
    existing=$(find_item "$1" "$2" "$3")
    if [ -n "$existing" ]; then
        api PUT "/api/card/$existing" "$4" > /dev/null
        echo "$existing"
    else
        api POST /api/card "$4" | jq -r .id
    fi
}

if [ -z "${ADMIN_SESSION:-}" ]; then
    echo "dashboards: ADMIN_SESSION is not set" >&2
    exit 1
fi

# Metabase database ids differ between instances, so look it up by name.
DB_NAME=$(jq -r .database "$MANIFEST")
DB_ID=$(api GET /api/database | jq -r --arg n "$DB_NAME" '.data[] | select(.name == $n) | .id')
if [ -z "$DB_ID" ]; then
    echo "dashboards: Metabase database '$DB_NAME' not found" >&2
    exit 1
fi

# Collections. COLLECTION_IDS maps manifest key -> Metabase collection id.
COLLECTION_IDS='{}'
jq -c '.collections[]' "$MANIFEST" > "$TMP/collections.jsonl"
while read -r coll; do
    key=$(echo "$coll" | jq -r .key)
    body=$(echo "$coll" | jq -c '{name, description}')
    name=$(echo "$coll" | jq -r .name)
    id=$(api GET /api/collection | jq -r --arg n "$name" '[.[] | select(.name == $n and .location == "/" and .archived != true) | .id][0] // empty')
    if [ -z "$id" ]; then
        id=$(api POST /api/collection "$body" | jq -r .id)
    else
        api PUT "/api/collection/$id" "$body" > /dev/null
    fi
    COLLECTION_IDS=$(echo "$COLLECTION_IDS" | jq -c --arg k "$key" --argjson i "$id" '.[$k] = $i')

    # Group 1 is "All Users". New collections default to write access for them, so always
    # set the level explicitly ("none" keeps a collection admin-only).
    access=$(echo "$coll" | jq -r '.all_users // "none"')
    graph=$(api GET /api/collection/graph | jq -c --arg c "$id" --arg a "$access" '.groups["1"][$c] = $a')
    api PUT /api/collection/graph "$graph" > /dev/null
    echo "dashboards: collection '$key' -> $id (All Users: $access)"
done < "$TMP/collections.jsonl"

# Models: curated datasets (SQL without sensitive columns) that people build questions on.
# MODEL_IDS maps model name -> card id (dashboard filters can take their dropdown values from a model).
MODEL_IDS='{}'
jq -c '.models[]?' "$MANIFEST" > "$TMP/models.jsonl"
while read -r model; do
    coll_id=$(echo "$COLLECTION_IDS" | jq -r --arg k "$(echo "$model" | jq -r .collection)" '.[$k]')
    sql=$(cat "$DIR/sql/$(echo "$model" | jq -r .sql)")
    model_body=$(echo "$model" | jq -c --arg sql "$sql" --argjson db "$DB_ID" --argjson c "$coll_id" '{
        name,
        description,
        type: "model",
        display: "table",
        collection_id: $c,
        visualization_settings: {},
        dataset_query: {type: "native", database: $db, native: {query: $sql, "template-tags": {}}}
    }')
    model_id=$(upsert_card "$coll_id" dataset "$(echo "$model" | jq -r .name)" "$model_body")
    # A model created through the API has no column metadata. Run it once and save the columns,
    # otherwise its columns are missing in the query builder and it cannot feed a filter dropdown.
    # Metabase answers 202 even when the query failed, so check the status.
    curl -sS --fail-with-body -X POST "$BASE/api/card/$model_id/query" \
        -H "X-Metabase-Session: $ADMIN_SESSION" > "$TMP/result.json"
    if ! jq -e '.status == "completed" and .data.results_metadata.columns != null' "$TMP/result.json" > /dev/null; then
        echo "dashboards: model '$(echo "$model" | jq -r .name)' query failed: $(jq -r '.error // .status' "$TMP/result.json")" >&2
        exit 1
    fi
    meta=$(jq -c '.data.results_metadata.columns' "$TMP/result.json")
    api PUT "/api/card/$model_id" "$(jq -nc --argjson m "$meta" '{result_metadata: $m}')" > /dev/null
    MODEL_IDS=$(echo "$MODEL_IDS" | jq -c --arg k "$(echo "$model" | jq -r .name)" --argjson i "$model_id" '.[$k] = $i')
    echo "dashboards: model '$(echo "$model" | jq -r .name)' -> $model_id"
done < "$TMP/models.jsonl"

# Dashboards and their questions.
jq -c '.dashboards[]' "$MANIFEST" > "$TMP/dashboards.jsonl"
while read -r dash; do
    coll_id=$(echo "$COLLECTION_IDS" | jq -r --arg k "$(echo "$dash" | jq -r .collection)" '.[$k]')
    dash_body=$(echo "$dash" | jq -c --argjson c "$coll_id" '{name, description, collection_id: $c}')
    dash_id=$(find_item "$coll_id" dashboard "$(echo "$dash" | jq -r .name)")
    if [ -z "$dash_id" ]; then
        dash_id=$(api POST /api/dashboard "$dash_body" | jq -r .id)
    else
        api PUT "/api/dashboard/$dash_id" "$dash_body" > /dev/null
    fi

    DASHCARDS='[]'
    n=0
    echo "$dash" | jq -c '.cards[]' > "$TMP/cards.jsonl"
    while read -r card; do
        n=$((n + 1))
        sql=$(cat "$DIR/sql/$(echo "$card" | jq -r .sql)")
        card_body=$(echo "$card" | jq -c --arg sql "$sql" --argjson db "$DB_ID" --argjson c "$coll_id" '{
            name,
            display,
            collection_id: $c,
            visualization_settings: (.settings // {}),
            dataset_query: {type: "native", database: $db, native: {
                query: $sql,
                # A card listing "filters" must have an optional {{name}} variable of that name in its SQL.
                "template-tags": (((.filters // []) | map({(.): {id: ., name: ., "display-name": ., type: "text", required: false}}) | add) // {})
            }}
        }')
        card_id=$(upsert_card "$coll_id" card "$(echo "$card" | jq -r .name)" "$card_body")
        # Negative ids tell Metabase these dashboard cards are new. Each filter of the card is wired
        # to the variable of the same name in its SQL.
        DASHCARDS=$(echo "$DASHCARDS" | jq -c --argjson card "$card" --argjson cid "$card_id" --argjson n "$n" \
            '. + [{id: (-$n), card_id: $cid, row: $card.row, col: $card.col, size_x: $card.size_x, size_y: $card.size_y,
                   parameter_mappings: [($card.filters // [])[] as $f
                       | {parameter_id: $f, card_id: $cid, target: ["variable", ["template-tag", $f]]}],
                   visualization_settings: {}}]')
    done < "$TMP/cards.jsonl"

    # Text cards (markdown) have no question behind them.
    echo "$dash" | jq -c '.texts[]?' > "$TMP/texts.jsonl"
    while read -r text; do
        n=$((n + 1))
        DASHCARDS=$(echo "$DASHCARDS" | jq -c --argjson t "$text" --argjson n "$n" \
            '. + [{id: (-$n), card_id: null, row: $t.row, col: $t.col, size_x: $t.size_x, size_y: $t.size_y,
                   visualization_settings: {virtual_card: {name: null, display: "text", archived: false}, text: $t.text}}]')
    done < "$TMP/texts.jsonl"

    # Dashboard filters; a filter with "values_from_model" gets its dropdown values from that model.
    PARAMS=$(echo "$dash" | jq -c --argjson models "$MODEL_IDS" '[(.parameters // [])[] | . as $p
        | {id: $p.id, name: $p.name, slug: $p.id, type: $p.type, sectionId: "string", values_query_type: "list"}
          + (if $p.values_from_model
             then {values_source_type: "card",
                   values_source_config: {card_id: $models[$p.values_from_model],
                                          value_field: ["field", $p.values_column, {"base-type": "type/Text"}]}}
             else {} end)]')

    api PUT "/api/dashboard/$dash_id" "$(jq -nc --argjson d "$DASHCARDS" --argjson p "$PARAMS" '{parameters: $p, dashcards: $d}')" > /dev/null
    echo "dashboards: '$(echo "$dash" | jq -r .name)' -> /dashboard/$dash_id ($n cards)"
done < "$TMP/dashboards.jsonl"

rm -rf "$TMP"
