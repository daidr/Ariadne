package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestPackUsesStoreForPrecompressedAssets(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"index.html":       []byte("<!doctype html>"),
		"index.html.br":    []byte("precompressed-br"),
		"assets/app.js":    []byte("console.log('hi')"),
		"assets/app.js.br": []byte("precompressed-js-br"),
		"assets/logo.png":  []byte("pngbytes"),
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(src, rel), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	target := filepath.Join(t.TempDir(), "web.zip")
	if err := pack(src, target); err != nil {
		t.Fatalf("pack failed: %v", err)
	}

	zr, err := zip.OpenReader(target)
	if err != nil {
		t.Fatalf("open zip failed: %v", err)
	}
	defer zr.Close()

	gotMethod := map[string]uint16{}
	for _, f := range zr.File {
		gotMethod[f.Name] = f.Method
	}
	for name, want := range map[string]uint16{
		"index.html":       zip.Deflate,
		"index.html.br":    zip.Store,
		"assets/app.js":    zip.Deflate,
		"assets/app.js.br": zip.Store,
		"assets/logo.png":  zip.Store,
	} {
		if gotMethod[name] != want {
			t.Fatalf("%s zip method = %d, want %d", name, gotMethod[name], want)
		}
	}
}
