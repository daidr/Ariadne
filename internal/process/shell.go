package process

import (
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/textutil"
)

type Shell struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

func SplitCommand(line string) []string { return splitCommand(line, runtime.GOOS == "windows") }
func splitCommand(line string, windows bool) []string {
	words := []string{}
	var word strings.Builder
	started := false
	var quote rune
	characters := []rune(line)
	for i := 0; i < len(characters); i++ {
		char := characters[i]
		if quote != 0 {
			if char == quote {
				quote = 0
			} else if char == '\\' && quote == '"' && i+1 < len(characters) && (!windows || characters[i+1] == '"') {
				i++
				word.WriteRune(characters[i])
			} else {
				word.WriteRune(char)
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
			started = true
		} else if char == '\\' && i+1 < len(characters) && !windows {
			i++
			word.WriteRune(characters[i])
			started = true
		} else if textutil.IsSpace(char) {
			if started {
				words = append(words, word.String())
			}
			word.Reset()
			started = false
		} else {
			word.WriteRune(char)
			started = true
		}
	}
	if started {
		words = append(words, word.String())
	}
	return words
}

func DefaultShell() Shell {
	if runtime.GOOS != "windows" {
		return Shell{Name: "bash", Argv: []string{"bash", "-c"}}
	}
	name := "powershell"
	if _, err := exec.LookPath("pwsh"); err == nil {
		name = "pwsh"
	}
	return Shell{Name: name, Argv: []string{name, "-NoProfile", "-NonInteractive", "-Command"}}
}
func ShellArgv(shell []string, command string) []string {
	if len(shell) == 0 {
		return []string{command}
	}
	parts := strings.FieldsFunc(shell[0], func(char rune) bool { return char == '/' || char == '\\' })
	name := ""
	if len(parts) > 0 {
		name = strings.ToLower(parts[len(parts)-1])
	}
	powershell := name == "pwsh" || name == "powershell" || name == "pwsh.exe" || name == "powershell.exe"
	if !powershell || !strings.EqualFold(shell[len(shell)-1], "-command") {
		return append(append([]string{}, shell...), command)
	}
	units := utf16.Encode([]rune("[Console]::OutputEncoding = $OutputEncoding = [Text.UTF8Encoding]::new($false)\n" + command))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2*i:], unit)
	}
	return append(append([]string{}, shell[:len(shell)-1]...), "-EncodedCommand", base64.StdEncoding.EncodeToString(data))
}
