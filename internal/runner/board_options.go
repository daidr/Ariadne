package runner

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"mime"
	"path/filepath"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/files"
)

type boardPicture struct {
	src, alt      string
	width, height int
}

type boardGroup struct {
	id, title, note, pick string
	hasNote               bool
	round                 int
	hasRound              bool
	confirmed             bool
	hasConfirmed          bool
}

type boardOption struct {
	id, title, note, text, revises, group string
	hasNote                               bool
	recommended                           bool
	hasRecommended                        bool
	pictures                              []boardPicture
	baselinePictures                      []boardPicture
}

func boardGroups(raw any) ([]boardGroup, error) {
	values, err := artifactObjects(raw)
	if err != nil {
		return nil, err
	}
	result := make([]boardGroup, 0, len(values))
	seen := map[string]bool{}
	for _, candidate := range values {
		group := boardGroup{
			id:    text(candidate, "id"),
			title: text(candidate, "title"),
			pick:  text(candidate, "pick"),
		}
		group.note, group.hasNote = candidate["note"].(string)
		if group.id == "" || group.title == "" || seen[group.id] {
			return nil, errors.New("each group needs a unique id and a title")
		}
		if group.pick != "" && group.pick != "one" && group.pick != "many" {
			return nil, fmt.Errorf("group %q pick must be one or many", group.id)
		}
		if rawRound, exists := candidate["round"]; exists && rawRound != nil {
			round, err := artifactInteger(rawRound, "round")
			if err != nil || round < 1 {
				return nil, fmt.Errorf("group %q round must be a positive integer", group.id)
			}
			group.round = int(round)
			group.hasRound = true
		}
		if confirmed, ok := candidate["confirmed"].(bool); ok {
			group.confirmed = confirmed
			group.hasConfirmed = true
		}
		seen[group.id] = true
		result = append(result, group)
	}
	return result, nil
}

func (turn *turn) boardOptions(ctx context.Context, raw any) ([]boardOption, error) {
	values, err := artifactObjects(raw)
	if err != nil {
		return nil, err
	}
	result := make([]boardOption, 0, len(values))
	seen := map[string]bool{}
	for _, candidate := range values {
		option := boardOption{
			id:      text(candidate, "id"),
			title:   text(candidate, "title"),
			text:    text(candidate, "text"),
			revises: text(candidate, "revises"),
			group:   text(candidate, "group"),
		}
		option.note, option.hasNote = candidate["note"].(string)
		if rec, ok := candidate["recommended"].(bool); ok {
			option.recommended = rec
			option.hasRecommended = true
		}
		if option.id == "" || option.title == "" || seen[option.id] {
			return nil, errors.New("each option needs a unique id and a title")
		}
		seen[option.id] = true
		images, err := artifactObjects(candidate["images"])
		if err != nil || len(images) > 50 {
			return nil, errors.New("an option can have up to 50 image objects")
		}
		for _, input := range images {
			picture, err := turn.boardPicture(ctx, input)
			if err != nil {
				return nil, err
			}
			option.pictures = append(option.pictures, picture)
		}
		baselineImages, err := artifactObjects(candidate["baselineImages"])
		if err != nil || len(baselineImages) > 50 {
			return nil, errors.New("an option can have up to 50 baseline image objects")
		}
		for _, input := range baselineImages {
			picture, err := turn.boardPicture(ctx, input)
			if err != nil {
				return nil, err
			}
			option.baselinePictures = append(option.baselinePictures, picture)
		}
		result = append(result, option)
	}
	return result, nil
}

func (turn *turn) boardPicture(ctx context.Context, input args) (boardPicture, error) {
	store := files.Store{Events: turn.Repository.Store}
	item := boardPicture{alt: text(input, "alt")}
	project := turn.conversation.ProjectID
	var data []byte
	var media string
	if id := strings.TrimPrefix(text(input, "file"), "file:"); id != "" {
		info, err := store.Info(ctx, project, id)
		if err != nil {
			return item, err
		}
		if info == nil || !files.IsImage(info.MIME) || info.Size > files.MaxImageBytes {
			return item, errors.New("the file must be an image of this project")
		}
		content, err := store.Read(ctx, id)
		if err != nil {
			return item, err
		}
		data, media = content.Data, content.MIME
		item.src = "/api/files/" + id
	} else {
		path := text(input, "path")
		if path == "" || turn.conversation.Role != "thread" || turn.Workspaces == nil {
			return item, errors.New("give a project file ID or a path in your thread workspace")
		}
		media, _, _ = mime.ParseMediaType(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
		if !slices.Contains([]string{"image/png", "image/gif", "image/jpeg", "image/webp"}, media) {
			return item, errors.New("the workspace image must be PNG, GIF, JPEG or WebP")
		}
		view, err := turn.source(ctx, args{})
		if err != nil {
			return item, err
		}
		data, err = view.ReadBytes(ctx, path, files.MaxImageBytes)
		if err != nil {
			return item, err
		}
	}
	w, h := pictureDimensions(data, media)
	if w <= 0 || h <= 0 || w > 100_000 || h > 100_000 {
		return item, errors.New("could not read the image dimensions")
	}
	if item.src == "" {
		stored, err := store.Save(ctx, project, media, data, false)
		if err != nil {
			return item, err
		}
		item.src = stored.URL
	}
	item.width, item.height = w, h
	return item, nil
}

func pictureDimensions(data []byte, media string) (int, int) {
	if media != "image/webp" {
		cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
		if err == nil && media == "image/"+kind {
			return cfg.Width, cfg.Height
		}
		return 0, 0
	}
	if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(data[12:16]) {
	case "VP8X":
		unit := func(i int) int { return int(data[i]) | int(data[i+1])<<8 | int(data[i+2])<<16 }
		return 1 + unit(24), 1 + unit(27)
	case "VP8 ":
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
	case "VP8L":
		bits := binary.LittleEndian.Uint32(data[21:25])
		return 1 + int(bits&0x3fff), 1 + int((bits>>14)&0x3fff)
	}
	return 0, 0
}

func (board *whiteboard) nextGroupY() float64 {
	const groupGap = 48.0
	hasTop := false
	bottom := 0.0
	for _, node := range board.Nodes {
		if text(node, "parent") != "" {
			continue
		}
		hasTop = true
		box := bounds(node)
		bottom = max(bottom, box.y+box.h)
	}
	if !hasTop {
		return 0
	}
	return bottom + groupGap
}

func groupHeaderInset(group args) float64 {
	if text(objectField(group, "data"), "note") != "" {
		return 68.0
	}
	return 48.0
}

func (board *whiteboard) resizeGroupToChildren(group args) {
	const padding = 20.0
	id := text(group, "id")
	gx, gy := asNumber(group["x"]), asNumber(group["y"])
	header := groupHeaderInset(group)
	minWidth := max(400.0, asNumber(group["width"]))
	minHeight := max(header+120.0+padding, 180.0)
	maxRight := gx + minWidth - padding
	maxBottom := gy + minHeight - padding
	for _, node := range board.Nodes {
		if text(node, "parent") != id {
			continue
		}
		box := bounds(node)
		maxRight = max(maxRight, box.x+box.w)
		maxBottom = max(maxBottom, box.y+box.h)
	}
	group["width"] = max(minWidth, maxRight-gx+padding)
	group["height"] = max(minHeight, maxBottom-gy+padding)
}

func (board *whiteboard) placeGroupsAndOptions(groups []boardGroup, options []boardOption, pick string) error {
	maxExistingRound := 0
	for _, node := range board.Nodes {
		if text(node, "type") == "group" {
			if r := int(asNumber(objectField(node, "data")["round"])); r > maxExistingRound {
				maxExistingRound = r
			}
		}
	}
	maxNewRound := 0
	explicitConfirmed := map[string]bool{}
	incomingGroupSet := map[string]bool{}
	for _, g := range groups {
		incomingGroupSet[g.id] = true
		if g.hasConfirmed {
			explicitConfirmed[g.id] = true
		}
		if g.hasRound && g.round > maxNewRound {
			maxNewRound = g.round
		}
	}
	if maxNewRound > maxExistingRound && maxExistingRound > 0 {
		for _, node := range board.Nodes {
			if text(node, "type") != "group" || explicitConfirmed[text(node, "id")] {
				continue
			}
			data := objectField(node, "data")
			if r := int(asNumber(data["round"])); r > 0 && r < maxNewRound {
				data["confirmed"] = true
				node["data"] = data
			}
		}
	}

	groupedOptions := map[string][]boardOption{}
	var existingGroupOptions []boardOption
	var ungroupedOptions []boardOption
	for _, opt := range options {
		switch {
		case opt.group == "":
			ungroupedOptions = append(ungroupedOptions, opt)
		case incomingGroupSet[opt.group]:
			groupedOptions[opt.group] = append(groupedOptions[opt.group], opt)
		default:
			existingGroupOptions = append(existingGroupOptions, opt)
		}
	}

	for _, g := range groups {
		nodes := boardItems(board.Nodes)
		existing := nodes[g.id]
		if existing != nil && text(existing, "type") != "group" {
			return fmt.Errorf("%q is already a different type of item", g.id)
		}
		data := objectField(existing, "data")
		data["label"] = g.title
		if g.hasNote {
			if g.note == "" {
				delete(data, "note")
			} else {
				data["note"] = g.note
			}
		}
		if g.pick != "" {
			data["pick"] = g.pick
		}
		if g.hasRound {
			data["round"] = g.round
		}
		if g.hasConfirmed {
			if g.confirmed {
				data["confirmed"] = true
			} else {
				delete(data, "confirmed")
			}
		}
		if existing == nil {
			existing = args{
				"id":     g.id,
				"type":   "group",
				"x":      0.0,
				"y":      board.nextGroupY(),
				"width":  400.0,
				"height": 180.0,
				"data":   data,
			}
			board.Nodes = append(board.Nodes, existing)
		} else {
			existing["data"] = data
		}
		for _, opt := range groupedOptions[g.id] {
			if err := board.placeSingleOption(opt, pick, existing); err != nil {
				return err
			}
		}
		board.resizeGroupToChildren(existing)
	}

	touchedExisting := map[string]args{}
	for _, opt := range existingGroupOptions {
		nodes := boardItems(board.Nodes)
		groupNode := nodes[opt.group]
		if groupNode == nil || text(groupNode, "type") != "group" {
			return fmt.Errorf("option %q references unknown group %q", opt.id, opt.group)
		}
		if err := board.placeSingleOption(opt, pick, groupNode); err != nil {
			return err
		}
		touchedExisting[opt.group] = groupNode
	}
	for _, groupNode := range touchedExisting {
		board.resizeGroupToChildren(groupNode)
	}

	for _, opt := range ungroupedOptions {
		if err := board.placeSingleOption(opt, pick, nil); err != nil {
			return err
		}
	}
	return nil
}

func (board *whiteboard) placeSingleOption(option boardOption, pick string, groupNode args) error {
	const width, padding, inset, gap, between = 360.0, 16.0, 40.0, 12.0, 48.0
	const groupPad, optionGap = 20.0, 24.0
	nodes := boardItems(board.Nodes)
	existing, before := nodes[option.id], nodes[option.revises]
	if existing != nil && text(existing, "type") != "option" {
		return fmt.Errorf("%q is already a different type of item", option.id)
	}
	if option.revises != "" && (text(before, "type") != "option" || option.revises == option.id) {
		return fmt.Errorf("cannot revise option %q", option.revises)
	}
	groupID := ""
	if groupNode != nil {
		groupID = text(groupNode, "id")
	}
	x, y := 0.0, 0.0
	switch {
	case existing != nil && (groupID == "" || text(existing, "parent") == groupID):
		x, y = asNumber(existing["x"]), asNumber(existing["y"])
	case before != nil:
		step := between
		if groupID != "" {
			step = optionGap
		}
		x, y = asNumber(before["x"]), asNumber(before["y"])+asNumber(before["height"])+step
	case groupNode != nil:
		gx, gy := asNumber(groupNode["x"]), asNumber(groupNode["y"])
		header := groupHeaderInset(groupNode)
		x, y = gx+groupPad, gy+header
		foundSibling := false
		for _, node := range board.Nodes {
			if text(node, "parent") == groupID && text(node, "type") == "option" && text(node, "id") != option.id {
				foundSibling = true
				x = max(x, asNumber(node["x"])+asNumber(node["width"])+optionGap)
			}
		}
		if !foundSibling {
			x = gx + groupPad
		}
	case len(board.Nodes) > 0:
		for _, node := range board.Nodes {
			if text(node, "parent") != "" {
				continue
			}
			x = max(x, asNumber(node["x"])+asNumber(node["width"])+between)
		}
		for _, node := range board.Nodes {
			if text(node, "type") == "option" && text(node, "parent") == "" {
				y = asNumber(node["y"])
				break
			}
		}
	}
	mine := map[string]bool{
		option.id + "-text":           true,
		option.id + "-baseline-label": true,
		option.id + "-actual-label":   true,
	}
	for i := range 50 {
		mine[fmt.Sprintf("%s-image-%d", option.id, i+1)] = true
		mine[fmt.Sprintf("%s-baseline-%d", option.id, i+1)] = true
	}
	for id := range mine {
		if node := nodes[id]; node != nil && text(node, "parent") != option.id {
			return fmt.Errorf("the option's generated item %q already exists", id)
		}
	}
	frameWidth := width
	var inner []args
	cursor := y + inset
	if len(option.baselinePictures) > 0 && len(option.pictures) > 0 {
		frameWidth = 720.0
		colWidth := (frameWidth - 2*padding - gap) / 2
		const labelHeight = 26.0
		leftX := x + padding
		rightX := x + padding + colWidth + gap
		inner = append(inner,
			args{"id": option.id + "-baseline-label", "type": "text", "parent": option.id, "x": leftX, "y": cursor, "width": colWidth, "height": labelHeight, "data": args{"text": "参考 / 改动前"}},
			args{"id": option.id + "-actual-label", "type": "text", "parent": option.id, "x": rightX, "y": cursor, "width": colWidth, "height": labelHeight, "data": args{"text": "实机结果"}},
		)
		leftCursor := cursor + labelHeight + 6
		for i, picture := range option.baselinePictures {
			h := math.Round(float64(picture.height) / float64(picture.width) * colWidth)
			fields := args{"src": picture.src}
			if picture.alt != "" {
				fields["alt"] = picture.alt
			}
			inner = append(inner, args{"id": fmt.Sprintf("%s-baseline-%d", option.id, i+1), "type": "image", "parent": option.id,
				"x": leftX, "y": leftCursor, "width": colWidth, "height": h, "data": fields})
			leftCursor += h + gap
		}
		rightCursor := cursor + labelHeight + 6
		for i, picture := range option.pictures {
			h := math.Round(float64(picture.height) / float64(picture.width) * colWidth)
			fields := args{"src": picture.src}
			if picture.alt != "" {
				fields["alt"] = picture.alt
			}
			inner = append(inner, args{"id": fmt.Sprintf("%s-image-%d", option.id, i+1), "type": "image", "parent": option.id,
				"x": rightX, "y": rightCursor, "width": colWidth, "height": h, "data": fields})
			rightCursor += h + gap
		}
		cursor = max(leftCursor, rightCursor)
	} else {
		pictureWidth := frameWidth - 2*padding
		for i, picture := range option.baselinePictures {
			h := math.Round(float64(picture.height) / float64(picture.width) * pictureWidth)
			fields := args{"src": picture.src}
			if picture.alt != "" {
				fields["alt"] = picture.alt
			}
			inner = append(inner, args{"id": fmt.Sprintf("%s-baseline-%d", option.id, i+1), "type": "image", "parent": option.id,
				"x": x + padding, "y": cursor, "width": pictureWidth, "height": h, "data": fields})
			cursor += h + gap
		}
		for i, picture := range option.pictures {
			h := math.Round(float64(picture.height) / float64(picture.width) * pictureWidth)
			fields := args{"src": picture.src}
			if picture.alt != "" {
				fields["alt"] = picture.alt
			}
			inner = append(inner, args{"id": fmt.Sprintf("%s-image-%d", option.id, i+1), "type": "image", "parent": option.id,
				"x": x + padding, "y": cursor, "width": pictureWidth, "height": h, "data": fields})
			cursor += h + gap
		}
	}
	if option.text != "" {
		height := boardLineCount(option.text, 16, frameWidth-2*padding-16)*22 + 8
		inner = append(inner, args{"id": option.id + "-text", "type": "text", "parent": option.id, "x": x + padding, "y": cursor,
			"width": frameWidth - 2*padding, "height": height, "data": args{"text": option.text}})
		cursor += height + gap
	}
	if len(inner) > 0 {
		cursor -= gap
	}
	height := max(cursor-y+padding, 120.0, asNumber(existing["height"]))
	fields := objectField(existing, "data")
	fields["title"] = option.title
	if option.hasNote {
		fields["note"] = option.note
	}
	if groupID != "" {
		fields["group"] = groupID
	}
	effectivePick := pick
	if effectivePick == "" && groupNode != nil {
		effectivePick = text(objectField(groupNode, "data"), "pick")
	}
	if effectivePick == "many" {
		fields["pick"] = "many"
	} else if effectivePick == "one" {
		delete(fields, "pick")
	}
	if option.hasRecommended {
		if option.recommended {
			fields["recommended"] = true
		} else {
			delete(fields, "recommended")
		}
	}
	if existing == nil {
		existing = args{"id": option.id, "type": "option"}
		board.Nodes = append(board.Nodes, existing)
	}
	if groupID != "" {
		existing["parent"] = groupID
	}
	existing["x"], existing["y"], existing["width"], existing["height"], existing["data"] = x, y, frameWidth, height, fields
	board.Nodes = slices.DeleteFunc(board.Nodes, func(node args) bool { return mine[text(node, "id")] })
	board.Nodes = append(board.Nodes, inner...)
	active := boardItems(board.Nodes)
	board.Edges = slices.DeleteFunc(board.Edges, func(edge args) bool {
		return active[boardEndpoint(edge, "source")] == nil || active[boardEndpoint(edge, "target")] == nil
	})
	if before != nil && !slices.ContainsFunc(board.Edges, func(edge args) bool {
		return boardEndpoint(edge, "source") == option.revises && boardEndpoint(edge, "target") == option.id
	}) {
		id := availableBoardID(boardItems(board.Edges), option.revises+"-"+option.id)
		board.Edges = append(board.Edges, args{"id": id, "type": "connector",
			"source": args{"node": option.revises, "port": "bottom"}, "target": args{"node": option.id, "port": "top-in"},
			"data": args{"route": "straight", "dash": "dashed"}})
	}
	return nil
}
