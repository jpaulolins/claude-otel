# ClickHouse Queries - Claude Code OTEL

All queries below run against ClickHouse via:

```bash
docker compose exec -T clickhouse clickhouse-client --query "<QUERY>"
```

---

## 1. List OTEL tables

Shows all tables created by the collector in the `observability` database.

```sql
SHOW TABLES FROM observability
```

**Returns:** table name (`otel_logs`, `otel_metrics_sum`, `otel_metrics_gauge`, `otel_traces`, etc.).

---

## 2. Recent logs

Returns the 10 most recent logs with timestamp, severity, service and truncated body.

```sql
SELECT
    Timestamp,
    SeverityText,
    ServiceName,
    substring(Body, 1, 120) AS body
FROM observability.otel_logs
ORDER BY Timestamp DESC
LIMIT 10
FORMAT PrettyCompactMonoBlock
```

**Returns:** timestamp, severity (INFO, etc.), service name (`claude-code` or `claude-audit-service`) and body preview.

---

## 3. Log count by service and severity

Overview of log volume grouped by service and severity level.

```sql
SELECT
    ServiceName,
    SeverityText,
    count() AS n
FROM observability.otel_logs
GROUP BY ServiceName, SeverityText
ORDER BY n DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** service, severity and log count.

---

## 4. Claude Code events by type

Shows event types emitted by Claude Code (`tool_result`, `tool_decision`, `api_request`, `user_prompt`).

```sql
SELECT
    Body,
    count() AS n
FROM observability.otel_logs
WHERE ServiceName = 'claude-code'
GROUP BY Body
ORDER BY n DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** event name and occurrence count.

---

## 5. Hook events by type and tool

Extracts from the Body JSON the hook name (`PreToolUse`, `PostToolUse`, `PostToolUseFailure`) and the tool that triggered it.

```sql
SELECT
    JSONExtractString(Body, 'hook_event_name') AS hook,
    JSONExtractString(Body, 'tool_name')       AS tool,
    count()                                    AS n
FROM observability.otel_logs
WHERE ServiceName = 'claude-audit-service'
GROUP BY hook, tool
ORDER BY n DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** hook name, tool name (e.g. `Bash`) and event count.

---

## 6. Token consumption by user, model and type

Primary audit query. Shows email, user id, org, model, token type and total.

```sql
SELECT
    Attributes['user.email']       AS user_email,
    Attributes['user.id']          AS user_id,
    Attributes['organization.id']  AS org_id,
    Attributes['model']            AS model,
    Attributes['type']             AS token_type,
    sum(Value)                     AS total_tokens
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.token.usage'
GROUP BY user_email, user_id, org_id, model, token_type
ORDER BY user_email, total_tokens DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** email, user id (hash), organization id, model (`claude-opus-4-6[1m]`, `claude-haiku-4-5-20251001`), token type (`input`, `output`, `cacheRead`, `cacheCreation`) and total.

---

## 7. Summarized token consumption (pivoted by token type)

Compact version with separate columns for each token type.

```sql
SELECT
    Attributes['user.email']  AS user_email,
    Attributes['model']       AS model,
    sum(Value)                AS total_tokens,
    round(sumIf(Value, Attributes['type'] = 'input'))         AS input,
    round(sumIf(Value, Attributes['type'] = 'output'))        AS output,
    round(sumIf(Value, Attributes['type'] = 'cacheRead'))     AS cache_read,
    round(sumIf(Value, Attributes['type'] = 'cacheCreation')) AS cache_creation
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.token.usage'
GROUP BY user_email, model
ORDER BY total_tokens DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** email, model, total tokens and individual columns (input, output, cache_read, cache_creation).

---

## 8. Cost (USD) by model

Shows accumulated cost in dollars by model.

```sql
SELECT
    Attributes['model']    AS model,
    round(sum(Value), 6)   AS total_cost_usd
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.cost.usage'
GROUP BY model
FORMAT PrettyCompactMonoBlock
```

**Returns:** model and total cost in USD.

---

## 9. Cost by user

Accumulated cost in dollars grouped by user email.

```sql
SELECT
    Attributes['user.email']  AS user_email,
    Attributes['model']       AS model,
    round(sum(Value), 4)      AS cost_usd
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.cost.usage'
GROUP BY user_email, model
ORDER BY cost_usd DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** email, model and cost in USD.

---

## 10. Cost by session

Cost grouped by session id - useful for identifying expensive sessions.

```sql
SELECT
    Attributes['session.id']  AS session,
    Attributes['user.email']  AS user_email,
    round(sum(Value), 4)      AS cost_usd
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.cost.usage'
GROUP BY session, user_email
ORDER BY cost_usd DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** session id, email and session cost in USD.

---

## 11. Tokens consumed in the last N minutes

Filters by time window. Adjust the `INTERVAL` as needed.

```sql
SELECT
    Attributes['user.email'] AS user_email,
    Attributes['model']      AS model,
    Attributes['type']       AS token_type,
    sum(Value)               AS tokens
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.token.usage'
  AND TimeUnix > now() - INTERVAL 10 MINUTE
GROUP BY user_email, model, token_type
ORDER BY tokens DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** email, model, token type and total consumed in the time window.

---

## 12. Available metrics (sum)

Lists all collected `sum`-type metrics.

```sql
SELECT
    MetricName,
    count() AS n
FROM observability.otel_metrics_sum
GROUP BY MetricName
ORDER BY n DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** metric name (`claude_code.token.usage`, `claude_code.cost.usage`, `claude_code.active_time.total`, `claude_code.session.count`) and datapoint count.

---

## 13. Traces - spans by service and name

Overview of emitted trace spans.

```sql
SELECT
    ServiceName,
    SpanName,
    count() AS n
FROM observability.otel_traces
GROUP BY ServiceName, SpanName
ORDER BY n DESC
FORMAT PrettyCompactMonoBlock
```

**Returns:** service, span name (`claude.hook.ingest`, `claude_code.tool`, `claude_code.llm_request`, etc.) and count.

---

## 14. Hook traces with duration

Shows the latest audit-service ingest spans with duration in milliseconds.

```sql
SELECT
    Timestamp,
    SpanName,
    round(Duration / 1e6, 2) AS duration_ms,
    StatusCode
FROM observability.otel_traces
WHERE ServiceName = 'claude-audit-service'
ORDER BY Timestamp DESC
LIMIT 10
FORMAT PrettyCompactMonoBlock
```

**Returns:** timestamp, span name, duration in ms and status code.

---

## 15. Available attributes for a metric

Useful for discovering which fields can be used in `GROUP BY` / `WHERE`.

```sql
-- Attributes (metric dimensions)
SELECT DISTINCT arrayJoin(mapKeys(Attributes)) AS k
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.token.usage'
FORMAT PrettyCompactMonoBlock

-- ResourceAttributes (infra / service)
SELECT DISTINCT arrayJoin(mapKeys(ResourceAttributes)) AS k
FROM observability.otel_metrics_sum
WHERE MetricName = 'claude_code.token.usage'
FORMAT PrettyCompactMonoBlock
```

**Returns:** list of available keys in `Attributes` (`user.email`, `model`, `type`, `session.id`, `organization.id`, etc.) and `ResourceAttributes` (`host.arch`, `os.type`, `service.name`, etc.).
