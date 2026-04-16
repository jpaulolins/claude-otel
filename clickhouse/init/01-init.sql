-- Database `observability` and user `otel_ingest` come from docker-compose env
-- (CLICKHOUSE_DB, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD). The image stores that user
-- in users_xml, which is read-only at runtime.
--
-- Do NOT run GRANT ... TO otel_ingest here: ClickHouse returns ACCESS_STORAGE_READONLY
-- because GRANT tries to persist changes to the XML-backed user definition.
-- The Docker-created user already has the rights needed for this database.

CREATE DATABASE IF NOT EXISTS observability;

SELECT 1;
