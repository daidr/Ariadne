package collab

import (
	"reflect"
	"testing"
)

func TestParseMarkdownCJKFriendlyEmphasisAndStrikethrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		markdown string
		want     []documentInline
	}{
		{
			name:     "full-width parens adjacent to bold and CJK text",
			markdown: "制作了**桌面工作台（1280×860）**与**移动端 PWA（390×800）**两套",
			want: []documentInline{
				{text: "制作了"},
				{text: "桌面工作台（1280×860）", marks: contentObject{"bold": true}},
				{text: "与"},
				{text: "移动端 PWA（390×800）", marks: contentObject{"bold": true}},
				{text: "两套"},
			},
		},
		{
			name:     "corner brackets inside bold",
			markdown: "这是**「重要」**内容",
			want: []documentInline{
				{text: "这是"},
				{text: "「重要」", marks: contentObject{"bold": true}},
				{text: "内容"},
			},
		},
		{
			name:     "book title marks inside bold",
			markdown: "推荐阅读**《架构设计》**一书",
			want: []documentInline{
				{text: "推荐阅读"},
				{text: "《架构设计》", marks: contentObject{"bold": true}},
				{text: "一书"},
			},
		},
		{
			name:     "curly quotes inside bold flanked by CJK",
			markdown: "这是**“引号”**测试",
			want: []documentInline{
				{text: "这是"},
				{text: "“引号”", marks: contentObject{"bold": true}},
				{text: "测试"},
			},
		},
		{
			name:     "full-width exclamation before closing bold",
			markdown: "支持**测试！**后面",
			want: []documentInline{
				{text: "支持"},
				{text: "测试！", marks: contentObject{"bold": true}},
				{text: "后面"},
			},
		},
		{
			name:     "half-width parens inside bold flanked by CJK",
			markdown: "这是**(半角括号)**测试",
			want: []documentInline{
				{text: "这是"},
				{text: "(半角括号)", marks: contentObject{"bold": true}},
				{text: "测试"},
			},
		},
		{
			name:     "full-width parens flanked by ASCII words",
			markdown: "foo**（全角括号）**bar",
			want: []documentInline{
				{text: "foo"},
				{text: "（全角括号）", marks: contentObject{"bold": true}},
				{text: "bar"},
			},
		},
		{
			name:     "italic adjacent to full-width parens",
			markdown: "注意*（可选）*参数",
			want: []documentInline{
				{text: "注意"},
				{text: "（可选）", marks: contentObject{"italic": true}},
				{text: "参数"},
			},
		},
		{
			name:     "strikethrough adjacent to full-width parens",
			markdown: "包含~~删除（测试）~~内容",
			want: []documentInline{
				{text: "包含"},
				{text: "删除（测试）", marks: contentObject{"strike": true}},
				{text: "内容"},
			},
		},
		{
			name:     "intraword underscores remain literal in ASCII",
			markdown: "foo_bar_baz",
			want: []documentInline{
				{text: "foo_bar_baz"},
			},
		},
		{
			name:     "pure ASCII punctuation flanking keeps CommonMark behavior",
			markdown: "a**(b)**c",
			want: []documentInline{
				{text: "a**(b)**c"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blocks, err := parseMarkdown(tc.markdown)
			if err != nil {
				t.Fatalf("parseMarkdown(%q): %v", tc.markdown, err)
			}
			if len(blocks) != 1 || blocks[0].kind != "paragraph" {
				t.Fatalf("expected 1 paragraph block, got %#v", blocks)
			}
			if !reflect.DeepEqual(blocks[0].inline, tc.want) {
				t.Fatalf("inline mismatch for %q\ngot:  %#v\nwant: %#v", tc.markdown, blocks[0].inline, tc.want)
			}
		})
	}
}

func TestDocumentRoundTripAndQuoteWithCJKPunctuationBold(t *testing.T) {
	t.Parallel()

	source := "制作了**桌面工作台（1280×860）**与**移动端 PWA（390×800）**两套方案。\n"

	if !ContainsDocumentQuote(source, "桌面工作台（1280×860）与移动端 PWA（390×800）") {
		t.Fatal("ContainsDocumentQuote did not match visible text across CJK bold runs")
	}

	doc, err := Load("document", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Destroy()

	if err := UpdateDocument(doc, source, "agent"); err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}

	serialized, err := ReadDocument(doc)
	if err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}
	if serialized != source {
		t.Fatalf("ReadDocument altered CJK bold Markdown\ngot:  %q\nwant: %q", serialized, source)
	}

	reparsed, err := parseMarkdown(serialized)
	if err != nil {
		t.Fatalf("parseMarkdown(serialized): %v", err)
	}
	want := []documentInline{
		{text: "制作了"},
		{text: "桌面工作台（1280×860）", marks: contentObject{"bold": true}},
		{text: "与"},
		{text: "移动端 PWA（390×800）", marks: contentObject{"bold": true}},
		{text: "两套方案。"},
	}
	if len(reparsed) != 1 || !reflect.DeepEqual(reparsed[0].inline, want) {
		t.Fatalf("round-trip inline mismatch\nserialized: %q\ngot:        %#v\nwant:       %#v", serialized, reparsed, want)
	}

	// Pure ASCII punctuation adjacent to ASCII letters still falls back to HTML tags
	// so serialized output remains valid CommonMark.
	asciiDoc, err := Load("document", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer asciiDoc.Destroy()
	if err := UpdateDocument(asciiDoc, "a<strong>(b)</strong>c\n", "agent"); err != nil {
		t.Fatalf("UpdateDocument ascii: %v", err)
	}
	asciiOut, err := ReadDocument(asciiDoc)
	if err != nil {
		t.Fatalf("ReadDocument ascii: %v", err)
	}
	if asciiOut != "a<strong>(b)</strong>c\n" {
		t.Fatalf("expected HTML tag fallback for pure ASCII punctuation flanking, got %q", asciiOut)
	}
}

func TestParseMarkdownCJKFriendlyInsideListsAndTables(t *testing.T) {
	t.Parallel()

	source := "- [x] 完成**桌面端（1280×860）**与**移动端（390×800）**验证\n\n| 模块 | 说明 |\n| --- | --- |\n| 编辑器 | 支持**「加粗」**和~~删除（旧版）~~语法 |\n"
	blocks, err := parseMarkdown(source)
	if err != nil {
		t.Fatalf("parseMarkdown: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (task item + table), got %d: %#v", len(blocks), blocks)
	}

	wantListInline := []documentInline{
		{text: "完成"},
		{text: "桌面端（1280×860）", marks: contentObject{"bold": true}},
		{text: "与"},
		{text: "移动端（390×800）", marks: contentObject{"bold": true}},
		{text: "验证"},
	}
	if blocks[0].kind != "checkItem" || !reflect.DeepEqual(blocks[0].inline, wantListInline) {
		t.Fatalf("task item mismatch\ngot:  %#v\nwant: %#v", blocks[0], wantListInline)
	}

	if blocks[1].kind != "table" || blocks[1].table == nil || len(blocks[1].table.rows) != 2 || len(blocks[1].table.rows[1].cells) != 2 {
		t.Fatalf("unexpected table structure: %#v", blocks[1])
	}
	wantCellInline := []documentInline{
		{text: "支持"},
		{text: "「加粗」", marks: contentObject{"bold": true}},
		{text: "和"},
		{text: "删除（旧版）", marks: contentObject{"strike": true}},
		{text: "语法"},
	}
	if got := blocks[1].table.rows[1].cells[1]; !reflect.DeepEqual(got, wantCellInline) {
		t.Fatalf("table cell mismatch\ngot:  %#v\nwant: %#v", got, wantCellInline)
	}
}
