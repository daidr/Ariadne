// webbundle packs the generated frontend into a deterministic archive for go:embed.
package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func zipMethodForPath(name string) uint16 {
	switch strings.ToLower(path.Ext(name)) {
	case ".br", ".gz", ".png", ".jpg", ".jpeg", ".webp", ".avif", ".woff2", ".zip":
		return zip.Store
	default:
		return zip.Deflate
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: webbundle <frontend directory> <archive path>")
		os.Exit(1)
	}
	if err := pack(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(directory, target string) error {
	source := os.DirFS(directory)
	index, err := fs.Stat(source, "index.html")
	if err != nil || !index.Mode().IsRegular() {
		return fmt.Errorf("%s needs a built index.html", directory)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".web-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	archive := zip.NewWriter(output)
	walkErr := fs.WalkDir(source, ".", func(filePath string, info fs.DirEntry, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !info.Type().IsRegular() {
			return fmt.Errorf("unexpected frontend file type: %s", filePath)
		}
		in, err := source.Open(filePath)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := archive.CreateHeader(&zip.FileHeader{
			Name:   filePath,
			Method: zipMethodForPath(filePath),
		})
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		return err
	})
	if err := errors.Join(walkErr, archive.Close(), output.Close()); err != nil {
		return err
	}
	return os.Rename(output.Name(), target)
}
