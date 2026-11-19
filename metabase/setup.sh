#!/bin/sh
set -e

# Obtain initial token
RESPONSE=$(curl --url http://metabase:3000/api/session/properties 2>/dev/null)
SETUP_TOKEN=$(echo "$RESPONSE" | jq -r '.["setup-token"]' 2>/dev/null)
if [ "$SETUP_TOKEN" = "null" ]; then
    echo "Metabase has already been setup"
    exit 0
fi


# Configure Metabase
curl http://metabase:3000/api/setup \
    -H 'Content-Type: application/json' \
    --data "{
        \"token\": \"$SETUP_TOKEN\",
        \"user\": {
            \"email\": \"$ADMIN_EMAIL\",
            \"first_name\": \"admin\",
            \"last_name\": \"admin\",
            \"password\": \"$ADMIN_PASS\"
        },
        \"prefs\": {
            \"allow_tracking\": false,
            \"site_name\": \"Metrino\"
        }
    }" 2>/dev/null

# Obtain login token
LOGIN_DATA="{
              \"username\":\"$ADMIN_EMAIL\",
              \"password\":\"$ADMIN_PASS\",
              \"remember\":false
            }"
TOKEN=$(curl --url 'http://metabase:3000/api/session' -H 'Content-Type: application/json' --data "$LOGIN_DATA" 2>/dev/null | jq -r .id)


# Create and obtain api key for setup
API_KEY=$(curl --url 'http://metabase:3000/api/api-key' -H 'Content-Type: application/json' -b "metabase.SESSION=$TOKEN" --data '{"name":"setup","group_id":2}' 2>/dev/null | jq -r .unmasked_key)
mkdir -p $(dirname $METABASE_KEY_FILE)
printf '%s' $API_KEY > $METABASE_KEY_FILE


# Connect to Trino
curl --url http://metabase:3000/api/database \
    -H 'Content-Type: application/json' \
    -H "X-API-Key: $API_KEY" \
    --data "{
             \"details\": {
               \"host\": \"${TRINO_HOST:-trino-reverse-proxy}\",
               \"port\": ${TRINO_PORT:-9090},
               \"catalog\":\"$CATALOG\",
               \"schema\":null,
               \"user\":\"trino\",
               \"ssl\":false,
               \"prepared-optimized\":true
             },
             \"name\":\"trino\",
             \"engine\":\"starburst\"
            }"

# Remove Metabase's built-in "Sample Database" (it holds unrelated e-commerce data)
SAMPLE_DB_ID=$(curl -s http://metabase:3000/api/database -H "X-API-Key: $API_KEY" | jq -r '.data[] | select(.is_sample == true) | .id')
if [ -n "$SAMPLE_DB_ID" ]; then
    curl -s -X DELETE "http://metabase:3000/api/database/$SAMPLE_DB_ID" -H "X-API-Key: $API_KEY" > /dev/null
fi

# api METHOD PATH [JSON_BODY]: prints the response body, fails loudly on an HTTP error.
api() {
    if [ $# -ge 3 ]; then
        curl -sS --fail-with-body -X "$1" "http://metabase:3000$2" \
            -H 'Content-Type: application/json' -H "X-API-Key: $API_KEY" --data "$3"
    else
        curl -sS --fail-with-body -X "$1" "http://metabase:3000$2" -H "X-API-Key: $API_KEY"
    fi
}

# Create the groups the test users and the dashboards (manifest.json) name. Administrators is built in.
METABASE_GROUPS=$(api GET /api/permissions/group)
jq -nr --slurpfile users "$TEST_USERS" --slurpfile manifest /dashboards/manifest.json \
    '[$users[0][].groups[], $manifest[0].groups[]?.name] | unique[]' | while read -r name; do
    if ! echo "$METABASE_GROUPS" | jq -e --arg n "$name" 'any(.[]; .name == $n)' > /dev/null; then
        api POST /api/permissions/group "$(jq -nc --arg n "$name" '{name: $n}')" > /dev/null
        echo "setup: created group '$name'"
    fi
done

# Local development only: the test users in $TEST_USERS (metabase/test-users.json), with the password
# $TEST_USER_PASS, and their groups. Real users sign in with Google, and an admin adds them in Metabase. The
# admin already exists (created by /api/setup above), so only its groups are checked.
METABASE_GROUPS=$(api GET /api/permissions/group)
jq -c '.[]' "$TEST_USERS" | while read -r user; do
    email=$(echo "$user" | jq -r .email)
    user_id=$(api GET /api/user | jq -r --arg e "$email" '.data[] | select(.email == $e) | .id')
    if [ -z "$user_id" ]; then
        user_id=$(api POST /api/user "$(echo "$user" | jq -c --arg p "$TEST_USER_PASS" \
            '{email, first_name, last_name, password: $p}')" | jq -r .id)
        echo "setup: created test user $email"
    fi
    echo "$user" | jq -r '.groups[]' | while read -r name; do
        group_id=$(echo "$METABASE_GROUPS" | jq -r --arg n "$name" '.[] | select(.name == $n) | .id')
        if [ -z "$group_id" ]; then
            echo "setup: test user $email lists group '$name', which doesn't exist in Metabase" >&2
            exit 1
        fi
        if api GET "/api/user/$user_id" | jq -e --argjson g "$group_id" 'any(.user_group_memberships[]; .id == $g)' > /dev/null; then
            continue
        fi
        api POST /api/permissions/membership "$(jq -nc --argjson g "$group_id" --argjson u "$user_id" \
            '{group_id: $g, user_id: $u}')" > /dev/null
        echo "setup: added $email to '$name'"
    done
done

# Wait until metabase-sync has written every test user to Trino's group file, so the queries below have
# their users' groups. Then give Trino time to reread it, which it does every 5 seconds
# (trino/group-provider.properties).
missing_test_users() {
    jq -r '.[].email' "$TEST_USERS" | while read -r email; do
        tr ':,' '\n\n' < /groups/group.txt | grep -qxF "$email" || echo "$email"
    done
}
waited=0
while [ -n "$(missing_test_users)" ]; do
    if [ "$waited" -ge 60 ]; then
        echo "setup: after 60 seconds, Trino's group file still lacks:" $(missing_test_users) >&2
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done
echo "setup: the group file lists every test user"
sleep 6

# Create the sample collections, curated models, questions and dashboards. Queries run as the admin's
# session: Trino sees the Metabase user who ran a query, and the API key's user has no groups.
API_KEY="$API_KEY" ADMIN_SESSION="$TOKEN" sh /dashboards/dashboards.sh

# Restrict raw SQL and the raw tables to trusted groups
API_KEY="$API_KEY" sh /dashboards/access.sh

# The Metabase sync's schema file, the web admin page's column list, follows Metabase's scan of Trino's tables, which
# the dashboards above needed. Wait for it, so the integration tests find the tables there.
waited=0
until grep -q '"students"' /groups/schema.json 2>/dev/null; do
    if [ "$waited" -ge 60 ]; then
        echo "setup: after 60 seconds, the Metabase sync's schema file still lacks the students table" >&2
        exit 1
    fi
    sleep 1
    waited=$((waited + 1))
done
echo "setup: the schema file lists the sample tables"

echo "Metabase setup complete!"
echo ""
echo ""
echo "  🚀 Login at: http://localhost:8080"
echo "  👤 Username: $ADMIN_EMAIL"
echo "  🔑 Password: $ADMIN_PASS"
echo ""
echo ""
echo "  Other test users (password: $TEST_USER_PASS), with their groups in $TEST_USERS:"
jq -r '.[] | "  👤 \(.email): \(.groups | join(", "))"' "$TEST_USERS"
