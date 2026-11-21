package textutil

import "testing"

func TestChatMarkdownRepairsOnlyProseParagraphs(t *testing.T) {
	prose := "已通过检查。\\n\\n合入前请确认。"
	code := "\n\n`\\n\\n` 和 [链接](/\\n\\n)\n\n"
	fenced := "~~~js\nconst lines = '\\n\\n'\n~~~\n"
	if got, want := ChatMarkdown(prose+code+fenced), "已通过检查。\n\n合入前请确认。"+code+fenced; got != want {
		t.Fatalf("ChatMarkdown = %q, want %q", got, want)
	}
}
