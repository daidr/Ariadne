package transcript

import (
	"fmt"
	"strings"
	"testing"
)

func TestImageByteLimitKeepsNewestUpload(t *testing.T) {
	var entries []Entry
	for i := range 6 {
		id := fmt.Sprintf("fil_%d", i)
		entries = append(entries, Entry{"kind": "message", "seq": i, "message": map[string]any{
			"author": "user", "id": fmt.Sprintf("msg_%d", i),
			"attachments": []any{map[string]any{"fileId": id, "name": id + ".png", "mime": "image/png", "size": 5 << 20}},
		}})
	}
	messages := ToMessages(entries, nil, ChatOptions{Images: true})
	if len(messages[5].Images) != 1 || messages[5].Images[0].FileID != "fil_5" {
		t.Fatal("latest image was omitted")
	}
	if len(messages[0].Images) != 0 || !strings.Contains(messages[0].Content, "image size limit") {
		t.Fatalf("old image was not explained and removed: %+v", messages[0])
	}
	var sum int64
	for _, message := range messages {
		for _, image := range message.Images {
			sum += image.Size
		}
	}
	if sum != 20<<20 {
		t.Fatalf("expected 20 MiB of newer images, got %d", sum)
	}
}
