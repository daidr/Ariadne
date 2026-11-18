package textutil

import "strings"

// Trim uses ECMAScript whitespace for compatibility with saved user input.
func Trim(value string) string {
	return strings.TrimFunc(value, IsSpace)
}
func IsSpace(char rune) bool {
	return char >= 9 && char <= 13 || char == ' ' || char == 0xA0 || char == 0x1680 || char >= 0x2000 && char <= 0x200A || char == 0x2028 || char == 0x2029 || char == 0x202F || char == 0x205F || char == 0x3000 || char == 0xFEFF
}
