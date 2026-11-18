package textutil

import "testing"

func TestEmojiUsesOneGraphemeAndExistingPropertyRule(t *testing.T) {
	for _, emoji := range []string{"👍", "👩🏽‍💻", "🇨🇳", "👨‍👩‍👧‍👦", "©️", "🏳️‍🌈"} {
		if !IsEmoji(emoji) {
			t.Errorf("rejected %q", emoji)
		}
	}
	for _, other := range []string{"", "a", "👍👍", "1️⃣", " 👍", "\xff", "🇨🇳🇺🇸"} {
		if IsEmoji(other) {
			t.Errorf("accepted %q", other)
		}
	}
}
