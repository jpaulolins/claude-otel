#!/usr/bin/env bash
#
# Downloads the official ClickHouse driver for Metabase into ./plugins/.
# Idempotent: skips the download if the JAR already exists and is non-empty.
#
# Driver releases: https://github.com/ClickHouse/metabase-clickhouse-driver/releases
# Compatible with Metabase v0.51.x.

set -euo pipefail

DRIVER_VERSION="${METABASE_CLICKHOUSE_DRIVER_VERSION:-1.53.4}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGINS_DIR="${SCRIPT_DIR}/plugins"
JAR_PATH="${PLUGINS_DIR}/clickhouse.metabase-driver.jar"
DRIVER_URL="https://github.com/ClickHouse/metabase-clickhouse-driver/releases/download/${DRIVER_VERSION}/clickhouse.metabase-driver.jar"

mkdir -p "${PLUGINS_DIR}"

if [[ -s "${JAR_PATH}" ]]; then
  echo "ClickHouse driver already present: ${JAR_PATH}"
  exit 0
fi

echo "Downloading ClickHouse Metabase driver v${DRIVER_VERSION}..."
echo "  ${DRIVER_URL}"

if ! curl -L --fail --silent --show-error -o "${JAR_PATH}.tmp" "${DRIVER_URL}"; then
  rm -f "${JAR_PATH}.tmp"
  echo "Error: failed to download driver from ${DRIVER_URL}" >&2
  echo "       Check the version pin (METABASE_CLICKHOUSE_DRIVER_VERSION) and your network." >&2
  exit 1
fi

if [[ ! -s "${JAR_PATH}.tmp" ]]; then
  rm -f "${JAR_PATH}.tmp"
  echo "Error: downloaded driver is empty." >&2
  exit 1
fi

mv "${JAR_PATH}.tmp" "${JAR_PATH}"
echo "Driver installed at ${JAR_PATH}"
