package textutil

import (
	"slices"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/clipperhouse/uax29/v2/graphemes"
)

// IsEmoji follows the existing reaction rule: one grapheme, at most 32 UTF-16
// units, containing an extended pictograph or a regional indicator.
func IsEmoji(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(utf16.Encode([]rune(value))) > 32 {
		return false
	}
	iterator := graphemes.FromString(value)
	if !iterator.Next() || iterator.Next() {
		return false
	}
	for _, point := range value {
		_, found := slices.BinarySearchFunc(emojiRanges, point, func(bounds [2]rune, target rune) int {
			if bounds[1] < target {
				return -1
			}
			if bounds[0] > target {
				return 1
			}
			return 0
		})
		if found {
			return true
		}
	}
	return false
}
