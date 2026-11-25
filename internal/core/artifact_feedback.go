package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

// RemindPendingArtifactFeedback adds context for one more model request in
// this turn. It does not wake the conversation or complete a feedback round.
func (repo *Repository) RemindPendingArtifactFeedback(ctx context.Context, id, turnID string) (bool, error) {
	recorded := false
	err := repo.withTurn(ctx, id, turnID, func(tx *eventstore.Transaction, projectID string) error {
		rows, err := records(ctx, tx.SQL(), `SELECT f.artifact_id, f.round
			FROM artifact_feedback f JOIN artifacts a ON a.id = f.artifact_id
			JOIN conversations c ON c.id = f.notify
			WHERE a.project_id = ? AND c.id = ? AND f.done_at IS NULL AND f.withdrawn_at IS NULL
			  AND (f.created_at <= c.turn_started_at OR c.pending_seq = 0)
			  AND c.wait_id IS NULL
			  AND NOT EXISTS (SELECT 1 FROM attentions WHERE conversation_id = c.id AND closed = 0)
			ORDER BY f.created_at, f.artifact_id, f.round LIMIT 20`, projectID, id)
		if err != nil || len(rows) == 0 {
			return err
		}
		rounds := make([]object, 0, len(rows))
		for _, row := range rows {
			rounds = append(rounds, object{"artifactId": row["artifact_id"], "round": row["round"]})
		}
		err = appendChanges(tx, projectID, []change{{"conversation.feedback_reminder", object{"conversationId": id, "turnId": turnID, "rounds": rounds}}})
		recorded = err == nil
		return err
	})
	return recorded, err
}

// FeedbackCandidates pages over changed artifacts without reading their bodies.
// SendPendingArtifactFeedback rechecks the baseline in the write transaction.
func (reader Reader) FeedbackCandidates(ctx context.Context, after string, cutoff time.Time) ([]string, error) {
	rows, err := records(ctx, reader.DB, `
		SELECT a.id FROM artifacts a
		WHERE a.id > ? AND a.kind IN ('document', 'whiteboard')
		  AND max(a.updated_at, a.commented_at) <= ?
		  AND (
		    (a.updated_by = 'user' AND a.version > max(1,
		      coalesce((SELECT max(version) FROM artifact_feedback WHERE artifact_id = a.id AND withdrawn_at IS NULL), 0),
		      coalesce((SELECT max(version) FROM artifact_versions WHERE artifact_id = a.id AND author <> 'user'), 0)))
		    OR a.commented_at > coalesce((SELECT max(created_at) FROM artifact_feedback WHERE artifact_id = a.id AND withdrawn_at IS NULL), 0)
		      AND (
		        EXISTS (
		          SELECT 1 FROM artifact_comments c, json_each(c.comments) comment
		          WHERE c.artifact_id = a.id AND json_extract(comment.value, '$.author') = 'user'
		            AND max(
		              coalesce(json_extract(comment.value, '$.createdAt'), 0),
		              coalesce(json_extract(comment.value, '$.editedAt'), 0)
		            ) > coalesce((SELECT max(created_at) FROM artifact_feedback WHERE artifact_id = a.id AND withdrawn_at IS NULL), 0))
		        OR EXISTS (
		          SELECT 1 FROM artifact_comments c
		          WHERE c.artifact_id = a.id AND c.reopened_by = 'user' AND c.resolved_by IS NULL
		            AND coalesce(c.reopened_at, 0) > coalesce((SELECT max(created_at) FROM artifact_feedback WHERE artifact_id = a.id AND withdrawn_at IS NULL), 0))
		      )
		  )
		ORDER BY a.id LIMIT 64`, after, cutoff.UnixMilli())
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.text("id"))
	}
	return ids, nil
}

func commentFingerprint(threadID, commentID string, createdAt, editedAt int64, body string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(createdAt, 10) + "|" + strconv.FormatInt(editedAt, 10) + "|" + body))
	return "c:" + threadID + ":" + commentID + ":" + hex.EncodeToString(sum[:8])
}

func reopenFingerprint(threadID string, reopenedAt int64) string {
	return "r:" + threadID + ":" + strconv.FormatInt(reopenedAt, 10)
}

func artifactCommentStates(ctx context.Context, db queryer, id string) ([]object, error) {
	rows, err := records(ctx, db, "SELECT * FROM artifact_comments WHERE artifact_id = ? ORDER BY created_at, rowid", id)
	if err != nil {
		return nil, err
	}
	result := make([]object, 0, len(rows))
	for _, row := range rows {
		anchor, err := row.json("anchor", nil)
		if err != nil {
			return nil, err
		}
		comments, err := row.json("comments", []any{})
		if err != nil {
			return nil, err
		}
		item := object{
			"id":        row["id"],
			"quote":     row["quote"],
			"anchor":    anchor,
			"comments":  comments,
			"createdAt": row["created_at"],
		}
		if row.text("resolved_by") != "" {
			item["resolved"] = object{"by": row["resolved_by"], "at": row.integer("resolved_at")}
		}
		if row.text("reopened_by") != "" {
			item["reopened"] = object{"by": row["reopened_by"], "at": row.integer("reopened_at")}
		}
		result = append(result, item)
	}
	return result, nil
}

// HasPendingDecisionGroups reports whether a whiteboard has at least one active
// (unconfirmed) decision group, in which case automatic idle sending pauses so
// the user can review and submit the round explicitly.
func HasPendingDecisionGroups(content string) bool {
	nodes, err := boardNodes(content)
	if err != nil {
		return false
	}
	index := indexBoard(nodes)
	optionGroups := map[string]bool{}
	for _, node := range nodes {
		if node.str("type") != "option" {
			continue
		}
		if gid := optionGroupID(node, index); gid != "" {
			optionGroups[gid] = true
		}
	}
	for _, node := range nodes {
		if node.str("type") != "group" {
			continue
		}
		data := node.obj("data")
		if data.yes("confirmed") {
			continue
		}
		round, _ := number(data["round"])
		mode := data.str("mode")
		if mode == "choose" || round > 0 || optionGroups[node.str("id")] {
			return true
		}
	}
	return false
}

type parsedDocumentAsk struct {
	id        string
	question  string
	options   map[string]string
	selected  []string
	note      string
	confirmed bool
}

func parseDocumentAsks(content string) ([]parsedDocumentAsk, string) {
	var asks []parsedDocumentAsk
	normalized := documentAskBlockPattern.ReplaceAllStringFunc(content, func(match string) string {
		norm, ok := parseAskMatch(match)
		if !ok {
			return match
		}
		id, _ := norm["id"].(string)
		question, _ := norm["question"].(string)
		optLabels := map[string]string{}
		if list, ok := norm["options"].([]any); ok {
			for _, item := range list {
				if entry, ok := item.(map[string]any); ok {
					oid, _ := entry["id"].(string)
					label, _ := entry["label"].(string)
					if oid != "" && label != "" {
						optLabels[oid] = label
					}
				}
			}
		}
		selected := []string{}
		if list, ok := norm["selected"].([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok && s != "" {
					selected = append(selected, s)
				}
			}
		}
		note, _ := norm["note"].(string)
		confirmed, _ := norm["confirmed"].(bool)
		asks = append(asks, parsedDocumentAsk{
			id:        id,
			question:  question,
			options:   optLabels,
			selected:  selected,
			note:      strings.TrimSpace(note),
			confirmed: confirmed,
		})
		// Re-serialize into the canonical ```ask block shape with user answer
		// fields stripped so answering an inline question (even when created via
		// <ask> tags or shorthand options/omitted defaults) does not emit a noisy
		// raw JSON edit diff alongside the structured answer item.
		if encoded, err := MarshalCanonicalAsk(norm, true); err == nil {
			return "```ask\n" + encoded + "\n```"
		}
		return match
	})
	return asks, normalized
}

// HasPendingDocumentAsks reports whether a document has at least one active
// (unconfirmed) inline ask block.
func HasPendingDocumentAsks(content string) bool {
	asks, _ := parseDocumentAsks(content)
	for _, ask := range asks {
		if !ask.confirmed {
			return true
		}
	}
	return false
}

func describeDocumentAskChanges(previous, current string) ([]object, string, string) {
	prevAsks, prevNorm := parseDocumentAsks(previous)
	currAsks, currNorm := parseDocumentAsks(current)
	byID := make(map[string]parsedDocumentAsk, len(prevAsks))
	for _, ask := range prevAsks {
		byID[ask.id] = ask
	}
	answers := []object{}
	for _, ask := range currAsks {
		prev, existed := byID[ask.id]
		prevSel := slices.Clone(prev.selected)
		currSel := slices.Clone(ask.selected)
		slices.Sort(prevSel)
		slices.Sort(currSel)
		if existed && slices.Equal(prevSel, currSel) && prev.note == ask.note {
			continue
		}
		if !existed && len(ask.selected) == 0 && ask.note == "" {
			continue
		}
		values := make([]string, 0, len(ask.selected))
		for _, sid := range ask.selected {
			if label := ask.options[sid]; label != "" {
				values = append(values, label)
			} else {
				values = append(values, sid)
			}
		}
		entry := object{
			"id":       ask.id,
			"question": ask.question,
			"values":   values,
		}
		if ask.note != "" {
			entry["note"] = ask.note
		}
		answers = append(answers, entry)
	}
	return answers, prevNorm, currNorm
}

type DocumentAskBaselineEntry struct {
	ID       string
	Selected []string
	Note     string
}

type DocumentAskBaseline struct {
	Version int64
	Asks    []DocumentAskBaselineEntry
}

func artifactFeedbackBaseline(w *projectionWriter, artifactID string) (int64, record, error) {
	round := w.one("SELECT version, created_at, fingerprints FROM artifact_feedback WHERE artifact_id = ? AND withdrawn_at IS NULL ORDER BY round DESC LIMIT 1", artifactID)
	agent := w.one("SELECT version, created_at FROM artifact_versions WHERE artifact_id = ? AND author != 'user' ORDER BY version DESC LIMIT 1", artifactID)
	if w.err != nil {
		return 0, nil, w.err
	}
	since := agent
	if round.integer("version") >= agent.integer("version") {
		since = round
	}
	return max(1, since.integer("version")), round, nil
}

func documentAskBaseline(ctx context.Context, tx *sql.Tx, artifact ArtifactView) (*DocumentAskBaseline, error) {
	if artifact.Kind != "document" {
		return nil, nil
	}
	w := &projectionWriter{ctx: ctx, tx: tx}
	baselineVersion, _, err := artifactFeedbackBaseline(w, artifact.ID)
	if err != nil {
		return nil, err
	}
	baseContent := artifact.Content
	if baselineVersion != artifact.Version {
		base, err := getArtifact(ctx, tx, artifact.ID, &baselineVersion)
		if err != nil {
			return nil, err
		}
		baseContent = base.Content
	}
	parsed, _ := parseDocumentAsks(baseContent)
	asks := make([]DocumentAskBaselineEntry, 0, len(parsed))
	for _, ask := range parsed {
		selected := slices.Clone(ask.selected)
		slices.Sort(selected)
		asks = append(asks, DocumentAskBaselineEntry{
			ID:       ask.id,
			Selected: selected,
			Note:     ask.note,
		})
	}
	return &DocumentAskBaseline{
		Version: baselineVersion,
		Asks:    asks,
	}, nil
}

// Document edits start from the latest known version. Comments use content
// fingerprints so edited comments and user-reopened threads trigger feedback
// idempotently while user-resolved threads stay quiet.
func pendingArtifactFeedback(w *projectionWriter, artifact ArtifactView) ([]any, []string, error) {
	if artifact.Kind != "document" && artifact.Kind != "whiteboard" {
		return nil, nil, nil
	}
	version, round, err := artifactFeedbackBaseline(w, artifact.ID)
	if err != nil {
		return nil, nil, err
	}
	created := round.integer("created_at")
	seen := map[string]bool{}
	seenCommentPrefix := map[string]bool{}
	if round != nil {
		if rawFPs, ok := w.parsed(round, "fingerprints", []any{}).([]any); ok {
			for _, v := range rawFPs {
				if fp, ok := v.(string); ok && fp != "" {
					seen[fp] = true
					if strings.HasPrefix(fp, "c:") {
						if lastColon := strings.LastIndex(fp, ":"); lastColon > 2 {
							seenCommentPrefix[fp[:lastColon+1]] = true
						}
					}
				}
			}
		}
	}
	if w.err != nil {
		return nil, nil, w.err
	}
	legacyBaseline := len(seen) == 0 && created > 0
	base, err := getArtifact(w.ctx, w.tx, artifact.ID, &version)
	if err != nil {
		return nil, nil, err
	}
	items := []any{}
	var board []object
	if artifact.Kind == "document" {
		answers, baseNorm, currentNorm := describeDocumentAskChanges(base.Content, artifact.Content)
		if len(answers) > 0 {
			items = append(items, object{"kind": "answer", "answers": answers})
		}
		if diff := LineDiff(baseNorm, currentNorm, 4000); diff != nil {
			items = append(items, object{"kind": "edit", "diff": *diff})
		}
	} else {
		board, err = boardNodes(artifact.Content)
		if err != nil {
			return nil, nil, err
		}
		if base.Content != artifact.Content {
			previous, err := boardNodes(base.Content)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, describeBoardChanges(previous, board)...)
		}
	}
	threads, err := artifactCommentStates(w.ctx, w.tx, artifact.ID)
	if err != nil {
		return nil, nil, err
	}
	currentFingerprints := []string{}
	for _, thread := range threads {
		threadID := thread.str("id")
		where := fmt.Sprintf("on the text %q", thread.str("quote"))
		anchor := thread.obj("anchor")
		switch {
		case anchor.str("block") != "":
			where = fmt.Sprintf("in whiteboard block %s, on %s", anchor.str("block"), anchorItem(anchor))
		case artifact.Kind == "whiteboard":
			where = boardAnchor(board, anchor, threadID)
		}
		for i, comment := range objects(thread["comments"]) {
			if comment.str("author") != "user" {
				continue
			}
			commentID := comment.str("id")
			createdAtF, _ := number(comment["createdAt"])
			editedAtF, _ := number(comment["editedAt"])
			createdAt, editedAt := int64(createdAtF), int64(editedAtF)
			body := comment.str("body")
			fp := commentFingerprint(threadID, commentID, createdAt, editedAt, body)
			currentFingerprints = append(currentFingerprints, fp)
			if seen[fp] {
				continue
			}
			if legacyBaseline && createdAt <= created && editedAt <= created {
				continue
			}
			action := "reply"
			if i == 0 {
				action = "create"
			}
			if editedAt > 0 && (seenCommentPrefix["c:"+threadID+":"+commentID+":"] || (legacyBaseline && createdAt <= created)) {
				action = "edit"
			}
			items = append(items, object{
				"kind": "comment", "threadId": threadID, "commentId": commentID,
				"action": action, "body": body, "where": where,
			})
		}
		reopened := thread.obj("reopened")
		if thread["resolved"] == nil && reopened.str("by") == "user" {
			reopenedAtF, _ := number(reopened["at"])
			reopenedAt := int64(reopenedAtF)
			if reopenedAt > 0 {
				rfp := reopenFingerprint(threadID, reopenedAt)
				currentFingerprints = append(currentFingerprints, rfp)
				if !seen[rfp] && !(legacyBaseline && reopenedAt <= created) {
					items = append(items, object{
						"kind": "comment", "threadId": threadID,
						"action": "reopen", "body": "", "where": where,
					})
				}
			}
		}
	}
	slices.Sort(currentFingerprints)
	currentFingerprints = slices.Compact(currentFingerprints)
	return items, currentFingerprints, nil
}

func boardNodes(content string) ([]object, error) {
	var board struct {
		Nodes []object `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(content), &board); err != nil {
		return nil, err
	}
	return board.Nodes, nil
}

func boardNodeName(node object) string {
	if name := node.obj("data").str("title"); name != "" {
		return name
	}
	return node.str("id")
}

func boardNodeKind(node object) string {
	switch name := node.str("type"); name {
	case "ink":
		return "drawing"
	case "sticky":
		return "sticky note"
	case "default":
		return "box"
	case "prototype":
		return "prototype"
	default:
		return name
	}
}

func formatTweakScalar(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return val, true
	case bool:
		if val {
			return "true", true
		}
		return "false", true
	case float64, float32, int, int64, int32, uint, uint64, json.Number:
		n, ok := number(val)
		if !ok {
			return "", false
		}
		return strconv.FormatFloat(n, 'f', -1, 64), true
	default:
		return "", false
	}
}

func describePrototypeTweaks(previous []object, oldIndex map[string]object, current []object, newIndex map[string]object) []object {
	_ = previous
	entries := []object{}
	for _, node := range current {
		if node.str("type") != "prototype" {
			continue
		}
		id := node.str("id")
		data := node.obj("data")
		currValues := asObject(data["tweakValues"])
		var prevValues object
		if prior := oldIndex[id]; prior != nil {
			prevValues = asObject(prior.obj("data")["tweakValues"])
		}
		if len(currValues) == 0 && len(prevValues) == 0 {
			continue
		}
		type tweakMeta struct {
			label string
			def   string
			hasD  bool
			unit  string
			order int
		}
		metaByKey := map[string]tweakMeta{}
		keys := []string{}
		seenKey := map[string]bool{}
		if specs, ok := data["tweaks"].([]any); ok {
			for idx, raw := range specs {
				spec := asObject(raw)
				if spec == nil {
					continue
				}
				key := spec.str("key")
				if key == "" {
					continue
				}
				def, hasDef := formatTweakScalar(spec["default"])
				label := spec.str("label")
				if label == "" {
					label = key
				}
				metaByKey[key] = tweakMeta{
					label: label,
					def:   def,
					hasD:  hasDef,
					unit:  spec.str("unit"),
					order: idx,
				}
				if !seenKey[key] {
					seenKey[key] = true
					keys = append(keys, key)
				}
			}
		}
		extraKeys := []string{}
		for k := range currValues {
			if !seenKey[k] {
				seenKey[k] = true
				extraKeys = append(extraKeys, k)
			}
		}
		for k := range prevValues {
			if !seenKey[k] {
				seenKey[k] = true
				extraKeys = append(extraKeys, k)
			}
		}
		slices.Sort(extraKeys)
		keys = append(keys, extraKeys...)

		changes := []object{}
		for _, key := range keys {
			meta, hasMeta := metaByKey[key]
			currStr, hasCurr := formatTweakScalar(currValues[key])
			if !hasCurr && hasMeta && meta.hasD {
				currStr = meta.def
			}
			prevStr, hasPrev := formatTweakScalar(prevValues[key])
			if !hasPrev && hasMeta && meta.hasD {
				prevStr = meta.def
			}
			if currStr == prevStr {
				continue
			}
			label := key
			if hasMeta && meta.label != "" {
				label = meta.label
			}
			change := object{
				"key":   key,
				"label": label,
				"value": currStr,
			}
			if prevStr != "" {
				change["previous"] = prevStr
			}
			if hasMeta && meta.unit != "" {
				change["unit"] = meta.unit
			}
			changes = append(changes, change)
		}
		if len(changes) == 0 {
			continue
		}
		entry := object{
			"nodeId":  id,
			"title":   boardNodeName(node),
			"changes": changes,
		}
		if opt := boardOption(current, newIndex, node); opt != nil {
			entry["optionId"] = opt.str("id")
			entry["optionTitle"] = boardNodeName(opt)
		}
		entries = append(entries, entry)
	}
	return entries
}

func optionGroupID(node object, index map[string]object) string {
	if group := node.obj("data").str("group"); group != "" {
		return group
	}
	if parent := node.str("parent"); parent != "" && index[parent].str("type") == "group" {
		return parent
	}
	return ""
}

func groupNodeTitle(group object) string {
	if group == nil {
		return ""
	}
	data := group.obj("data")
	if label := data.str("label"); label != "" {
		return label
	}
	return data.str("title")
}

func describeBoardChanges(previous, current []object) []any {
	old := indexBoard(previous)
	new := indexBoard(current)
	chosen := func(nodes []object, index map[string]object) ([]string, []object) {
		keys, values := []string{}, []object{}
		for _, node := range nodes {
			if node.str("type") == "option" && node.obj("data").yes("chosen") {
				gid := optionGroupID(node, index)
				keys = append(keys, gid+":"+node.str("id"))
				entry := object{"id": node["id"], "title": boardNodeName(node)}
				if gid != "" {
					entry["group"] = gid
					if title := groupNodeTitle(index[gid]); title != "" {
						entry["groupTitle"] = title
					}
				}
				values = append(values, entry)
			}
		}
		return keys, values
	}
	items := []any{}
	before, _ := chosen(previous, old)
	after, now := chosen(current, new)
	slices.Sort(before)
	slices.Sort(after)
	if !slices.Equal(before, after) {
		items = append(items, object{"kind": "choice", "chosen": now})
	}
	if tweaks := describePrototypeTweaks(previous, old, current, new); len(tweaks) > 0 {
		items = append(items, object{"kind": "tweak", "tweaks": tweaks})
	}
	changes := []string{}
	for _, node := range current {
		id, kind := node.str("id"), boardNodeKind(node)
		words := node.obj("data").str("text")
		prior := old[id]
		switch {
		case prior == nil:
			items = append(items, object{"kind": "mark", "nodeId": id, "type": kind, "text": words, "where": boardPlacement(current, new, node)})
		case kind != "option":
			if original := prior.obj("data").str("text"); words != original {
				changes = append(changes, fmt.Sprintf("They rewrote %s from %q to %q.", id, original, words))
			}
		case boardNodeName(prior) != boardNodeName(node):
			changes = append(changes, fmt.Sprintf("They renamed option %s to %q.", id, boardNodeName(node)))
		}
	}
	for _, node := range previous {
		id, kind := node.str("id"), boardNodeKind(node)
		if new[id] != nil {
			continue
		}
		words := node.obj("data").str("text")
		if words == "" && kind == "option" {
			words = boardNodeName(node)
		}
		if words != "" {
			words = fmt.Sprintf(" %q", words)
		}
		changes = append(changes, "They removed the "+kind+" "+id+words+".")
	}
	if len(changes) > 0 {
		items = append(items, object{"kind": "board", "changes": changes})
	}
	return items
}

func indexBoard(nodes []object) map[string]object {
	index := make(map[string]object, len(nodes))
	for _, node := range nodes {
		index[node.str("id")] = node
	}
	return index
}

type boardRect struct{ x, y, w, h float64 }

func rectangle(node object) boardRect {
	x, _ := number(node["x"])
	y, _ := number(node["y"])
	w, width := number(node["width"])
	h, height := number(node["height"])
	if !width || w <= 0 {
		w = 160
	}
	if !height || h <= 0 {
		h = 90
	}
	return boardRect{x, y, w, h}
}

func (box boardRect) contains(x, y float64) bool {
	return x >= box.x && x <= box.x+box.w && y >= box.y && y <= box.y+box.h
}

func boardOption(nodes []object, index map[string]object, node object) object {
	id := node.str("id")
	seen := map[string]bool{id: true}
	for parent := node.str("parent"); parent != "" && !seen[parent]; {
		seen[parent] = true
		group := index[parent]
		if group.str("type") == "option" {
			return group
		}
		parent = group.str("parent")
	}
	pos := rectangle(node)
	for _, candidate := range nodes {
		if candidate.str("id") != id && candidate.str("type") == "option" &&
			rectangle(candidate).contains(pos.x+pos.w/2, pos.y+pos.h/2) {
			return candidate
		}
	}
	return nil
}

func boardImage(nodes []object, x, y float64) object {
	for _, candidate := range nodes {
		if candidate.str("type") == "image" && rectangle(candidate).contains(x, y) {
			return candidate
		}
	}
	return nil
}

func boardPrototype(nodes []object, x, y float64) object {
	for _, candidate := range nodes {
		if candidate.str("type") == "prototype" && rectangle(candidate).contains(x, y) {
			return candidate
		}
	}
	return nil
}

func prototypePosition(proto object, x, y float64) string {
	box := rectangle(proto)
	title := proto.obj("data").str("title")
	if title != "" {
		title = fmt.Sprintf(" %q", title)
	}
	px, py := math.Round((x-box.x)/box.w*100), math.Round((y-box.y)/box.h*100)
	return fmt.Sprintf("prototype%s (%s) at %.0f%% across, %.0f%% down", title, proto.str("id"), px, py)
}

func describeCommentElement(elem object) string {
	if elem == nil {
		return ""
	}
	parts := []string{}
	roleOrTag := elem.str("role")
	if roleOrTag == "" {
		roleOrTag = elem.str("tag")
	}
	if roleOrTag != "" {
		parts = append(parts, roleOrTag)
	}
	if label := elem.str("label"); label != "" {
		parts = append(parts, fmt.Sprintf("%q", label))
	}
	if selector := elem.str("selector"); selector != "" {
		parts = append(parts, "("+selector+")")
	}
	return strings.Join(parts, " ")
}

func boardPlacement(nodes []object, index map[string]object, node object) string {
	id, kind := node.str("id"), node.str("type")
	if kind == "option" {
		return fmt.Sprintf("on option %q (%s) as a whole", boardNodeName(node), id)
	}
	place := "on the board, in no option"
	if option := boardOption(nodes, index, node); option != nil {
		place = fmt.Sprintf("in option %q (%s)", boardNodeName(option), option.str("id"))
	}
	if kind == "image" {
		return place + ", the image " + id
	}
	if kind == "prototype" {
		return fmt.Sprintf("%s, prototype %q (%s)", place, boardNodeName(node), id)
	}
	pos := rectangle(node)
	x, y := pos.x+pos.w/2, pos.y+pos.h/2
	if image := boardImage(nodes, x, y); image != nil {
		return place + ", over " + imagePosition(image, x, y)
	}
	if proto := boardPrototype(nodes, x, y); proto != nil {
		return place + ", over " + prototypePosition(proto, x, y)
	}
	return place
}

func imagePosition(image object, x, y float64) string {
	box := rectangle(image)
	alt := image.obj("data").str("alt")
	if alt != "" {
		alt = fmt.Sprintf(" %q", alt)
	}
	px, py := math.Round((x-box.x)/box.w*100), math.Round((y-box.y)/box.h*100)
	return fmt.Sprintf("the image%s (%s) at %.0f%% across, %.0f%% down", alt, image.str("id"), px, py)
}

func anchorItem(anchor object) string {
	if edge := anchor.str("edge"); edge != "" {
		return "connector " + edge
	}
	return "item " + anchor.str("node")
}

func boardAnchor(nodes []object, anchor object, thread string) string {
	if anchor == nil {
		return "on the board"
	}
	if edge := anchor.str("edge"); edge != "" {
		return "on the connector " + edge
	}
	index := indexBoard(nodes)
	id := anchor.str("node")
	node := index[id]
	if node == nil {
		return "on " + id + ", which is gone now"
	}
	kind := boardNodeKind(node)
	if pin := node.obj("commentPins").obj(thread); pin != nil {
		place := "on the board"
		option := boardOption(nodes, index, node)
		if kind == "option" {
			option = node
		}
		if option != nil {
			place = fmt.Sprintf("in option %q (%s)", boardNodeName(option), option.str("id"))
		}
		box := rectangle(node)
		px, _ := number(pin["x"])
		py, _ := number(pin["y"])
		x, y := box.x+px*box.w, box.y+py*box.h
		image := node
		if kind != "image" {
			image = boardImage(nodes, x, y)
		}
		proto := node
		if kind != "prototype" {
			proto = boardPrototype(nodes, x, y)
		}
		point := fmt.Sprintf("at %.0f%% across, %.0f%% down of %s", math.Round(px*100), math.Round(py*100), id)
		if image != nil {
			point = "on " + imagePosition(image, x, y)
		} else if proto != nil {
			point = "on " + prototypePosition(proto, x, y)
		}
		if elemDesc := describeCommentElement(anchor.obj("element")); elemDesc != "" {
			point = "element " + elemDesc + " " + point
		}
		return "pinned " + place + ", " + point
	}
	if kind == "option" {
		return boardPlacement(nodes, index, node)
	}
	if elemDesc := describeCommentElement(anchor.obj("element")); elemDesc != "" {
		return fmt.Sprintf("on element %s of %s (%s) %s", elemDesc, kind, id, boardPlacement(nodes, index, node))
	}
	words := node.obj("data").str("text")
	if words != "" {
		words = fmt.Sprintf(" %q", words)
	}
	return fmt.Sprintf("on the %s%s (%s) %s", kind, words, id, boardPlacement(nodes, index, node))
}
