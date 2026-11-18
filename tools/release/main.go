// release builds production binaries with the bundled web interface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

type target struct {
	name, os, arch string
}

var targets = []target{
	{"linux-x64", "linux", "amd64"},
	{"linux-arm64", "linux", "arm64"},
	{"darwin-x64", "darwin", "amd64"},
	{"darwin-arm64", "darwin", "arm64"},
	{"windows-x64", "windows", "amd64"},
}

var plainVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	selection := flags.String("target", "", "comma-separated targets, all, or the current platform by default")
	out := flags.String("out", "dist/release", "output directory")
	flags.Bool("no-upx", true, "accepted for compatibility, binaries are always uncompressed")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected argument", flags.Arg(0))
		os.Exit(2)
	}
	if err := release(ctx, *selection, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func selectTargets(selection string) ([]target, error) {
	if selection == "all" {
		return targets, nil
	}
	if selection == "" {
		for _, item := range targets {
			if item.native() {
				return []target{item}, nil
			}
		}
		return nil, errors.New("no build target for this platform, specify --target")
	}
	var selected []target
	for name := range strings.SplitSeq(selection, ",") {
		name = strings.TrimSpace(name)
		index := slices.IndexFunc(targets, func(item target) bool { return item.name == name })
		if index == -1 {
			return nil, fmt.Errorf("unknown build target %q", name)
		}
		if !slices.Contains(selected, targets[index]) {
			selected = append(selected, targets[index])
		}
	}
	return selected, nil
}

func (item target) native() bool { return runtime.GOOS == item.os && runtime.GOARCH == item.arch }

func (item target) executable() string {
	name := "ariadne-" + item.name
	if item.os == "windows" {
		name += ".exe"
	}
	return name
}

func release(ctx context.Context, selection, out string) error {
	selected, err := selectTargets(selection)
	if err != nil {
		return err
	}
	manifest, err := os.ReadFile("package.json")
	if err != nil {
		return err
	}
	var pkg struct{ Version string }
	if err := json.Unmarshal(manifest, &pkg); err != nil {
		return err
	}
	if !plainVersion.MatchString(pkg.Version) {
		return fmt.Errorf("invalid release version %q", pkg.Version)
	}
	if err := run(ctx, nil, "bun", "run", "build"); err != nil {
		return err
	}
	if err := run(ctx, nil, "go", "run", "./tools/webbundle", "packages/web/dist", "cmd/ariadne/dist/web.zip"); err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, item := range selected {
		if err := build(ctx, out, pkg.Version, item); err != nil {
			return err
		}
	}
	return nil
}

func build(ctx context.Context, out, version string, item target) error {
	fmt.Printf("Building %s\n", item.name)
	temporary, err := os.CreateTemp(out, ".ariadne-*"+filepath.Ext(item.executable()))
	if err != nil {
		return err
	}
	staged := temporary.Name()
	defer os.Remove(staged)
	if err := temporary.Close(); err != nil {
		return err
	}
	settings := []string{"CGO_ENABLED=0", "GOOS=" + item.os, "GOARCH=" + item.arch}
	options := "-s -w -X main.version=" + version
	if err := run(ctx, settings, "go", "build", "-tags", "ariadne_web", "-trimpath", "-ldflags", options, "-o", staged, "./cmd/ariadne"); err != nil {
		return err
	}
	if item.native() {
		if err := run(ctx, nil, staged, "self-check"); err != nil {
			return err
		}
	}
	path := filepath.Join(out, item.executable())
	if err := os.Rename(staged, path); err != nil {
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Printf("%s (%.1f MiB)\n", path, float64(stat.Size())/(1<<20))
	return nil
}

func run(ctx context.Context, env []string, program string, args ...string) error {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = append(os.Environ(), env...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(program), err)
	}
	return nil
}
