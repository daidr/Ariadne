package local

import (
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

var systemLocale = sync.OnceValue(func() language.Tag { return localeTag(defaultLocale()) })

func nameComparator() func(string, string) int { return collate.New(systemLocale()).CompareString }

func localeTag(value string) language.Tag {
	value = strings.TrimSpace(value)
	value, _, _ = strings.Cut(value, ".")
	value, _, _ = strings.Cut(value, "@")
	if value == "" || value == "C" || value == "POSIX" {
		return language.AmericanEnglish
	}
	tag, err := language.Parse(strings.ReplaceAll(value, "_", "-"))
	if err != nil {
		return language.AmericanEnglish
	}
	return tag
}
