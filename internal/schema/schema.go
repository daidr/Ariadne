// Package schema keeps the persistent layout compatible with the existing log.
package schema

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

const Version = 68

//go:embed schema.sql
var ddl string

func Setup(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != Version {
		return fmt.Errorf("projection version %d requires a migrated event replay before opening with version %d", version, Version)
	}
	if version == 0 {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("existing events without projection version require a full replay")
		}
	}
	return Create(ctx, tx)
}

// Create is used after the complete domain projection has arranged a replay.
// Setup remains conservative for consumers that implement only partial projections.
func Create(ctx context.Context, tx *sql.Tx) error {
	sqlText := strings.ReplaceAll(ddl, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ")
	sqlText = strings.ReplaceAll(sqlText, "CREATE VIRTUAL TABLE ", "CREATE VIRTUAL TABLE IF NOT EXISTS ")
	sqlText = strings.ReplaceAll(sqlText, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ")
	sqlText = strings.ReplaceAll(sqlText, "CREATE UNIQUE INDEX ", "CREATE UNIQUE INDEX IF NOT EXISTS ")
	if _, err := tx.ExecContext(ctx, sqlText); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", Version))
	return err
}
