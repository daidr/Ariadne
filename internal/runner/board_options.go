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

type boardOption struct {
	id, title, note, text, revises string
	hasNote                        bool
	pictures                       []boardPicture
}

func (turn *turn) boardOptions(ctx context.Context, raw any) ([]boardOption, error) {
	values, err := artifactObjects(raw)
	if err != nil {
		return nil, err
	}
	result := make([]boardOption, 0, len(values))
	seen := map[string]bool{}
	for _, candidate := range values {
		option := boardOption{id: text(candidate, "id"), title: text(candidate, "title"), text: text(candidate, "text"), revises: text(candidate, "revises")}
		option.note, option.hasNote = candidate["note"].(string)
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

func (board *whiteboard) placeOptions(options []boardOption, pick string) error {
	const width, padding, inset, gap, between = 360.0, 16.0, 40.0, 12.0, 48.0
	for _, option := range options {
		nodes := boardItems(board.Nodes)
		existing, before := nodes[option.id], nodes[option.revises]
		if existing != nil && text(existing, "type") != "option" {
			return fmt.Errorf("%q is already a different type of item", option.id)
		}
		if option.revises != "" && (text(before, "type") != "option" || option.revises == option.id) {
			return fmt.Errorf("cannot revise option %q", option.revises)
		}
		x, y := 0.0, 0.0
		switch {
		case existing != nil:
			x, y = asNumber(existing["x"]), asNumber(existing["y"])
		case before != nil:
			x, y = asNumber(before["x"]), asNumber(before["y"])+asNumber(before["height"])+between
		case len(board.Nodes) > 0:
			for _, node := range board.Nodes {
				x = max(x, asNumber(node["x"])+asNumber(node["width"])+between)
			}
			for _, node := range board.Nodes {
				if text(node, "type") == "option" {
					y = asNumber(node["y"])
					break
				}
			}
		}
		mine := map[string]bool{option.id + "-text": true}
		for i := range 50 {
			mine[fmt.Sprintf("%s-image-%d", option.id, i+1)] = true
		}
		for id := range mine {
			if node := nodes[id]; node != nil && text(node, "parent") != option.id {
				return fmt.Errorf("the option's generated item %q already exists", id)
			}
		}
		var inner []args
		cursor := y + inset
		for i, picture := range option.pictures {
			const pictureWidth = width - 2*padding
			height := math.Round(float64(picture.height) / float64(picture.width) * pictureWidth)
			fields := args{"src": picture.src}
			if picture.alt != "" {
				fields["alt"] = picture.alt
			}
			inner = append(inner, args{"id": fmt.Sprintf("%s-image-%d", option.id, i+1), "type": "image", "parent": option.id,
				"x": x + padding, "y": cursor, "width": pictureWidth, "height": height, "data": fields})
			cursor += height + gap
		}
		if option.text != "" {
			height := boardLineCount(option.text, 16, width-2*padding-16)*22 + 8
			inner = append(inner, args{"id": option.id + "-text", "type": "text", "parent": option.id, "x": x + padding, "y": cursor,
				"width": width - 2*padding, "height": height, "data": args{"text": option.text}})
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
		if pick == "many" {
			fields["pick"] = "many"
		} else if pick == "one" {
			delete(fields, "pick")
		}
		if existing == nil {
			existing = args{"id": option.id, "type": "option"}
			board.Nodes = append(board.Nodes, existing)
		}
		existing["x"], existing["y"], existing["width"], existing["height"], existing["data"] = x, y, width, height, fields
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
	}
	return nil
}
