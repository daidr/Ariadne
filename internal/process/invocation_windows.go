//go:build windows

package process

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func escapeCmdMeta(value string) string {
	var escaped strings.Builder
	for _, char := range value {
		if strings.ContainsRune("()[]%!^\"`<>&|;, *?", char) {
			escaped.WriteByte('^')
		}
		escaped.WriteRune(char)
	}
	return escaped.String()
}
func batchArgument(value string) string {
	// Windows argv quoting, followed by both cmd parsing passes used by a
	// batch shim forwarding %*. This is the cross-spawn quoting algorithm:
	// https://github.com/moxystudio/node-cross-spawn/blob/master/lib/util/escape.js
	var quoted strings.Builder
	quoted.WriteByte('"')
	slashes := 0
	for _, char := range value {
		if char == '\\' {
			slashes++
			continue
		}
		if char == '"' {
			quoted.WriteString(strings.Repeat("\\", slashes*2+1))
			quoted.WriteByte('"')
		} else {
			quoted.WriteString(strings.Repeat("\\", slashes))
			quoted.WriteRune(char)
		}
		slashes = 0
	}
	quoted.WriteString(strings.Repeat("\\", slashes*2))
	quoted.WriteByte('"')
	return escapeCmdMeta(escapeCmdMeta(quoted.String()))
}
func prepareInvocation(command *exec.Cmd) error {
	extension := strings.ToLower(filepath.Ext(command.Path))
	if extension != ".cmd" && extension != ".bat" {
		return nil
	}
	for _, arg := range append([]string{command.Path}, command.Args[1:]...) {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return errors.New("batch command arguments cannot contain NUL or a line break")
		}
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	arguments := []string{escapeCmdMeta(command.Path)}
	for _, arg := range command.Args[1:] {
		arguments = append(arguments, batchArgument(arg))
	}
	command.Path = filepath.Join(system, "cmd.exe")
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(command.Path) + ` /d /s /v:off /c "` + strings.Join(arguments, " ") + `"`}
	return nil
}
