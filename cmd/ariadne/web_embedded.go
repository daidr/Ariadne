//go:build ariadne_web

package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io/fs"
)

const bundledBuild = true

//go:embed dist/web.zip
var web []byte

func embeddedWeb() (fs.FS, error) {
	reader, err := zip.NewReader(bytes.NewReader(web), int64(len(web)))
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(reader, "index.html"); err != nil {
		return nil, err
	}
	return reader, nil
}
