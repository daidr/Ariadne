package local

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

type probeResult struct {
	done  chan struct{}
	value bool
}

func (workspace *Workspace) probe(ctx context.Context, key string, argv []string) (bool, error) {
	workspace.probeMu.Lock()
	if err := workspace.life.Err(); err != nil {
		workspace.probeMu.Unlock()
		return false, err
	}
	if workspace.probes == nil {
		workspace.probes = map[string]*probeResult{}
	}
	probe := workspace.probes[key]
	if probe == nil {
		probe = &probeResult{done: make(chan struct{})}
		workspace.probes[key] = probe
		workspace.probeJobs.Add(1)
		go func() {
			defer workspace.probeJobs.Done()
			defer close(probe.done)
			result, err := workspace.ExecFile(workspace.life, argv, sdk.ExecOptions{Timeout: 10 * time.Second})
			probe.value = err == nil && result.ExitCode == 0
		}()
	}
	workspace.probeMu.Unlock()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-probe.done:
		return probe.value, nil
	}
}
func (workspace *Workspace) Files(ctx context.Context, path string) (sdk.FileListing, error) {
	options := sdk.ExecOptions{Timeout: 60 * time.Second}
	inGit, err := workspace.probe(ctx, "git", []string{"git", "rev-parse", "--is-inside-work-tree"})
	if err != nil {
		return sdk.FileListing{}, err
	}
	var files []string
	partial := false
	if inGit {
		result, err := workspace.ExecFile(ctx, []string{"git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", path}, options)
		if err != nil {
			return sdk.FileListing{}, err
		}
		if result.ExitCode == 0 {
			files = []string{}
			seen := map[string]bool{}
			for _, file := range strings.Split(result.Stdout, "\x00") {
				if file != "" && !seen[file] {
					files = append(files, file)
					seen[file] = true
				}
			}
			partial = strings.Contains(result.Stdout, " characters cut …]")
		}
	}
	if files == nil {
		hasRG, err := workspace.probe(ctx, "rg", []string{"rg", "--version"})
		if err != nil {
			return sdk.FileListing{}, err
		}
		if hasRG {
			result, err := workspace.ExecFile(ctx, []string{"rg", "--files", "--", path}, options)
			if err != nil {
				return sdk.FileListing{}, err
			}
			if result.ExitCode == 0 || result.ExitCode == 1 {
				files = []string{}
				for _, line := range strings.Split(result.Stdout, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						files = append(files, line)
					}
				}
				partial = strings.Contains(result.Stdout, " characters cut …]")
			}
		}
	}
	if files == nil {
		return sdk.Walk(ctx, workspace, path)
	}
	for index, file := range files {
		files[index] = strings.TrimPrefix(strings.ReplaceAll(file, "\\", "/"), "./")
	}
	return sdk.FileListing{Files: files, Partial: partial}, nil
}
func (workspace *Workspace) Search(ctx context.Context, pattern, path string, options sdk.SearchOptions) (sdk.FileSearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	glob := strings.TrimPrefix(strings.TrimSpace(options.Glob), "./")
	keep, err := sdk.SearchFilter(glob)
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	hasRG, err := workspace.probe(ctx, "rg", []string{"rg", "--version"})
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	inGit, err := workspace.probe(ctx, "git", []string{"git", "rev-parse", "--is-inside-work-tree"})
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	caseless := []string{}
	if options.IgnoreCase {
		caseless = append(caseless, "-i")
	}
	atRoot := path == "" || path == "." || path == "./"
	argv := []string{}
	if hasRG {
		argv = []string{"rg", "-n", "--no-heading", "--color", "never"}
		if inGit {
			argv = append(argv, "--hidden", "-g", "!.git")
		}
		if glob != "" && (atRoot || !strings.ContainsRune(glob, '/')) {
			argv = append(argv, "-g", glob)
		}
		argv = append(argv, caseless...)
		argv = append(argv, "--max-count", "50", "-e", pattern, "--", path)
	} else if inGit {
		argv = []string{"git", "grep", "-n", "-I", "-E"}
		argv = append(argv, caseless...)
		argv = append(argv, "--untracked", "--no-color", "-e", pattern, "--")
		under := path
		if glob != "" && atRoot && !strings.ContainsRune(glob, '{') {
			under = glob
			if !strings.ContainsRune(under, '/') {
				under = "**/" + under
			}
			under = ":(glob)" + under
		}
		argv = append(argv, under)
	} else {
		argv = []string{"grep", "-rnIE"}
		argv = append(argv, caseless...)
		argv = append(argv, "--exclude-dir=.git", "--exclude-dir=node_modules", "-m", "50", "-e", pattern, "--", path)
	}
	result, err := workspace.ExecFile(ctx, argv, sdk.ExecOptions{Timeout: 30 * time.Second})
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	if !hasRG && !inGit && result.ExitCode == 127 {
		return sdk.SearchByReading(ctx, workspace, pattern, path, options)
	}
	if result.ExitCode != 0 && result.ExitCode != 1 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return sdk.FileSearchResult{Lines: []string{}, Error: &message}, nil
	}
	lines := []string{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "./")
		if keep(line) {
			lines = append(lines, line)
		}
	}
	return sdk.FileSearchResult{Lines: sdk.CappedMatches(lines), More: strings.Contains(result.Stdout, " characters cut …]")}, nil
}

type searchableView struct {
	workspace *Workspace
	ref       string
}

func SearchableView(workspace *Workspace, ref string) sdk.SourceView {
	return sdk.ReadOnlyView(searchableView{workspace: workspace, ref: ref})
}
func (view searchableView) Ref() string { return view.ref }
func (view searchableView) check(path string) (string, error) {
	checked, err := sdk.ViewPath(path)
	if err != nil {
		return "", err
	}
	_, err = view.workspace.Resolve(checked)
	return checked, err
}
func (view searchableView) ReadBytes(ctx context.Context, path string, max int64) ([]byte, error) {
	checked, err := view.check(path)
	if err != nil {
		return nil, err
	}
	return view.workspace.ReadBytes(ctx, checked, max)
}
func (view searchableView) List(ctx context.Context, path string) ([]sdk.DirectoryEntry, error) {
	checked, err := view.check(path)
	if err != nil {
		return nil, err
	}
	return view.workspace.List(ctx, checked)
}
func (view searchableView) Files(ctx context.Context, path string) (sdk.FileListing, error) {
	checked, err := view.check(path)
	if err != nil {
		return sdk.FileListing{}, err
	}
	found, err := view.workspace.Files(ctx, checked)
	if err != nil {
		return sdk.FileListing{}, err
	}
	kept := []string{}
	for _, file := range found.Files {
		if err := ctx.Err(); err != nil {
			return sdk.FileListing{}, err
		}
		if _, err := view.check(file); err == nil {
			kept = append(kept, file)
		}
	}
	found.Files = kept
	return found, nil
}
func (view searchableView) Search(ctx context.Context, pattern, path string, options sdk.SearchOptions) (sdk.FileSearchResult, error) {
	checked, err := view.check(path)
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	found, err := view.workspace.Search(ctx, pattern, checked, options)
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	kept := []string{}
	seen := map[string]bool{}
	checkedFiles := map[string]bool{}
	for _, line := range found.Lines {
		file, _, _ := strings.Cut(line, ":")
		if !checkedFiles[file] {
			_, err := view.check(file)
			seen[file] = err == nil
			checkedFiles[file] = true
		}
		if seen[file] {
			kept = append(kept, line)
		}
	}
	found.Lines = kept
	return found, nil
}
