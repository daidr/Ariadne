package collab

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/antst/go-yjs/crdt"
	"github.com/google/uuid"
)

func ReadDocument(doc *crdt.Doc) (string, error) {
	blocks, err := readDocumentBlocks(doc.GetMap("document"))
	if err != nil {
		return "", err
	}
	return renderDocument(blocks), nil
}

func normalizeDocument(markdown string) (string, error) {
	blocks, err := parseMarkdown(markdown)
	if err != nil {
		return "", err
	}
	return renderDocument(blocks), nil
}

func readDocumentBlocks(root *crdt.YMap) ([]documentBlock, error) {
	entries := root.Entries()
	parents := make(map[string]string, len(entries))
	for id, raw := range entries {
		node, ok := raw.(*crdt.YMap)
		if !ok {
			continue
		}
		parent := stringField(node, "parent")
		if _, exists := entries[parent].(*crdt.YMap); !exists || parent == id {
			parent = ""
		}
		parents[id] = parent
	}
	settled := map[string]bool{}
	for id := range parents {
		path, positions := []string{}, map[string]int{}
		for at := id; at != "" && !settled[at]; at = parents[at] {
			if found, cycle := positions[at]; cycle {
				parents[slices.Min(path[found:])] = ""
				break
			}
			positions[at] = len(path)
			path = append(path, at)
		}
		for _, at := range path {
			settled[at] = true
		}
	}
	children := map[string][]string{}
	for _, id := range orderedKeys(root, "order") {
		parent := parents[id]
		children[parent] = append(children[parent], id)
	}
	var descend func(string, int) ([]documentBlock, error)
	descend = func(parent string, depth int) ([]documentBlock, error) {
		if depth > maxDocumentDepth && len(children[parent]) > 0 {
			return nil, errors.New("document has too many nested blocks")
		}
		nodes := make([]documentBlock, 0, len(children[parent]))
		for _, id := range children[parent] {
			node := entries[id].(*crdt.YMap)
			text, _ := node.Get("text").(*crdt.YText)
			fields, _ := node.Get("props").(*crdt.YMap)
			props := contentObject{}
			if fields != nil {
				props = normalizeJSON(fields.ToJSON()).(contentObject)
			}
			kind := stringField(node, "type")
			block := documentBlock{id: id, kind: kind, props: props, inline: readDocumentText(text)}
			switch kind {
			case "whiteboard":
				if fields != nil {
					block.props["board"] = readNestedBoard(fields.Get("board"))
				}
			case "table":
				table, err := readDocumentTable(node, props)
				if err != nil {
					return nil, err
				}
				block.table = table
			}
			kids, err := descend(id, depth+1)
			if err != nil {
				return nil, err
			}
			block.children = kids
			nodes = append(nodes, block)
		}
		return nodes, nil
	}
	return descend("", 0)
}

func stringField(fields *crdt.YMap, key string) string {
	value, _ := fields.Get(key).(string)
	return value
}

func ensureMap(fields *crdt.YMap, key string) *crdt.YMap {
	existing, _ := fields.Get(key).(*crdt.YMap)
	if existing == nil {
		existing = crdt.NewYMap(nil)
		fields.Set(key, existing)
	}
	return existing
}

func readDocumentText(text *crdt.YText) []documentInline {
	if text == nil {
		return nil
	}
	var result []documentInline
	for _, op := range text.ToDelta(nil, nil, nil) {
		if op.InsertText != "" {
			attrs := normalizeJSON(op.Attributes).(contentObject)
			result = append(result, documentInline{text: op.InsertText, marks: attrs})
		} else if toObject(op.Insert)["node"] == "hardBreak" {
			result = append(result, documentInline{br: true})
		}
	}
	return result
}

type inlineCharacter struct {
	value rune
	marks contentObject
	br    bool
}

func inlineCharacters(inline []documentInline) []inlineCharacter {
	var result []inlineCharacter
	for _, run := range inline {
		if run.br {
			result = append(result, inlineCharacter{br: true})
		}
		for _, char := range run.text {
			result = append(result, inlineCharacter{value: char, marks: run.marks})
		}
	}
	return result
}

func (character inlineCharacter) length() int {
	if character.br {
		return 1
	}
	return utf16.RuneLen(character.value)
}

func sameMarks(a, b contentObject) bool {
	for key, value := range a {
		if !strings.HasPrefix(key, "comment:") && !equalJSON(value, b[key]) {
			return false
		}
	}
	for key, value := range b {
		if !strings.HasPrefix(key, "comment:") && !equalJSON(value, a[key]) {
			return false
		}
	}
	return true
}

func sameCharacter(a, b inlineCharacter) bool {
	return a.value == b.value && a.br == b.br && sameMarks(a.marks, b.marks)
}

func markedLength(chars []inlineCharacter) int {
	units := 0
	for _, char := range chars {
		units += char.length()
	}
	return units
}

func markObject(fields contentObject) crdt.Object {
	object := crdt.MakeObject()
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		object.Set(key, sharedJSON(fields[key]))
	}
	return object
}

func insertDocumentText(text *crdt.YText, offset int, runs []documentInline) {
	for _, run := range runs {
		marks := markObject(run.marks)
		if run.br {
			text.InsertEmbed(offset, crdt.MakeObject("node", "hardBreak"), marks)
			offset++
		}
		if run.text != "" {
			text.Insert(offset, run.text, marks)
			offset += len(utf16.Encode([]rune(run.text)))
		}
	}
}

func groupCharacters(chars []inlineCharacter) []documentInline {
	var result []documentInline
	var text strings.Builder
	var marks contentObject
	flush := func() {
		if text.Len() != 0 {
			result = append(result, documentInline{text: text.String(), marks: marks})
			text.Reset()
		}
	}
	for _, char := range chars {
		if char.br {
			flush()
			result = append(result, documentInline{br: true})
			continue
		}
		if !sameMarks(marks, char.marks) {
			flush()
			marks = char.marks
		}
		text.WriteRune(char.value)
	}
	flush()
	return result
}

func documentPlain(runs []documentInline) string {
	var result strings.Builder
	for _, run := range runs {
		if run.br {
			result.WriteRune('\uFFFC')
		}
		result.WriteString(run.text)
	}
	return result.String()
}

// In-place edits retain the Y.Text and the formatted spans at either end.
func writeDocumentText(text *crdt.YText, next []documentInline) {
	old := readDocumentText(text)
	if len(old) == 0 {
		insertDocumentText(text, 0, next)
		return
	}
	if documentPlain(old) == documentPlain(next) {
		writeDocumentMarks(text, old, next)
		return
	}
	a, b := inlineCharacters(old), inlineCharacters(next)
	left := 0
	for left < min(len(a), len(b)) && sameCharacter(a[left], b[left]) {
		left++
	}
	right := 0
	for right < min(len(a), len(b))-left && sameCharacter(a[len(a)-1-right], b[len(b)-1-right]) {
		right++
	}
	at := markedLength(a[:left])
	if removed := markedLength(a[left : len(a)-right]); removed != 0 {
		text.Delete(at, removed)
	}
	insertDocumentText(text, at, groupCharacters(b[left:len(b)-right]))
}

func writeDocumentMarks(text *crdt.YText, old, next []documentInline) {
	a, b := inlineCharacters(old), inlineCharacters(next)
	if len(a) != len(b) {
		return
	}
	offset := 0
	for i := 0; i < len(a); {
		if sameMarks(a[i].marks, b[i].marks) {
			offset += a[i].length()
			i++
			continue
		}
		j, length := i+1, a[i].length()
		for j < len(a) && sameMarks(a[i].marks, a[j].marks) && sameMarks(b[i].marks, b[j].marks) {
			length += a[j].length()
			j++
		}
		change := markObject(b[i].marks)
		for key := range a[i].marks {
			if _, ok := b[i].marks[key]; !ok && !strings.HasPrefix(key, "comment:") {
				change.Set(key, crdt.NullType{})
			}
		}
		text.Format(offset, length, change)
		offset += length
		i = j
	}
}

func readNestedBoard(raw any) contentObject {
	board, isMap := raw.(*crdt.YMap)
	if !isMap {
		if props := toObject(normalizeJSON(raw)); props != nil {
			return props
		}
		return contentObject{"version": 1, "nodes": []any{}, "edges": []any{}}
	}
	content := normalizeJSON(board.ToJSON()).(contentObject)
	for _, key := range []string{"nodes", "edges"} {
		items, _ := board.Get(key).(*crdt.YMap)
		result := []contentObject{}
		for _, id := range orderedKeys(items, "\x00order") {
			fields := items.Get(id).(*crdt.YMap)
			entry := normalizeJSON(fields.ToJSON()).(contentObject)
			delete(entry, "\x00order")
			entry["id"] = id
			result = append(result, entry)
		}
		content[key] = result
	}
	content["version"] = 1
	return content
}

func documentBoardItems(value any) []contentObject {
	if typed, ok := value.([]contentObject); ok {
		return typed
	}
	items, _ := value.([]any)
	result := make([]contentObject, 0, len(items))
	for _, item := range items {
		if item := toObject(item); itemID(item) != "" {
			result = append(result, item)
		}
	}
	return result
}

func writeNestedBoard(props *crdt.YMap, content contentObject) {
	mapOfBoard := ensureMap(props, "board")
	mapOfBoard.Set("version", 1)
	for _, key := range []string{"nodes", "edges"} {
		part := ensureMap(mapOfBoard, key)
		oldIDs := orderedKeys(part, "\x00order")
		items := documentBoardItems(content[key])
		nextIDs := make([]string, 0, len(items))
		for _, item := range items {
			id := itemID(item)
			nextIDs = append(nextIDs, id)
			fields := ensureMap(part, id)
			previous := normalizeJSON(fields.ToJSON()).(contentObject)
			delete(previous, "\x00order")
			for field := range previous {
				if _, exists := item[field]; !exists {
					fields.Delete(field)
				}
			}
			for field, value := range item {
				if field == "id" || field == "\x00order" {
					continue
				}
				if field == "data" {
					data := ensureMap(fields, field)
					changeFields(data, toObject(previous[field]), toObject(value), false)
				} else if !equalJSON(previous[field], value) || !fields.Has(field) {
					fields.Set(field, sharedJSON(value))
				}
			}
		}
		setSequence(part, oldIDs, nextIDs, "\x00order")
	}
}

func setSequence(fields *crdt.YMap, old, next []string, order string) {
	keep := make(map[string]bool, len(next))
	for _, id := range next {
		keep[id] = true
	}
	for _, id := range old {
		if !keep[id] {
			fields.Delete(id)
		}
	}
	sequenceOrders(fields, next, order)
}

func sequenceOrders(fields *crdt.YMap, next []string, order string) {
	keys := make([]string, len(next))
	for i, id := range next {
		value := stringField(ensureMap(fields, id), order)
		if validOrderKey(value) {
			keys[i] = value
		}
	}
	frozen := increasingIndexes(keys, func(s string) bool { return s != "" }, func(a, b string) bool { return a < b })
	for i := 0; i < len(next); {
		if frozen[i] {
			i++
			continue
		}
		end := i + 1
		for end < len(next) && !frozen[end] {
			end++
		}
		left, right := "", ""
		if i > 0 {
			left = keys[i-1]
		}
		if end < len(next) {
			right = keys[end]
		}
		fillOrderKeys(keys[i:end], left, right)
		for at := i; at < end; at++ {
			ensureMap(fields, next[at]).Set(order, keys[at])
		}
		i = end
	}
}

func UpdateDocument(doc *crdt.Doc, markdown string, origin any) error {
	next, err := parseMarkdown(markdown)
	if err != nil {
		return err
	}
	root := doc.GetMap("document")
	before, err := readDocumentBlocks(root)
	if err != nil {
		return err
	}
	doc.Transact(func(*crdt.Transaction) {
		updateDocumentChildren(root, "", before, next)
	}, origin)
	return nil
}

func blockKey(block documentBlock) string {
	block.children = nil
	if block.kind == "ask" {
		if id, _ := block.props["id"].(string); id != "" {
			return "ask\x00" + id
		}
	}
	if block.kind == "ref" {
		artifactID, _ := block.props["artifactId"].(string)
		if artifactID != "" {
			groupID, _ := block.props["groupId"].(string)
			optionID, _ := block.props["optionId"].(string)
			return "ref\x00" + artifactID + "\x00" + groupID + "\x00" + optionID
		}
	}
	return block.kind + "\x00" + renderDocumentBlock(block, 1)
}

func blockMatches(before, next []documentBlock) []int {
	byKey := make(map[string][]int, len(before))
	for i, block := range before {
		key := blockKey(block)
		byKey[key] = append(byKey[key], i)
	}
	matches := make([]int, len(next))
	used := make([]bool, len(before))
	for i, block := range next {
		key := blockKey(block)
		options := byKey[key]
		matches[i] = -1
		if len(options) > 0 {
			matches[i] = options[0]
			used[options[0]] = true
			byKey[key] = options[1:]
		}
	}
	byType := make(map[string][]int)
	for i := range before {
		if !used[i] {
			byType[before[i].kind] = append(byType[before[i].kind], i)
		}
	}
	for i, block := range next {
		if options := byType[block.kind]; matches[i] < 0 && len(options) > 0 {
			matches[i] = options[0]
			byType[block.kind] = options[1:]
		}
	}
	return matches
}

func updateDocumentChildren(root *crdt.YMap, parent string, before, next []documentBlock) {
	matches := blockMatches(before, next)
	oldIDs := make([]string, len(before))
	nextIDs := make([]string, len(next))
	remain := map[string]bool{}
	for i, block := range before {
		oldIDs[i] = block.id
	}
	for i, block := range next {
		var old documentBlock
		if matches[i] >= 0 {
			old = before[matches[i]]
		}
		id := old.id
		if id == "" {
			id = uuid.NewString()
		}
		nextIDs[i], remain[id] = id, true
		node := ensureMap(root, id)
		var parentValue any = crdt.NullType{}
		if parent != "" {
			parentValue = parent
		}
		if !node.Has("parent") || !equalJSON(node.Get("parent"), parentValue) {
			node.Set("parent", parentValue)
		}
		writeDocumentBlock(node, old, block)
		updateDocumentChildren(root, id, old.children, block.children)
	}
	for _, block := range before {
		if !remain[block.id] {
			removeDocumentBlock(root, block)
		}
	}
	setSequence(root, oldIDs, nextIDs, "order")
}

func removeDocumentBlock(root *crdt.YMap, block documentBlock) {
	for _, child := range block.children {
		removeDocumentBlock(root, child)
	}
	root.Delete(block.id)
}

func writeDocumentBlock(node *crdt.YMap, old, next documentBlock) {
	if old.kind != next.kind {
		node.Set("type", next.kind)
	}
	text, _ := node.Get("text").(*crdt.YText)
	if text == nil {
		text = crdt.NewYText("")
		node.Set("text", text)
	}
	props := ensureMap(node, "props")
	for key := range old.props {
		if _, exists := next.props[key]; !exists && next.kind != "table" {
			props.Delete(key)
		}
	}
	for key, value := range next.props {
		if key == "board" && next.kind == "whiteboard" {
			if !equalJSON(old.props[key], value) {
				writeNestedBoard(props, toObject(value))
			}
		} else if !props.Has(key) || !equalJSON(old.props[key], value) {
			props.Set(key, sharedJSON(value))
		}
	}
	if next.table != nil {
		if old.table == nil || writeMarkdownTable(old.table) != writeMarkdownTable(next.table) ||
			old.table.headerRow != next.table.headerRow || old.table.headerColumn != next.table.headerColumn {
			props.Set("headerRow", next.table.headerRow)
			props.Set("headerColumn", next.table.headerColumn)
			writeDocumentTable(node, next.table, old.table)
		}
		return
	}
	writeDocumentText(text, next.inline)
}

func ValidateDocument(markdown string) error {
	if _, err := parseMarkdown(markdown); err != nil {
		return fmt.Errorf("document: %w", err)
	}
	return nil
}
