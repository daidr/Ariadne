package hostinghttp

import (
	"net/url"
	"strings"
)

func FormComponent(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(value), "~", "%7E"), "%2A", "*")
}

// Component matches encodeURIComponent rather than Go's query encoding (+ for
// spaces) or path encoding (which leaves GitLab namespace slashes unescaped).
func Component(value string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.!~*'()", rune(c)) {
			out.WriteByte(c)
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
	return out.String()
}
