// Package collab implements the Yjs document and sync wire boundary. Rooms and
// artifact-specific edit semantics are owned by the application above this layer.
package collab

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/antst/go-yjs/crdt"
	"github.com/antst/go-yjs/protocol"
)

func roots(kind string) ([]string, error) {
	switch kind {
	case "document":
		return []string{"document"}, nil
	case "whiteboard":
		return []string{"nodes", "edges"}, nil
	case "dashboard":
		return []string{"widgets", "snapshot"}, nil
	}
	return nil, fmt.Errorf("unknown shared artifact kind: %s", kind)
}

// Load owns a newly allocated document; the caller must Destroy it. All access
// to one live document must be serialized by its room, including snapshots.
func Load(kind string, state []byte) (doc *crdt.Doc, err error) {
	names, err := roots(kind)
	if err != nil {
		return nil, err
	}
	doc = crdt.NewDoc("")
	defer func() {
		if cause := recover(); cause != nil {
			err = fmt.Errorf("invalid Yjs state: %v", cause)
		}
		if err != nil {
			doc.Destroy()
			doc = nil
		}
	}()
	for _, name := range names {
		doc.GetMap(name)
	}
	if len(state) > 0 {
		err = crdt.ApplyUpdate(doc, state, "load")
	}
	return doc, err
}

func Snapshot(doc *crdt.Doc, kind string) ([]byte, error) {
	names, err := roots(kind)
	if err != nil {
		return nil, err
	}
	value := make(map[string]any, len(names))
	for _, name := range names {
		value[name] = doc.GetMap(name).ToJSON()
	}
	return json.Marshal(value)
}

func HandleSync(doc *crdt.Doc, frame []byte, origin any) ([]byte, error) {
	var reply bytes.Buffer
	_, err := protocol.NewSyncHandler(doc).HandleMessageWithOrigin(frame, &reply, origin)
	if err != nil {
		return nil, err
	}
	return reply.Bytes(), nil
}
