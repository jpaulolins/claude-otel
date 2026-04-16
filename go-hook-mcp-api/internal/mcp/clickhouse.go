package mcp

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/ClickHouse/clickhouse-go/v2"
)

// Querier abstracts ClickHouse queries for testing.
type Querier interface {
	Query(ctx context.Context, query string, args ...any) ([]map[string]any, error)
}

type CHClient struct {
	db *sql.DB
}

func NewCHClient(dsn string) (*CHClient, error) {
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, fmt.Errorf("clickhouse open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}
	return &CHClient{db: db}, nil
}

func (c *CHClient) Close() error {
	return c.db.Close()
}

func (c *CHClient) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = values[i]
		}
		results = append(results, row)
	}
	return results, rows.Err()
}
