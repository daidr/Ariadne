package background

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/textutil"
)

var served = regexp.MustCompile(`\bhttps?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1?\]):\d{2,5}(?:/[^\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}'"<>)\]]*)?`)
var serving = regexp.MustCompile(`(?i)\b(?:local|listening|running|ready|serving|started|available)\b`)

func ServedURL(output string) *string {
	lines := strings.SplitAfter(textutil.ScreenText(output), "\n")
	find := func(line string) *string {
		for _, match := range served.FindAllStringIndex(line, -1) {
			if match[1] >= len(line) {
				continue
			}
			next, _ := utf8.DecodeRuneInString(line[match[1]:])
			if !textutil.IsSpace(next) && !strings.ContainsRune("'\"<>)],", next) {
				continue
			}
			value := line[match[0]:match[1]]
			value = strings.Replace(value, "//0.0.0.0:", "//localhost:", 1)
			value = strings.Replace(value, "//[::]:", "//localhost:", 1)
			value = strings.TrimRight(value, ".,;:")
			value = strings.Clone(value)
			return &value
		}
		return nil
	}
	for _, line := range lines {
		if serving.MatchString(line) {
			if value := find(line); value != nil {
				return value
			}
		}
	}
	for _, line := range lines {
		if value := find(line); value != nil {
			return value
		}
	}
	return nil
}
