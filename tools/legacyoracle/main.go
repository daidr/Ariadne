// legacyoracle prepares the original Bun backend solely for comparison tests.
package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

const baseline = "7bb1af9820dbfc03e78a711a7eca58f8e8035673"

func main() {
	output := flag.String("out", "dist/legacy-oracle", "directory for the reference checkout")
	flag.Parse()
	if err := prepare(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(output string) error {
	output, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(output)
	if err != nil {
		return err
	}
	defer root.Close()
	want := []byte(baseline + "\n")
	if got, err := root.ReadFile(".ariadne-legacy"); err == nil {
		if string(got) != string(want) {
			return errors.New("reference directory was prepared for another commit")
		}
	} else {
		info, err := os.ReadDir(output)
		if err != nil {
			return err
		}
		if len(info) > 0 {
			return errors.New("reference output directory must be empty")
		}
		if err := extractBaseline(root); err != nil {
			return err
		}
		if err := root.WriteFile(".ariadne-legacy", want, 0o600); err != nil {
			return err
		}
	}
	fixtures := os.DirFS("tools/legacyoracle/fixtures")
	if err := fs.WalkDir(fixtures, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contents, err := fs.ReadFile(fixtures, name)
		if err != nil {
			return err
		}
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		return root.WriteFile(name, contents, 0o644)
	}); err != nil {
		return err
	}
	schema, err := os.ReadFile("internal/schema/schema.sql")
	if err != nil {
		return err
	}
	if err := root.MkdirAll("internal/schema", 0o755); err != nil {
		return err
	}
	if err := root.WriteFile("internal/schema/schema.sql", schema, 0o644); err != nil {
		return err
	}
	bun := exec.Command("bun", "install", "--frozen-lockfile")
	bun.Dir, bun.Stdout, bun.Stderr = output, os.Stdout, os.Stderr
	if err := bun.Run(); err != nil {
		return err
	}
	fmt.Println("Legacy oracle available at " + output)
	return nil
}

func extractBaseline(root *os.Root) error {
	file, err := os.CreateTemp("", "ariadne-legacy-*.zip")
	if err != nil {
		return err
	}
	archivePath := file.Name()
	defer os.Remove(archivePath)
	if err := file.Close(); err != nil {
		return err
	}
	git := exec.Command("git", "archive", "--format=zip", "--output="+archivePath, baseline)
	if data, err := git.CombinedOutput(); err != nil {
		return fmt.Errorf("could not archive %s: %w\n%s", baseline, err, data)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	for _, entry := range archive.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") {
			return errors.New("invalid path in the reference archive")
		}
		if entry.Mode().IsDir() {
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("unexpected file type for %s", name)
		}
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		if err := extractFile(root, entry, name); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(root *os.Root, entry *zip.File, name string) error {
	in, err := entry.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entry.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}
