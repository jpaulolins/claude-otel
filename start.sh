#!/usr/bin/env bash

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${PROJECT_DIR}/docker-compose.yml"

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker not found in PATH."
  exit 1
fi

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  echo "Error: docker-compose.yml not found in ${PROJECT_DIR}."
  exit 1
fi

require_docker() {
  if ! docker info >/dev/null 2>&1; then
    echo "Error: cannot contact the Docker daemon (docker info failed)." >&2
    echo "       Start Docker Desktop / Colima / Rancher Desktop and check socket permissions." >&2
    exit 1
  fi
}

run_compose() {
  require_docker
  docker compose -f "${COMPOSE_FILE}" "$@"
}

up() {
  echo "Starting full environment (build + detached)..."
  run_compose up --build -d
  echo "Environment started."
}

down() {
  echo "Stopping and removing containers, network, volumes and local images..."
  # Include all compose profiles so services like mcp-server (profile: mcp) and
  # metabase (profile: metabase) are stopped before --rmi local / network
  # removal; otherwise those resources stay "in use" and Docker prints warnings.
  run_compose --profile mcp --profile metabase down --volumes --remove-orphans --rmi local
  echo "Environment removed."
}

restart() {
  down
  up
}

status() {
  run_compose ps
}

logs() {
  run_compose logs -f --tail=200
}

up_mcp() {
  echo "Starting full environment + MCP server (build + detached)..."
  run_compose --profile mcp up --build -d
  echo "Environment + MCP server started."
}

up_metabase() {
  local password_file="${PROJECT_DIR}/metabase/.admin-password"

  echo "Fetching ClickHouse Metabase driver (if missing)..."
  "${PROJECT_DIR}/metabase/fetch-driver.sh"

  # Generate a random 16-char password on first run; reuse on subsequent runs
  # so the credential stays stable across restarts without hard-coding it.
  # openssl rand -base64 12 → exactly 16 base64 chars (12 bytes, no = padding);
  # tr replaces the two non-alphanumeric base64 chars (+ /) so the result is
  # pure alphanumeric. Single pipeline, no head -c, no SIGPIPE with pipefail.
  if [[ ! -s "${password_file}" ]]; then
    openssl rand -base64 12 | tr '+/' 'pQ' > "${password_file}"
  fi
  export MB_ADMIN_PASSWORD
  MB_ADMIN_PASSWORD=$(<"${password_file}")

  echo "Starting full environment + Metabase (build + detached)..."
  run_compose --profile metabase up --build -d

  echo "Provisioning Metabase dashboard..."
  "${PROJECT_DIR}/metabase/bootstrap.sh"

  cat <<EOF
Environment + Metabase started.
  URL:      http://localhost:3000
  Login:    admin@claude-otel.local
  Password: ${MB_ADMIN_PASSWORD}
  (saved in metabase/.admin-password — delete to rotate)
EOF
}

usage() {
  cat <<'EOF'
Usage:
  ./start.sh up           # start full environment (clickhouse + collector)
  ./start.sh up-mcp       # start everything + MCP server (Streamable HTTP on /mcp port 8081)
  ./start.sh up-metabase  # start everything + Metabase (port 3000) with auto-provisioned dashboard
  ./start.sh down         # stop all project containers (incl. mcp + metabase profiles), volumes, local images
  ./start.sh restart      # restart everything
  ./start.sh status       # show service status
  ./start.sh logs         # follow logs

No argument:
  equivalent to "up"
EOF
}

ACTION="${1:-up}"

case "${ACTION}" in
  up) up ;;
  up-mcp) up_mcp ;;
  up-metabase) up_metabase ;;
  down) down ;;
  restart) restart ;;
  status) status ;;
  logs) logs ;;
  -h|--help|help) usage ;;
  *)
    echo "Invalid command: ${ACTION}"
    usage
    exit 1
    ;;
esac
