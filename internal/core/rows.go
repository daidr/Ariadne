package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type record map[string]any
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func records(ctx context.Context, db queryer, query string, args ...any) ([]record, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]record, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(record, len(columns))
		for i, name := range columns {
			row[name] = values[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (row record) text(name string) string {
	switch value := row[name].(type) {
	case string:
		return value
	case []byte:
		return string(value)
	}
	return ""
}
func (row record) integer(name string) int64 {
	switch value := row[name].(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	case int:
		return int64(value)
	}
	return 0
}
func (row record) flag(name string) bool    { return row.integer(name) == 1 }
func (row record) present(name string) bool { return row[name] != nil }
func (row record) json(name string, fallback any) (any, error) {
	if !row.present(name) || row.text(name) == "" {
		return fallback, nil
	}
	var result any
	if err := json.Unmarshal([]byte(row.text(name)), &result); err != nil {
		return nil, fmt.Errorf("invalid persisted JSON in %s: %w", name, err)
	}
	return result, nil
}

func usage(row record) map[string]any {
	value := func(key string) any {
		if row[key] == nil {
			return 0
		}
		return row[key]
	}
	return map[string]any{"input": value("input_tokens"), "output": value("output_tokens"), "cached": value("cached_tokens"), "cacheWrite": value("cache_write_tokens"), "reasoning": value("reasoning_tokens"), "cost": map[string]any{"USD": value("cost_usd"), "CNY": value("cost_cny")}}
}
