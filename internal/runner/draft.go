package runner

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

func (turn *turn) clearDraft() {
	if turn.draft != nil {
		turn.draft.Withdraw()
	}
	turn.draft, turn.draftCall = nil, nil
}

func (turn *turn) preview(call *int, name, arguments string) {
	if turn.Drafts == nil || name != "send_message" || call == nil || len(arguments) > 256<<10 {
		return
	}
	text := partialText(arguments)
	if strings.TrimSpace(text) == "" {
		return
	}
	if turn.draft != nil && *turn.draftCall != *call {
		return
	}
	if turn.draft == nil {
		id, err := uuid.NewV7()
		if err != nil {
			return
		}
		handle, err := turn.Drafts.Start(turn.conversation.ProjectID, turn.conversation.ID, "msg_"+id.String())
		if err != nil {
			return
		}
		turn.draft, turn.draftCall = &handle, new(*call)
	}
	_ = turn.draft.Write(text)
}

func partialText(input string) string {
	return partialString(input, "text")
}

func partialString(input, key string) string {
	reader := json.NewDecoder(strings.NewReader(input))
	if token, err := reader.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	for reader.More() {
		token, err := reader.Token()
		if err != nil {
			return ""
		}
		if token == key {
			source := strings.TrimLeft(input[reader.InputOffset():], " \t\r\n:")
			if len(source) == 0 || source[0] != '"' {
				return ""
			}
			end := 1
			for start := 1; start < len(source); {
				next := start + 1
				switch source[start] {
				case '"':
					var text string
					_ = json.Unmarshal([]byte(source[:next]), &text)
					return text
				case '\\':
					next++
					if start+1 < len(source) && source[start+1] == 'u' {
						next += 4
					}
				default:
					if !utf8.FullRuneInString(source[start:]) {
						next = len(source) + 1
					} else {
						_, size := utf8.DecodeRuneInString(source[start:])
						next = start + size
					}
				}
				if next > len(source) {
					break
				}
				end, start = next, next
			}
			var text string
			_ = json.Unmarshal([]byte(source[:end]+"\""), &text)
			return text
		}
		var skip json.RawMessage
		if err := reader.Decode(&skip); err != nil {
			return ""
		}
	}
	return ""
}
