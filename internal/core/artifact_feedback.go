package core

import (
	"context"
	"crypto/sha256"
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
		if mode == "choose" || mode == "verify" || round > 0 || optionGroups[node.str("id")] {
			return true
		}
	}
	return false
}

// Document edits start from the latest known version. Comments use content
// fingerprints so edited comments and user-reopened threads trigger feedback
// idempotently while user-resolved threads stay quiet.
func pendingArtifactFeedback(w *projectionWriter, artifact ArtifactView) ([]any, []string, error) {
	if artifact.Kind != "document" && artifact.Kind != "whiteboard" {
		return nil, nil, nil
	}
	round := w.one("SELECT version, created_at, fingerprints FROM artifact_feedback WHERE artifact_id = ? AND withdrawn_at IS NULL ORDER BY round DESC LIMIT 1", artifact.ID)
	agent := w.one("SELECT version, created_at FROM artifact_versions WHERE artifact_id = ? AND author != 'user' ORDER BY version DESC LIMIT 1", artifact.ID)
	if w.err != nil {
		return nil, nil, w.err
	}
	since := agent
	if round.integer("version") >= agent.integer("version") {
		since = round
	}
	version := max(1, since.integer("version"))
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
		if diff := LineDiff(base.Content, artifact.Content, 4000); diff != nil {
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
	default:
		return name
	}
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
	pos := rectangle(node)
	x, y := pos.x+pos.w/2, pos.y+pos.h/2
	if image := boardImage(nodes, x, y); image != nil {
		return place + ", over " + imagePosition(image, x, y)
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
		point := fmt.Sprintf("at %.0f%% across, %.0f%% down of %s", math.Round(px*100), math.Round(py*100), id)
		if image != nil {
			point = "on " + imagePosition(image, x, y)
		}
		return "pinned " + place + ", " + point
	}
	if kind == "option" {
		return boardPlacement(nodes, index, node)
	}
	words := node.obj("data").str("text")
	if words != "" {
		words = fmt.Sprintf(" %q", words)
	}
	return fmt.Sprintf("on the %s%s (%s) %s", kind, words, id, boardPlacement(nodes, index, node))
}
