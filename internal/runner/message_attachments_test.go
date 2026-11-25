package runner

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/trace"
	"github.com/daidr/Ariadne/internal/transcript"
)

func TestUserThreadAttachmentsReachCoordinatorAndRelayedThread(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Relay target"})
	if err != nil {
		t.Fatal(err)
	}
	store := files.Store{Events: repo.Store}
	const body = "unique content of notes.txt"
	document, err := store.Save(ctx, project.ID, "text/plain", []byte(body), true)
	if err != nil {
		t.Fatal(err)
	}
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+X2JcAAAAASUVORK5CYII="
	pixels, err := base64.StdEncoding.DecodeString(png)
	if err != nil {
		t.Fatal(err)
	}
	image, err := store.Save(ctx, project.ID, "image/png", pixels, true)
	if err != nil {
		t.Fatal(err)
	}
	message, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: thread.ID, Author: "user",
		Attachments: []any{attachment(document, "notes.txt"), attachment(image, "screenshot.png")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{thread.ID, project.CoordinatorID, target.ID} {
		if id == target.ID {
			if _, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
				ConversationID: target.ID, Author: "coordinator", Kind: "relay", Cites: []string{message.ID},
			}); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := repo.ListTranscript(ctx, id, core.TranscriptOptions{ModelContext: true})
		if err != nil {
			t.Fatal(err)
		}
		entries := make([]transcript.Entry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, row)
		}
		options, title := trace.ModelInput(ctx, repo, project.ID, nil, true)
		turn := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{ID: id, ProjectID: project.ID}}
		var receivedText, receivedImage bool
		for _, input := range turn.withImages(ctx, transcript.ToMessages(entries, title, options)) {
			receivedText = receivedText || strings.Contains(input.Content, body)
			for _, sent := range input.Images {
				receivedImage = receivedImage || sent.MIME == "image/png" && sent.Data == png
			}
		}
		if !receivedText || !receivedImage {
			t.Errorf("conversation %s received text=%t image=%t", id, receivedText, receivedImage)
		}
	}
}
