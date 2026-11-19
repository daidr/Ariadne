package transcript

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func nonempty(parts []string, separator string) string {
	values := []string{}
	for _, part := range parts {
		if part != "" {
			values = append(values, part)
		}
	}
	return strings.Join(values, separator)
}
func viewable(files []Attachment, options ChatOptions) ([]Attachment, string) {
	shown, notes := []Attachment{}, []string{}
	for _, file := range files {
		if !strings.HasPrefix(file.MIME, "image/") {
			continue
		}
		why := ""
		if !options.Images {
			why = "this model cannot look at images"
		} else if file.MIME != "image/png" && file.MIME != "image/jpeg" && file.MIME != "image/gif" && file.MIME != "image/webp" {
			why = file.MIME + " is not a type models read; PNG, JPEG, GIF and WebP are"
		} else if file.Size > 5*1024*1024 {
			why = "it is larger than 5 MB"
		}
		if why != "" {
			notes = append(notes, `[image "`+file.Name+`" is not shown: `+why+`]`)
		} else {
			shown = append(shown, file)
		}
	}
	if len(notes) > 0 {
		return shown, "\n" + strings.Join(notes, "\n")
	}
	return shown, ""
}
func attachedFiles(files []Attachment, options ChatOptions) (string, []Attachment) {
	parts := []string{}
	for _, file := range files {
		if file.MIME != "text/plain" {
			continue
		}
		body, ok := "", false
		if options.ReadText != nil {
			body, ok = options.ReadText(file.FileID)
		}
		if !ok {
			parts = append(parts, `[attached file "`+file.Name+`" could not be read]`)
			continue
		}
		cut := unitLength(body) > MaxAttachedChars
		if cut {
			body = textutil.HeadUnits(body, MaxAttachedChars)
		}
		block := Fenced("attached_file", body, `name="`+attributeText.Replace(file.Name)+`"`)
		extra := ""
		if cut {
			extra = ", cut after " + decimal(MaxAttachedChars) + " characters"
		}
		parts = append(parts, `The user attached "`+file.Name+`"; its text is inside <`+block.Tag+">"+extra+".\n"+block.Text)
	}
	shown, notes := viewable(files, options)
	if notes != "" {
		parts = append(parts, textutil.Trim(notes))
	}
	if len(shown) > 0 {
		names := []string{}
		for _, file := range shown {
			names = append(names, `"`+file.Name+`"`)
		}
		parts = append(parts, "[the user attached "+strings.Join(names, ", ")+", shown below]")
	}
	return strings.Join(parts, "\n\n"), shown
}
func CommentsText(raw any) string {
	comments := arr(raw)
	if len(comments) == 0 {
		return ""
	}
	items := []string{}
	for _, raw := range comments {
		comment := obj(raw)
		quote := ""
		if value := textutil.Trim(text(comment["quote"])); value != "" {
			quote = " `" + strings.ReplaceAll(value, "`", "'") + "`"
		}
		before := ""
		if comment["side"] == "original" {
			before = " (before your changes)"
		}
		items = append(items, fmt.Sprintf("- %s:%d%s%s\n  %s", text(comment["path"]), int64(num(comment["line"])), before, quote, strings.ReplaceAll(text(comment["body"]), "\n", "\n  ")))
	}
	return "<line_comments>\nThe user commented on lines of your changes (file:line as the changes view showed it, that line's text, then the comment). A line marked \"before your changes\" is from the file as it was, often one you removed or rewrote. Line numbers may have moved since; find the line by its text.\n" + strings.Join(items, "\n") + "\n</line_comments>"
}
func MentionsText(raw any) string {
	items, skills := []string{}, []string{}
	for _, raw := range arr(raw) {
		mention := obj(raw)
		switch text(mention["kind"]) {
		case "skill":
			skills = append(skills, text(mention["name"]))
		case "file":
			items = append(items, "- file "+text(mention["path"])+" (a path in the files you can read; read it if it matters)")
		default:
			items = append(items, `- `+text(mention["kind"])+` "`+text(mention["title"])+`" (`+text(mention["id"])+`)`)
		}
	}
	parts := []string{}
	if len(items) > 0 {
		parts = append(parts, "<mentions>\nThe user pointed at these with @:\n"+strings.Join(items, "\n")+"\n</mentions>")
	}
	if len(skills) > 0 {
		parts = append(parts, "<skills_asked>\nThe user asks you to work by these skills: "+strings.Join(skills, ", ")+". Read each with read_skill before you go on, and follow it.\n</skills_asked>")
	}
	return strings.Join(parts, "\n\n")
}
func openAsksHint(raw any) string {
	asks := arr(raw)
	if len(asks) == 0 {
		return ""
	}
	list := []string{}
	for _, raw := range asks {
		ask := obj(raw)
		list = append(list, "- ["+text(ask["attentionId"])+"] "+text(ask["title"]))
	}
	return "<open_questions>\nYour questions below were still waiting when the user wrote this. If the message answers one or makes it moot, act on it and withdraw that question with withdraw_ask; otherwise leave it open.\n" + strings.Join(list, "\n") + "\n</open_questions>"
}
func relayMessage(message map[string]any, titleOf func(string) string, options ChatOptions) Message {
	images := []Attachment{}
	cited := []string{}
	for _, raw := range arr(message["cites"]) {
		cite := obj(raw)
		files, pictures := attachedFiles(attachments(cite["attachments"]), options)
		images = append(images, pictures...)
		conversation := text(cite["conversationId"])
		where := "to the coordinator"
		if conversation == text(message["conversationId"]) {
			where = "in this thread"
		} else if strings.HasPrefix(conversation, "thr_") {
			where = `in the thread "` + titleOf(conversation) + `"`
		}
		body := nonempty([]string{Defang(text(cite["content"])), MentionsText(cite["mentions"]), files}, "\n\n")
		cited = append(cited, `<user_message wrote="`+where+`" at="`+LocalTime(int64(num(cite["at"])))+`">`+"\n"+body+"\n</user_message>")
	}
	intro := ""
	if len(cited) > 0 {
		intro = "The coordinator passed along the user's own words, exactly as they wrote them; treat them as the user speaking to you.\n" + strings.Join(cited, "\n") + "\n"
	}
	note := ""
	if textutil.Trim(text(message["content"])) != "" {
		note = "<message_from_coordinator>\n" + Defang(text(message["content"])) + "\n</message_from_coordinator>"
	}
	return Message{Role: "user", Content: textutil.Trim(nonempty([]string{intro, note, "[message " + text(message["id"]) + "]"}, "\n")), Images: images}
}
func Clearable(entry Entry) bool {
	return unitLength(text(entry["content"])) > ClearableChars || len(arr(entry["files"])) > 0
}
func ClearedResult(length int) string {
	return "[Ariadne: this result (" + decimal(length) + " characters) was cleared to make room in your context. Run the call again if you need it.]"
}
func unknownResult() string {
	return "The result of this call is unknown: the turn ended (missing result) before it was recorded. It may already have taken effect; check before calling it again."
}
func ToMessages(entries []Entry, titleOf func(string) string, options ChatOptions) []Message {
	if titleOf == nil {
		titleOf = func(id string) string { return id }
	}
	visible := VisibleHistory(entries)
	base := LatestBase(entries)
	baseAt := float64(-1)
	if base != nil {
		baseAt = num(base["seq"])
	}
	keep := func(call any) bool {
		return options.KeepReasoning == nil || call != nil && options.KeepReasoning(text(call))
	}
	out := []Message{}
	if visible.Summary != nil && *visible.Summary != "" {
		reasoning := visible.Reasoning
		var call any
		if visible.Call != nil {
			call = *visible.Call
		}
		if !keep(call) {
			reasoning = nil
		}
		state := ""
		if visible.State != nil {
			state = *visible.State
		}
		budget := float64(KeptUserTokens)
		if visible.UserTokens != nil {
			budget = *visible.UserTokens
		}
		out = SummaryMessages(*visible.Summary, state, reasoning, ShownUserWords(FoldUserWords(entries, visible.UpTo, budget)))
	}
	clearedUpTo := visible.ClearedUpTo
	if options.ClearUpTo != nil {
		clearedUpTo = math.Max(clearedUpTo, *options.ClearUpTo)
	}
	type openStep struct {
		ids []string
		cut bool
	}
	var open *openStep
	held := []Message{}
	instructions := map[string]string{}
	for _, pair := range sectionPairs(base) {
		instructions[pair[0]] = pair[1]
	}
	results := map[string]Entry{}
	for _, entry := range visible.Entries {
		if entry["kind"] == "tool" {
			results[text(entry["toolCallId"])] = entry
		}
	}
	closeStep := func() {
		if open == nil {
			return
		}
		for _, id := range open.ids {
			result := results[id]
			if result != nil && OrderKey(result) <= clearedUpTo && Clearable(result) {
				out = append(out, Message{Role: "tool", ToolCallID: id, Content: ClearedResult(unitLength(text(result["content"]))), IsError: yes(result["isError"])})
				continue
			}
			images, notes := viewable(attachments(result["files"]), options)
			content := unknownResult()
			if result != nil {
				content = text(result["content"])
			}
			out = append(out, Message{Role: "tool", ToolCallID: id, Content: AsOutside(content) + notes, IsError: result == nil || yes(result["isError"]), Images: images})
		}
		if open.cut {
			out = append(out, Message{Role: "user", Content: CutHint})
		}
		open = nil
		out = append(out, held...)
		held = nil
	}
	appendUser := func(message Message) {
		if open != nil {
			held = append(held, message)
		} else {
			out = append(out, message)
		}
	}
	for _, entry := range InModelOrder(visible.Entries) {
		switch entry["kind"] {
		case "step":
			closeStep()
			message := Message{Role: "assistant", Content: text(entry["text"]), ToolCalls: calls(entry["toolCalls"])}
			if keep(entry["call"]) {
				message.Reasoning = reasonings(entry["reasoning"])
			}
			out = append(out, message)
			if len(message.ToolCalls) > 0 {
				ids := []string{}
				for _, call := range message.ToolCalls {
					ids = append(ids, call.ID)
				}
				open = &openStep{ids: ids, cut: yes(entry["cut"])}
			} else if yes(entry["cut"]) {
				out = append(out, Message{Role: "user", Content: CutHint})
			} else if !yes(entry["quiet"]) && textutil.Trim(message.Content) != "" {
				out = append(out, Message{Role: "user", Content: UnsentHint})
			}
		case "prompt":
			if yes(entry["base"]) || options.PromptUpdates != nil && !*options.PromptUpdates || OrderKey(entry) < baseAt {
				continue
			}
			appendUser(Message{Role: "user", Content: PromptUpdate(entry, instructions)})
			for _, pair := range sectionPairs(entry) {
				instructions[pair[0]] = pair[1]
			}
		case "message":
			message := obj(entry["message"])
			if message["kind"] == "relay" {
				appendUser(relayMessage(message, titleOf, options))
				continue
			}
			files, images := attachedFiles(attachments(message["attachments"]), options)
			id := ""
			if message["author"] == "user" {
				id = "[message " + text(message["id"]) + "]"
			}
			appendUser(Message{Role: "user", Content: nonempty([]string{text(message["content"]), CommentsText(message["comments"]), MentionsText(message["mentions"]), files, openAsksHint(entry["openAsks"]), id}, "\n\n"), Images: images})
		case "note":
			note := obj(entry["note"])
			var images []Attachment
			notes := ""
			switch note["type"] {
			case "artifact-feedback":
				images, notes = viewable(attachments(note["images"]), options)
			case "user-in-thread":
				var files string
				files, images = attachedFiles(attachments(note["attachments"]), options)
				if files != "" {
					notes = "\n\n" + files
				}
			}
			appendUser(Message{Role: "user", Content: DescribeNote(note, titleOf) + notes, Images: images})
		}
	}
	closeStep()
	return keepNewestImages(out)
}
func keepNewestImages(messages []Message) []Message {
	kept := 0
	remaining := int64(MaxInputImageBytes)
	for i := len(messages) - 1; i >= 0; i-- {
		message := &messages[i]
		if message.Role != "user" && message.Role != "tool" || len(message.Images) == 0 {
			continue
		}
		var bytes int64
		for _, image := range message.Images {
			bytes += image.Size
		}
		if len(message.Images) <= MaxImages-kept && bytes <= remaining {
			kept += len(message.Images)
			remaining -= bytes
			continue
		}
		var notes []string
		var images []Attachment
		for j := len(message.Images) - 1; j >= 0; j-- {
			file := message.Images[j]
			switch {
			case kept == MaxImages:
				notes = append(notes, `[image "`+file.Name+`" was shown earlier and is no longer attached]`)
			case file.Size > remaining:
				notes = append(notes, `[image "`+file.Name+`" was omitted because of the total image size limit]`)
			default:
				images = append(images, file)
				remaining -= file.Size
				kept++
			}
		}
		slices.Reverse(notes)
		slices.Reverse(images)
		message.Content += "\n" + strings.Join(notes, "\n")
		message.Images = images
	}
	return messages
}
func WithImageData(messages []Message, load func(Attachment) (sdk.Image, bool)) []sdk.Message {
	out := make([]sdk.Message, 0, len(messages))
	for _, message := range messages {
		converted := sdk.Message{Role: message.Role, Content: message.Content}
		if message.Role == "system" || message.Role == "assistant" {
			converted.ToolCalls = message.ToolCalls
			converted.Reasoning = message.Reasoning
		} else {
			for _, file := range message.Images {
				image, ok := load(file)
				if ok {
					converted.Images = append(converted.Images, image)
				} else {
					converted.Content += "\n[image \"" + file.Name + "\" could not be read]"
				}
			}
			if message.Role == "tool" {
				converted.ToolCallID = message.ToolCallID
				converted.IsError = message.IsError
			}
		}
		out = append(out, converted)
	}
	return out
}
