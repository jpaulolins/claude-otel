#!/usr/bin/env bash
#
# Idempotently provisions a Metabase instance:
#   1. Waits for /api/health.
#   2. Runs first-time setup (creates admin) OR logs in.
#   3. Adds the ClickHouse database connection.
#   4. Triggers schema sync, waits for tables.
#   5. Creates a collection, the cards from cards.json, and a dashboard.
#   6. Lays out the dashboard cards according to the position field in cards.json.
#
# Re-running is safe: existing entities are detected by name and reused; the
# dashboard layout is overwritten on every run (so editing cards.json + re-
# running this script reshapes the dashboard).

set -euo pipefail

MB_URL="${MB_URL:-http://localhost:3000}"
MB_ADMIN_EMAIL="${MB_ADMIN_EMAIL:-admin@claude-otel.local}"
MB_ADMIN_PASSWORD="${MB_ADMIN_PASSWORD:-ClaudeOtel#2026}"  # overridden by start.sh
DB_NAME="${MB_DB_NAME:-Claude Observability}"
CH_HOST="${MB_CLICKHOUSE_HOST:-clickhouse}"
CH_PORT="${MB_CLICKHOUSE_PORT:-8123}"
CH_USER="${MB_CLICKHOUSE_USER:-otel_ingest}"
CH_PASSWORD="${MB_CLICKHOUSE_PASSWORD:-CHANGE_ME}"
CH_DB="${MB_CLICKHOUSE_DB:-observability}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CARDS_FILE="${SCRIPT_DIR}/cards.json"

for cmd in curl jq; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Error: '$cmd' not found in PATH (required by bootstrap.sh)." >&2
    exit 1
  fi
done

if [[ ! -f "$CARDS_FILE" ]]; then
  echo "Error: cards spec not found at $CARDS_FILE" >&2
  exit 1
fi

#------------------------------------------------------------------- helpers

log() { echo "[metabase-bootstrap] $*"; }

# Wait for a healthy /api/health response.
wait_for_health() {
  log "Waiting for Metabase at ${MB_URL}/api/health ..."
  local deadline=$(( $(date +%s) + 240 ))
  while true; do
    if body=$(curl -fsS "${MB_URL}/api/health" 2>/dev/null) && \
       echo "$body" | jq -e '.status == "ok"' >/dev/null 2>&1; then
      log "Metabase is healthy."
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      echo "Error: Metabase did not become healthy within 240s." >&2
      return 1
    fi
    sleep 3
  done
}

# Authenticated curl helper. Usage: mb METHOD PATH [JSON_BODY]
mb() {
  local method="$1" path="$2" data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" "${MB_URL}${path}" \
      -H "X-Metabase-Session: ${SESSION_ID}" \
      -H "Content-Type: application/json" \
      -d "$data"
  else
    curl -fsS -X "$method" "${MB_URL}${path}" \
      -H "X-Metabase-Session: ${SESSION_ID}"
  fi
}

#------------------------------------------------------------------- 1. health
wait_for_health

#------------------------------------------------------------------- 2. session
PROPS=$(curl -fsS "${MB_URL}/api/session/properties")
SETUP_TOKEN=$(echo "$PROPS" | jq -r '.["setup-token"] // empty')
HAS_USER_SETUP=$(echo "$PROPS" | jq -r '.["has-user-setup"] // false')

if [[ -n "$SETUP_TOKEN" && "$HAS_USER_SETUP" != "true" ]]; then
  log "First-time setup detected. Creating admin '${MB_ADMIN_EMAIL}'..."
  payload=$(jq -n \
    --arg token "$SETUP_TOKEN" \
    --arg email "$MB_ADMIN_EMAIL" \
    --arg password "$MB_ADMIN_PASSWORD" \
    '{
       token: $token,
       user: {
         first_name: "Admin",
         last_name: "User",
         email: $email,
         password: $password,
         site_name: "Claude Code Observability"
       },
       prefs: {site_name: "Claude Code Observability", allow_tracking: false}
     }')
  SESSION_ID=$(curl -fsS -X POST "${MB_URL}/api/setup" \
    -H "Content-Type: application/json" \
    -d "$payload" | jq -r '.id')
else
  log "Logging in as ${MB_ADMIN_EMAIL}..."
  payload=$(jq -n --arg u "$MB_ADMIN_EMAIL" --arg p "$MB_ADMIN_PASSWORD" \
    '{username: $u, password: $p}')
  SESSION_ID=$(curl -fsS -X POST "${MB_URL}/api/session" \
    -H "Content-Type: application/json" \
    -d "$payload" | jq -r '.id')
fi

if [[ -z "$SESSION_ID" || "$SESSION_ID" == "null" ]]; then
  echo "Error: failed to obtain a Metabase session id." >&2
  exit 1
fi

#------------------------------------------------------------------- 3. database
log "Looking for existing database '${DB_NAME}'..."
DB_ID=$(mb GET "/api/database" \
  | jq -r --arg n "$DB_NAME" '.data[] | select(.name == $n) | .id' \
  | head -1)

if [[ -z "$DB_ID" ]]; then
  log "Creating database '${DB_NAME}' (clickhouse @ ${CH_HOST}:${CH_PORT})..."
  payload=$(jq -n \
    --arg name "$DB_NAME" \
    --arg host "$CH_HOST" \
    --argjson port "$CH_PORT" \
    --arg user "$CH_USER" \
    --arg password "$CH_PASSWORD" \
    --arg dbname "$CH_DB" \
    '{
       engine: "clickhouse",
       name: $name,
       details: {
         host: $host,
         port: $port,
         user: $user,
         password: $password,
         dbname: $dbname,
         "scan-all-databases": false,
         ssl: false
       },
       is_full_sync: true,
       is_on_demand: false
     }')
  DB_ID=$(mb POST "/api/database" "$payload" | jq -r '.id')
  log "Database created (id=${DB_ID})."
else
  log "Database already registered (id=${DB_ID})."
fi

#------------------------------------------------------------------- 4. sync
log "Triggering schema sync..."
mb POST "/api/database/${DB_ID}/sync_schema" >/dev/null 2>&1 || true

log "Waiting for tables to be discovered..."
deadline=$(( $(date +%s) + 120 ))
while true; do
  table_count=$(mb GET "/api/database/${DB_ID}/metadata" | jq '.tables | length' 2>/dev/null || echo 0)
  if (( table_count >= 4 )); then
    log "  ${table_count} tables visible."
    break
  fi
  if (( $(date +%s) >= deadline )); then
    log "  Warning: only ${table_count} tables visible after 120s. Proceeding."
    break
  fi
  sleep 3
done

#------------------------------------------------------------------- 5. collection
COLLECTION_NAME=$(jq -r '.collection_name' "$CARDS_FILE")
log "Looking for collection '${COLLECTION_NAME}'..."
COLLECTION_ID=$(mb GET "/api/collection" \
  | jq -r --arg n "$COLLECTION_NAME" '.[] | select(.name == $n) | .id' \
  | head -1)

if [[ -z "$COLLECTION_ID" ]]; then
  log "Creating collection..."
  payload=$(jq -n --arg name "$COLLECTION_NAME" \
    '{name: $name, color: "#509EE3"}')
  COLLECTION_ID=$(mb POST "/api/collection" "$payload" | jq -r '.id')
  log "Collection created (id=${COLLECTION_ID})."
else
  log "Collection already exists (id=${COLLECTION_ID})."
fi

#------------------------------------------------------------------- 6. cards
CARD_ID_FILE=$(mktemp)
trap 'rm -f "$CARD_ID_FILE"' EXIT

EXISTING_CARDS=$(mb GET "/api/collection/${COLLECTION_ID}/items?models=card" | jq -c '.data // []')

log "Provisioning cards (existing ones are PUT-updated so cards.json is the source of truth)..."
while IFS= read -r card_spec; do
  card_name=$(echo "$card_spec" | jq -r '.name')
  existing_id=$(echo "$EXISTING_CARDS" \
    | jq -r --arg n "$card_name" '.[] | select(.model == "card" and .name == $n) | .id' \
    | head -1)

  payload=$(echo "$card_spec" | jq \
    --argjson db "$DB_ID" \
    --argjson coll "$COLLECTION_ID" \
    '{
       name: .name,
       description: .description,
       display: .display,
       visualization_settings: .visualization_settings,
       dataset_query: {
         type: "native",
         native: {query: .sql, "template-tags": {}},
         database: $db
       },
       collection_id: $coll
     }')

  if [[ -n "$existing_id" && "$existing_id" != "null" ]]; then
    mb PUT "/api/card/${existing_id}" "$payload" >/dev/null
    printf '%s\t%s\n' "$card_name" "$existing_id" >> "$CARD_ID_FILE"
    log "  [updated] ${card_name} (id=${existing_id})"
  else
    card_id=$(mb POST "/api/card" "$payload" | jq -r '.id')
    printf '%s\t%s\n' "$card_name" "$card_id" >> "$CARD_ID_FILE"
    log "  [created] ${card_name} (id=${card_id})"
  fi
done < <(jq -c '.cards[]' "$CARDS_FILE")

#------------------------------------------------------------------- 7. dashboard
DASHBOARD_NAME=$(jq -r '.dashboard_name' "$CARDS_FILE")
DASHBOARD_DESCRIPTION=$(jq -r '.dashboard_description' "$CARDS_FILE")

log "Looking for dashboard '${DASHBOARD_NAME}'..."
DASHBOARD_ID=$(mb GET "/api/collection/${COLLECTION_ID}/items?models=dashboard" \
  | jq -r --arg n "$DASHBOARD_NAME" '.data[] | select(.name == $n) | .id' \
  | head -1)

if [[ -z "$DASHBOARD_ID" ]]; then
  payload=$(jq -n \
    --arg name "$DASHBOARD_NAME" \
    --arg desc "$DASHBOARD_DESCRIPTION" \
    --argjson coll "$COLLECTION_ID" \
    '{name: $name, description: $desc, collection_id: $coll}')
  DASHBOARD_ID=$(mb POST "/api/dashboard" "$payload" | jq -r '.id')
  log "Dashboard created (id=${DASHBOARD_ID})."
else
  log "Dashboard already exists (id=${DASHBOARD_ID})."
fi

#------------------------------------------------------------------- 8. layout
log "Setting dashboard layout..."
dashcards_json='[]'
i=0
while IFS= read -r card_spec; do
  card_name=$(echo "$card_spec" | jq -r '.name')
  card_id=$(awk -F'\t' -v n="$card_name" '$1 == n {print $2; exit}' "$CARD_ID_FILE")
  if [[ -z "$card_id" || "$card_id" == "null" ]]; then
    log "  Warning: no id resolved for card '${card_name}', skipping."
    continue
  fi
  i=$((i + 1))
  dashcard=$(echo "$card_spec" | jq \
    --argjson id "-$i" \
    --argjson card_id "$card_id" \
    '{
       id: $id,
       card_id: $card_id,
       row: .position.row,
       col: .position.col,
       size_x: .position.size_x,
       size_y: .position.size_y,
       parameter_mappings: [],
       visualization_settings: {}
     }')
  dashcards_json=$(echo "$dashcards_json" | jq --argjson dc "$dashcard" '. + [$dc]')
done < <(jq -c '.cards[]' "$CARDS_FILE")

mb PUT "/api/dashboard/${DASHBOARD_ID}" \
  "$(jq -n --argjson dc "$dashcards_json" '{dashcards: $dc}')" >/dev/null

count=$(echo "$dashcards_json" | jq 'length')
log "Layout applied (${count} cards)."

echo ""
echo "✓ Metabase provisioning complete."
echo "  Dashboard: ${MB_URL}/dashboard/${DASHBOARD_ID}"
echo "  Login:     ${MB_ADMIN_EMAIL} / ${MB_ADMIN_PASSWORD}"
