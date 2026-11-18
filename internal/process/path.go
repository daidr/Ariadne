package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func environmentValue(environment []string, key string) string {
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value
		}
	}
	return ""
}
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0)
}

// exec.Command normally looks up PATH in the backend's environment. Commands
// need the environment and working directory chosen for their own workspace.
func resolveProgram(program string, environment []string, directory string) (string, error) {
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	abs := func(path string) string {
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		absolute, _ := filepath.Abs(path)
		return absolute
	}
	extensions := []string{""}
	if runtime.GOOS == "windows" {
		raw := environmentValue(environment, "PATHEXT")
		if raw == "" {
			raw = ".COM;.EXE;.BAT;.CMD"
		}
		for _, extension := range strings.Split(raw, ";") {
			if extension != "" {
				extensions = append(extensions, extension)
			}
		}
	}
	find := func(path string) string {
		for _, extension := range extensions {
			candidate := abs(path + extension)
			if executable(candidate) {
				return candidate
			}
		}
		return ""
	}
	hasSeparator := false
	for i := 0; i < len(program); i++ {
		hasSeparator = hasSeparator || os.IsPathSeparator(program[i])
	}
	if filepath.IsAbs(program) || hasSeparator {
		if path := find(program); path != "" {
			return path, nil
		}
		return "", &exec.Error{Name: program, Err: exec.ErrNotFound}
	}
	paths := filepath.SplitList(environmentValue(environment, "PATH"))
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, directory := range paths {
		if path := find(filepath.Join(directory, program)); path != "" {
			return path, nil
		}
	}
	return "", &exec.Error{Name: program, Err: exec.ErrNotFound}
}
