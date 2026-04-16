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
  run_compose down --volumes --remove-orphans --rmi local
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

usage() {
  cat <<'EOF'
Usage:
  ./start.sh up        # start full environment (clickhouse + collector + audit-service)
  ./start.sh up-mcp    # start everything + MCP server (Streamable HTTP on /mcp port 8081)
  ./start.sh down      # stop and remove containers/volumes/local images
  ./start.sh restart   # restart everything
  ./start.sh status    # show service status
  ./start.sh logs      # follow logs

No argument:
  equivalent to "up"
EOF
}

ACTION="${1:-up}"

case "${ACTION}" in
  up) up ;;
  up-mcp) up_mcp ;;
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
