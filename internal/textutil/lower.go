// Package textutil preserves text behavior at the JavaScript-to-Go boundary.
package textutil

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Lower uses full Unicode lowercase mapping, including expanding dotted I and
// context-dependent final sigma. A caser is never shared across goroutines.
func Lower(value string) string { return cases.Lower(language.Und).String(value) }
