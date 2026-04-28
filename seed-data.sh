#!/usr/bin/env bash
# seed-data.sh — Generate realistic telemetry data for all OTEL tables.
#
# Populates:
#   otel_logs            — via cotel hook command
#   otel_traces          — via cotel hook command (span per hook)
#   otel_traces_trace_id_ts — materialized view (auto-populated from otel_traces)
#   otel_metrics_sum     — via OTLP HTTP /v1/metrics (Sum metrics)
#   otel_metrics_gauge   — via OTLP HTTP /v1/metrics (Gauge metrics)
#
# Usage: ./seed-data.sh [--token TOKEN] [--audit-url URL] [--otlp-url URL]

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
OTLP_URL="${OTLP_ENDPOINT:-http://localhost:4318}"

while [[ $# -gt 0 ]]; do
  case $1 in
    --otlp-url)  OTLP_URL="$2"; shift 2 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# Locate cotel binary.
COTEL_BIN="${COTEL_BIN:-}"
if [[ -z "$COTEL_BIN" ]]; then
  if command -v cotel &>/dev/null; then
    COTEL_BIN="cotel"
  elif [[ -x "$(dirname "$0")/go-hook-mcp-api/bin/cotel" ]]; then
    COTEL_BIN="$(dirname "$0")/go-hook-mcp-api/bin/cotel"
  else
    printf '\033[0;31m[ERROR]\033[0m cotel binary not found. Run: cd go-hook-mcp-api && make build\n'
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# Data pools — realistic values for variety
# ---------------------------------------------------------------------------
USERS=(
  "joao.lins@example.com"
  "maria.silva@example.com"
  "carlos.dev@example.com"
  "ana.costa@example.com"
)
USER_IDS=(
  "user_joao_01"
  "user_maria_02"
  "user_carlos_03"
  "user_ana_04"
)
ORG_ID="org_example_001"

MODELS=(
  "claude-sonnet-4-5-20250514"
  "claude-opus-4-5-20250514"
  "claude-haiku-3-5-20241022"
)

TOOLS=("Bash" "Read" "Write" "Edit" "Grep" "Glob" "Agent")

BASH_COMMANDS=(
  "npm test"
  "go build ./..."
  "docker compose up -d"
  "git status"
  "python manage.py migrate"
  "cargo build --release"
  "make lint"
  "pytest -x tests/"
)

READ_PATHS=(
  "/repo/app/main.go"
  "/repo/src/index.ts"
  "/repo/README.md"
  "/repo/docker-compose.yml"
  "/repo/Makefile"
)

EDIT_FILES=(
  "/repo/app/handler.go"
  "/repo/src/components/App.tsx"
  "/repo/config/settings.py"
  "/repo/internal/service.go"
)

CWDS=(
  "/Users/joao/projects/api-gateway"
  "/Users/maria/projects/frontend-app"
  "/Users/carlos/projects/data-pipeline"
  "/Users/ana/projects/infra-tools"
)

# Counters
HOOKS_SENT=0
METRICS_SENT=0

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log_info()  { printf "\033[0;36m[INFO]\033[0m  %s\n" "$*"; }
log_ok()    { printf "\033[0;32m[OK]\033[0m    %s\n" "$*"; }
log_err()   { printf "\033[0;31m[ERROR]\033[0m %s\n" "$*"; }

now_iso() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
now_nano() { python3 -c "import time; print(int(time.time() * 1e9))"; }

# Bash 3.2–compatible (macOS default): no `local -n` namerefs.
rand_element() {
  local arr_name=$1 size idx
  eval "size=\${#${arr_name}[@]}"
  idx=$((RANDOM % size))
  eval "printf '%s\n' \"\${${arr_name}[$idx]}\""
}

rand_range() {
  echo $(( RANDOM % ($2 - $1 + 1) + $1 ))
}

# Bash 3.2 has no ${var,,}; use tr for lowercase.
tolower() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

uuid_like() {
  printf '%04x%04x-%04x-%04x-%04x-%04x%04x%04x' \
    $RANDOM $RANDOM $RANDOM $RANDOM $RANDOM $RANDOM $RANDOM $RANDOM
}

# ---------------------------------------------------------------------------
# Health checks
# ---------------------------------------------------------------------------
check_service() {
  local name=$1 url=$2
  # Accept any HTTP response (including 405 Method Not Allowed from the OTEL
  # collector on GET /v1/metrics) — we just need the port to be reachable.
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" "$url" 2>/dev/null) || true
  if [[ -n "$code" && "$code" != "000" ]]; then
    log_ok "$name is reachable at $url (HTTP $code)"
    return 0
  else
    log_err "$name is NOT reachable at $url"
    return 1
  fi
}

log_info "Checking services..."
check_service "otel-collector" "$OTLP_URL/v1/metrics" || true

echo ""
log_info "======================================"
log_info "  Seeding telemetry data"
log_info "======================================"
echo ""

# ---------------------------------------------------------------------------
# 1. HOOK EVENTS → otel_logs + otel_traces + otel_traces_trace_id_ts
# ---------------------------------------------------------------------------
send_hook() {
  local endpoint=$1  # kept for log messages; no longer used for routing
  local payload=$2
  local rc=0
  printf '%s' "$payload" | \
    OTEL_EXPORTER_OTLP_ENDPOINT="$OTLP_URL" \
    OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf" \
    OTEL_SERVICE_NAME="cotel-detect" \
    "$COTEL_BIN" hook >/dev/null 2>&1 || rc=$?
  if [[ $rc -eq 0 ]]; then
    HOOKS_SENT=$((HOOKS_SENT + 1))
    return 0
  else
    log_err "Hook $endpoint failed (cotel exit $rc)"
    return 1
  fi
}

generate_session_id() {
  echo "session-$(uuid_like)"
}

# Generate timestamps spread across the last 7 days
past_timestamp() {
  local hours_ago=$1
  if [[ "$(uname)" == "Darwin" ]]; then
    date -u -v-${hours_ago}H +"%Y-%m-%dT%H:%M:%SZ"
  else
    date -u -d "${hours_ago} hours ago" +"%Y-%m-%dT%H:%M:%SZ"
  fi
}

past_nano() {
  local hours_ago=$1
  python3 -c "import time; print(int((time.time() - $hours_ago * 3600) * 1e9))"
}

log_info "Phase 1: Sending hook events (logs + traces)..."
echo ""

# We'll generate events across 7 days, multiple sessions per user
for user_idx in "${!USERS[@]}"; do
  user="${USERS[$user_idx]}"
  user_id="${USER_IDS[$user_idx]}"
  cwd="${CWDS[$user_idx]}"
  # repository = filepath.Base(cwd) — matches audit.Normalize() behaviour so
  # Body.repository (hook events) and Attributes['repository'] (metrics below)
  # use the same canonical value per user/session.
  repo=$(basename "$cwd")

  # 3 sessions per user spread over the week
  for s in 1 2 3; do
    session_id=$(generate_session_id)
    hours_base=$((s * 48 + user_idx * 12))  # spread across time

    log_info "  User: $user | Session $s ($session_id)"

    # Each session: 4-8 tool uses
    num_events=$(rand_range 4 8)
    for e in $(seq 1 $num_events); do
      hours_ago=$((hours_base - e * 2))
      if (( hours_ago < 0 )); then hours_ago=0; fi
      ts=$(past_timestamp $hours_ago)
      tool=$(rand_element TOOLS)
      tool_lc=$(tolower "$tool")
      tool_use_id="toolu_$(uuid_like | tr -d '-' | head -c 20)"

      # Build command/content based on tool
      case "$tool" in
        Bash)    cmd=$(rand_element BASH_COMMANDS) ;;
        Read)    cmd=$(rand_element READ_PATHS) ;;
        Write)   cmd=$(rand_element EDIT_FILES) ;;
        Edit)    cmd=$(rand_element EDIT_FILES) ;;
        Grep)    cmd="pattern: TODO|FIXME" ;;
        Glob)    cmd="**/*.go" ;;
        Agent)   cmd="Explore codebase structure" ;;
      esac

      event_type="claude_code.${tool_lc}.post_tool_use"

      # --- PRE-TOOL-USE ---
      payload=$(cat <<ENDJSON
{
  "event_type": "claude_code.${tool_lc}.pre_tool_use",
  "user_id": "$user",
  "session_id": "$session_id",
  "tool_use_id": "$tool_use_id",
  "tool_name": "$tool",
  "command": "$cmd",
  "cwd": "$cwd",
  "permission_mode": "default",
  "success": true,
  "repository": "$repo",
  "organization_id": "$ORG_ID",
  "timestamp": "$ts"
}
ENDJSON
)
      send_hook "pre-tool-use" "$payload"

      # --- POST-TOOL-USE (success or failure) ---
      if (( RANDOM % 10 < 8 )); then
        # 80% success
        exit_code=0
        stdout_msg="Operation completed successfully"
        stderr_msg=""
        success=true
        endpoint="post-tool-use"
      else
        # 20% failure
        exit_code=$((RANDOM % 126 + 1))
        stdout_msg=""
        stderr_msg="Error: command failed with exit code $exit_code"
        success=false
        endpoint="post-tool-use-failure"
      fi

      payload=$(cat <<ENDJSON
{
  "event_type": "$event_type",
  "user_id": "$user",
  "session_id": "$session_id",
  "tool_use_id": "$tool_use_id",
  "tool_name": "$tool",
  "command": "$cmd",
  "cwd": "$cwd",
  "permission_mode": "default",
  "success": $success,
  "tool_response": {
    "exit_code": $exit_code,
    "stdout": "$stdout_msg",
    "stderr": "$stderr_msg"
  },
  "transcript_path": "/Users/${user%%@*}/.claude/projects/proj/transcript.jsonl",
  "repository": "$repo",
  "organization_id": "$ORG_ID",
  "timestamp": "$ts"
}
ENDJSON
)
      send_hook "$endpoint" "$payload"
    done

    # --- COMMAND event (one per session) ---
    ts=$(past_timestamp $hours_base)
    payload=$(cat <<ENDJSON
{
  "event_type": "claude_code.command",
  "user_id": "$user",
  "session_id": "$session_id",
  "tool_use_id": "",
  "tool_name": "",
  "command": "claude code --model claude-sonnet-4-5",
  "cwd": "$cwd",
  "permission_mode": "default",
  "success": true,
  "repository": "$repo",
  "organization_id": "$ORG_ID",
  "timestamp": "$ts"
}
ENDJSON
)
    send_hook "command" "$payload"
  done
done

log_ok "Hook events sent: $HOOKS_SENT"
echo ""

# ---------------------------------------------------------------------------
# 2. OTLP METRICS → otel_metrics_sum + otel_metrics_gauge
# ---------------------------------------------------------------------------
log_info "Phase 2: Sending OTLP metrics (sum + gauge)..."
echo ""

send_otlp_metrics() {
  local payload=$1
  local resp
  resp=$(curl -sf -w "\n%{http_code}" -X POST "$OTLP_URL/v1/metrics" \
    -H "Content-Type: application/json" \
    -d "$payload" 2>&1) || true
  local code
  code=$(echo "$resp" | tail -1)
  if [[ "$code" == "200" ]]; then
    METRICS_SENT=$((METRICS_SENT + 1))
    return 0
  else
    log_err "OTLP metrics failed (HTTP $code): $(echo "$resp" | head -1)"
    return 1
  fi
}

# Token costs per model (per 1M tokens, approximate)
cost_input_opus=15.0
cost_output_opus=75.0
cost_input_sonnet=3.0
cost_output_sonnet=15.0
cost_input_haiku=0.80
cost_output_haiku=4.0

for user_idx in "${!USERS[@]}"; do
  user="${USERS[$user_idx]}"
  user_id="${USER_IDS[$user_idx]}"
  # repository attribute mirrors basename(CWDS[user_idx]) used in Phase 1,
  # so metrics Attributes['repository'] matches otel_logs Body.repository.
  repo=$(basename "${CWDS[$user_idx]}")

  for s in 1 2 3; do
    session_id="session-$(uuid_like)"
    hours_base=$((s * 48 + user_idx * 12))
    model_idx=$(( (user_idx + s) % ${#MODELS[@]} ))
    model="${MODELS[$model_idx]}"

    log_info "  Metrics: $user | Session $s | $model"

    # Token usage per type spread across multiple data points
    for day_offset in 0 1 2; do
      hours_ago=$((hours_base + day_offset * 24))
      if (( hours_ago < 0 )); then hours_ago=0; fi
      ts_nano=$(past_nano $hours_ago)
      start_nano=$(past_nano $((hours_ago + 1)))

      # Realistic token counts
      input_tokens=$(rand_range 2000 50000)
      output_tokens=$(rand_range 500 15000)
      cache_read=$(rand_range 1000 30000)
      cache_creation=$(rand_range 200 5000)

      # Calculate cost
      case "$model" in
        *opus*)   cost_in=$cost_input_opus;  cost_out=$cost_output_opus ;;
        *sonnet*) cost_in=$cost_input_sonnet; cost_out=$cost_output_sonnet ;;
        *haiku*)  cost_in=$cost_input_haiku;  cost_out=$cost_output_haiku ;;
      esac
      cost_usd=$(python3 -c "print(round($input_tokens * $cost_in / 1e6 + $output_tokens * $cost_out / 1e6, 6))")

      # Active time in seconds (10-45 min)
      active_time=$(rand_range 600 2700)

      # Build OTLP metrics JSON — Sum metrics (token.usage, cost.usage, session.count)
      payload=$(cat <<ENDJSON
{
  "resourceMetrics": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"stringValue": "claude-code"}},
        {"key": "telemetry.source", "value": {"stringValue": "claude-code"}},
        {"key": "host.name", "value": {"stringValue": "workstation-${user_idx}"}}
      ]
    },
    "scopeMetrics": [{
      "scope": {"name": "claude-code-telemetry", "version": "1.0.0"},
      "metrics": [
        {
          "name": "claude_code.token.usage",
          "sum": {
            "dataPoints": [
              {
                "startTimeUnixNano": "$start_nano",
                "timeUnixNano": "$ts_nano",
                "asDouble": $input_tokens,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "model", "value": {"stringValue": "$model"}},
                  {"key": "type", "value": {"stringValue": "input"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              },
              {
                "startTimeUnixNano": "$start_nano",
                "timeUnixNano": "$ts_nano",
                "asDouble": $output_tokens,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "model", "value": {"stringValue": "$model"}},
                  {"key": "type", "value": {"stringValue": "output"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              },
              {
                "startTimeUnixNano": "$start_nano",
                "timeUnixNano": "$ts_nano",
                "asDouble": $cache_read,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "model", "value": {"stringValue": "$model"}},
                  {"key": "type", "value": {"stringValue": "cacheRead"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              },
              {
                "startTimeUnixNano": "$start_nano",
                "timeUnixNano": "$ts_nano",
                "asDouble": $cache_creation,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "model", "value": {"stringValue": "$model"}},
                  {"key": "type", "value": {"stringValue": "cacheCreation"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              }
            ],
            "aggregationTemporality": 2,
            "isMonotonic": true
          }
        },
        {
          "name": "claude_code.cost.usage",
          "sum": {
            "dataPoints": [
              {
                "startTimeUnixNano": "$start_nano",
                "timeUnixNano": "$ts_nano",
                "asDouble": $cost_usd,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "model", "value": {"stringValue": "$model"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              }
            ],
            "aggregationTemporality": 2,
            "isMonotonic": true
          }
        },
        {
          "name": "claude_code.session.count",
          "sum": {
            "dataPoints": [
              {
                "startTimeUnixNano": "$start_nano",
                "timeUnixNano": "$ts_nano",
                "asDouble": 1,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              }
            ],
            "aggregationTemporality": 2,
            "isMonotonic": true
          }
        }
      ]
    }]
  }]
}
ENDJSON
)
      send_otlp_metrics "$payload"

      # Gauge metric: active_time.total (separate request)
      gauge_payload=$(cat <<ENDJSON
{
  "resourceMetrics": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"stringValue": "claude-code"}},
        {"key": "telemetry.source", "value": {"stringValue": "claude-code"}}
      ]
    },
    "scopeMetrics": [{
      "scope": {"name": "claude-code-telemetry", "version": "1.0.0"},
      "metrics": [
        {
          "name": "claude_code.active_time.total",
          "gauge": {
            "dataPoints": [
              {
                "timeUnixNano": "$ts_nano",
                "asDouble": $active_time,
                "attributes": [
                  {"key": "user.email", "value": {"stringValue": "$user"}},
                  {"key": "user.id", "value": {"stringValue": "$user_id"}},
                  {"key": "organization.id", "value": {"stringValue": "$ORG_ID"}},
                  {"key": "model", "value": {"stringValue": "$model"}},
                  {"key": "session.id", "value": {"stringValue": "$session_id"}},
                  {"key": "repository", "value": {"stringValue": "$repo"}}
                ]
              }
            ]
          }
        }
      ]
    }]
  }]
}
ENDJSON
)
      send_otlp_metrics "$gauge_payload"
    done
  done
done

log_ok "OTLP metric batches sent: $METRICS_SENT"
echo ""

# ---------------------------------------------------------------------------
# 3. Wait for batch processor to flush and verify
# ---------------------------------------------------------------------------
log_info "Waiting 8s for OTEL batch processor to flush..."
sleep 8

echo ""
log_info "======================================"
log_info "  Verifying data in ClickHouse"
log_info "======================================"
echo ""

CH_URL="http://localhost:8123"
CH_AUTH="--user otel_ingest:CHANGE_ME"

query_ch() {
  # --get: converts --data-urlencode into a URL query-string (?query=...) so
  # ClickHouse receives a standard GET request with the SQL as a URL parameter.
  # Without --get, curl sends "query=SELECT..." as a POST body and ClickHouse
  # tries to execute "query=SELECT..." as raw SQL, causing a syntax error.
  # || true: bash 5.2+ exits on a failing command substitution with set -e;
  # suppress so a ClickHouse error logs a WARN instead of aborting the script.
  curl -sf --get "$CH_URL" $CH_AUTH --data-urlencode "query=$1" 2>/dev/null || true
}

tables=(
  "otel_logs"
  "otel_traces"
  "otel_traces_trace_id_ts"
  "otel_metrics_sum"
  "otel_metrics_gauge"
)

all_ok=true
for table in "${tables[@]}"; do
  count=$(query_ch "SELECT count() FROM observability.$table")
  if [[ -n "$count" && "$count" -gt 0 ]] 2>/dev/null; then
    log_ok "$table: $count rows"
  else
    log_err "$table: ${count:-0} rows (EMPTY)"
    all_ok=false
  fi
done

echo ""
if $all_ok; then
  log_ok "======================================"
  log_ok "  All tables populated successfully!"
  log_ok "======================================"
else
  log_err "Some tables are empty. Check the OTEL collector logs:"
  log_err "  docker compose logs otel-collector --tail 50"
fi

echo ""
log_info "======================================"
log_info "  Validating tool-query readiness"
log_info "  (repository + organization_id fanout)"
log_info "  — used by report_activity_timeline,"
log_info "    report_token_usage, report_developer_roi"
log_info "======================================"

# log_warn is introduced here (non-fatal): distinct from log_err because
# the seed must still exit 0 when a validation signals a gap.
log_warn() { printf "\033[0;33m[WARN]\033[0m  %s\n" "$*"; }

# Tiny helper: runs a ClickHouse query, trims whitespace, returns empty
# string on error (so `[[ -n ... ]]` checks behave predictably).
check_query() {
  local q=$1
  local out
  out=$(query_ch "$q" 2>/dev/null | tr -d '[:space:]')
  printf '%s' "$out"
}

# 1. otel_logs: total > 0, distinct repositories > 1, distinct org_id == 1
logs_total=$(check_query "SELECT count() FROM observability.otel_logs")
logs_repos=$(check_query "SELECT uniqExact(JSONExtractString(Body,'repository')) FROM observability.otel_logs WHERE JSONExtractString(Body,'repository') != ''")
logs_orgs=$(check_query "SELECT uniqExact(JSONExtractString(Body,'organization_id')) FROM observability.otel_logs WHERE JSONExtractString(Body,'organization_id') != ''")

if [[ -n "$logs_total" && "$logs_total" -gt 0 ]] 2>/dev/null; then
  log_ok "otel_logs rows: $logs_total"
else
  log_warn "otel_logs is empty — query: SELECT count() FROM observability.otel_logs"
fi
if [[ -n "$logs_repos" && "$logs_repos" -gt 1 ]] 2>/dev/null; then
  log_ok "otel_logs distinct repositories: $logs_repos"
else
  log_warn "otel_logs distinct repositories=${logs_repos:-0} (expected >1) — query: SELECT uniqExact(JSONExtractString(Body,'repository')) FROM observability.otel_logs"
fi
if [[ -n "$logs_orgs" && "$logs_orgs" == "1" ]]; then
  log_ok "otel_logs distinct organization_id: $logs_orgs (= $ORG_ID)"
else
  log_warn "otel_logs distinct organization_id=${logs_orgs:-0} (expected ==1) — query: SELECT uniqExact(JSONExtractString(Body,'organization_id')) FROM observability.otel_logs"
fi

# 2. otel_traces: total > 0, contains cotel-detect service
traces_total=$(check_query "SELECT count() FROM observability.otel_traces")
traces_has_cotel=$(check_query "SELECT count() FROM observability.otel_traces WHERE ServiceName = 'cotel-detect'")
if [[ -n "$traces_total" && "$traces_total" -gt 0 ]] 2>/dev/null; then
  log_ok "otel_traces rows: $traces_total"
else
  log_warn "otel_traces is empty — query: SELECT count() FROM observability.otel_traces"
fi
if [[ -n "$traces_has_cotel" && "$traces_has_cotel" -gt 0 ]] 2>/dev/null; then
  log_ok "otel_traces has ServiceName='cotel-detect' rows: $traces_has_cotel"
else
  log_warn "otel_traces missing cotel-detect spans — query: SELECT count() FROM observability.otel_traces WHERE ServiceName='cotel-detect'"
fi

# 3. otel_metrics_sum: per-MetricName count + distinct repository attr > 1
for mn in "claude_code.token.usage" "claude_code.cost.usage" "claude_code.session.count"; do
  mc=$(check_query "SELECT count() FROM observability.otel_metrics_sum WHERE MetricName = '$mn'")
  if [[ -n "$mc" && "$mc" -gt 0 ]] 2>/dev/null; then
    log_ok "otel_metrics_sum[$mn]: $mc rows"
  else
    log_warn "otel_metrics_sum[$mn] empty — query: SELECT count() FROM observability.otel_metrics_sum WHERE MetricName='$mn'"
  fi
done
sum_repos=$(check_query "SELECT uniqExact(Attributes['repository']) FROM observability.otel_metrics_sum WHERE Attributes['repository'] != ''")
if [[ -n "$sum_repos" && "$sum_repos" -gt 1 ]] 2>/dev/null; then
  log_ok "otel_metrics_sum distinct Attributes['repository']: $sum_repos"
else
  log_warn "otel_metrics_sum distinct Attributes['repository']=${sum_repos:-0} (expected >1) — query: SELECT uniqExact(Attributes['repository']) FROM observability.otel_metrics_sum"
fi

# 4. otel_metrics_gauge: active_time count + distinct repository > 1
gauge_at=$(check_query "SELECT count() FROM observability.otel_metrics_gauge WHERE MetricName = 'claude_code.active_time.total'")
if [[ -n "$gauge_at" && "$gauge_at" -gt 0 ]] 2>/dev/null; then
  log_ok "otel_metrics_gauge[claude_code.active_time.total]: $gauge_at rows"
else
  log_warn "otel_metrics_gauge[claude_code.active_time.total] empty — query: SELECT count() FROM observability.otel_metrics_gauge WHERE MetricName='claude_code.active_time.total'"
fi
gauge_repos=$(check_query "SELECT uniqExact(Attributes['repository']) FROM observability.otel_metrics_gauge WHERE Attributes['repository'] != ''")
if [[ -n "$gauge_repos" && "$gauge_repos" -gt 1 ]] 2>/dev/null; then
  log_ok "otel_metrics_gauge distinct Attributes['repository']: $gauge_repos"
else
  log_warn "otel_metrics_gauge distinct Attributes['repository']=${gauge_repos:-0} (expected >1) — query: SELECT uniqExact(Attributes['repository']) FROM observability.otel_metrics_gauge"
fi

echo ""
log_info "Summary:"
log_info "  Hook events sent:    $HOOKS_SENT"
log_info "  Metric batches sent: $METRICS_SENT"
log_info ""
log_info "Query examples:"
log_info "  curl '$CH_URL' $CH_AUTH -d 'SELECT count() FROM observability.otel_logs'"
log_info "  curl '$CH_URL' $CH_AUTH -d 'SELECT MetricName, count() FROM observability.otel_metrics_sum GROUP BY MetricName'"
log_info "  curl '$CH_URL' $CH_AUTH -d 'SELECT count() FROM observability.otel_metrics_gauge'"
