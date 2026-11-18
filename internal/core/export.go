package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
)

// ExportProject streams one consistent database snapshot. It retains one event
// or attachment at a time, rather than assembling the entire export in memory.
// Credentials in the separate settings file are not part of the export.
func (reader Reader) ExportProject(ctx context.Context, id string, writer io.Writer, exportedAt int64) error {
	tx, err := reader.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM projects WHERE id = ?", id).Scan(&name); err == sql.ErrNoRows {
		return domainerr.NotFound(id)
	} else if err != nil {
		return err
	}
	project, err := json.Marshal(map[string]any{"id": id, "name": name})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, `{"ariadne":1,"exportedAt":%d,"project":%s,"events":[`, exportedAt, project); err != nil {
		return err
	}
	writeJSON := func(value any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	}
	if err := func() error {
		rows, err := tx.QueryContext(ctx, "SELECT seq,id,type,project_id,data,at FROM events WHERE project_id = ? ORDER BY seq", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		first := true
		for rows.Next() {
			var event eventstore.Event
			var data string
			if err := rows.Scan(&event.Seq, &event.ID, &event.Type, &event.ProjectID, &data, &event.At); err != nil {
				return err
			}
			event.Data = json.RawMessage(data)
			if !first {
				if _, err := io.WriteString(writer, ","); err != nil {
					return err
				}
			}
			first = false
			if err := writeJSON(event); err != nil {
				return err
			}
		}
		return rows.Err()
	}(); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `],"files":[`); err != nil {
		return err
	}
	if err := func() error {
		rows, err := tx.QueryContext(ctx, "SELECT id,mime,data FROM files WHERE project_id = ? ORDER BY id", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		first := true
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var id, mime string
			var data []byte
			if err := rows.Scan(&id, &mime, &data); err != nil {
				return err
			}
			if !first {
				if _, err := io.WriteString(writer, ","); err != nil {
					return err
				}
			}
			first = false
			if _, err := io.WriteString(writer, `{"id":`); err != nil {
				return err
			}
			if err := writeJSON(id); err != nil {
				return err
			}
			if _, err := io.WriteString(writer, `,"mime":`); err != nil {
				return err
			}
			if err := writeJSON(mime); err != nil {
				return err
			}
			if _, err := io.WriteString(writer, `,"data":"`); err != nil {
				return err
			}
			encoded := base64.NewEncoder(base64.StdEncoding, writer)
			if _, err := encoded.Write(data); err != nil {
				return err
			}
			if err := encoded.Close(); err != nil {
				return err
			}
			if _, err := io.WriteString(writer, `"}`); err != nil {
				return err
			}
		}
		return rows.Err()
	}(); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `]}`); err != nil {
		return err
	}
	return tx.Commit()
}
