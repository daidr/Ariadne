package trace

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"runtime"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/secrets"
)

// Export streams the whole conversation in sequence order, independent of the
// trace UI's pagination. Shared request input records are stored once by hash.
func Export(ctx context.Context, repo *core.Repository, id, version string, at time.Time, redact secrets.DiagnosticRedactor, out io.Writer) error {
	return repo.ReadTraceSnapshot(ctx, id, func(snapshot core.TraceSnapshot) error {
		archive := zip.NewWriter(out)
		part := func(name string) (func(any) error, error) {
			entry, err := archive.Create(name)
			if err != nil {
				return nil, err
			}
			return func(value any) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				safe, err := redact.JSON(raw)
				if err != nil {
					return err
				}
				_, err = entry.Write(append(safe, '\n'))
				return err
			}, nil
		}
		manifest, err := part("manifest.json")
		if err != nil {
			return err
		}
		if err := manifest(object{
			"format": "ariadne-trace", "formatVersion": 1, "exportedAt": at.UTC(),
			"environment": object{"ariadne": version, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version()},
			"project":     snapshot.Project, "title": snapshot.Title, "conversation": snapshot.Conversation,
			"files":     object{"events.ndjson": "Turn, request, tool, retry and failure events in sequence order", "transcript.ndjson": "Conversation transcript and attachment metadata", "inputs.ndjson": "Inputs referenced by event hashes: system instructions, tools and provider settings"},
			"redaction": "Known credentials and common secret formats are redacted. Review conversation text and tool logs before sharing.",
			"omissions": []string{"Attachment contents, local files, environment variables and raw provider traffic are not included.", "Older traces may lack provider errors that were not recorded.", "Input hashes are original record identifiers, not checksums of the redacted text."},
		}); err != nil {
			return err
		}
		events, err := part("events.ndjson")
		if err != nil {
			return err
		}
		if err := snapshot.Events(func(event eventstore.Event) error { return events(event) }); err != nil {
			return err
		}
		transcript, err := part("transcript.ndjson")
		if err != nil {
			return err
		}
		if err := snapshot.Transcript(func(entry map[string]any) error { return transcript(entry) }); err != nil {
			return err
		}
		inputs, err := part("inputs.ndjson")
		if err != nil {
			return err
		}
		if err := snapshot.Prompts(func(hash, content string) error { return inputs(object{"hash": hash, "content": content}) }); err != nil {
			return err
		}
		return archive.Close()
	})
}
