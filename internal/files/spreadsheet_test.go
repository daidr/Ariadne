package files

import (
	"archive/zip"
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func workbook(t *testing.T, sheet string) []byte {
	t.Helper()
	var data bytes.Buffer
	book := zip.NewWriter(&data)
	for name, contents := range map[string]string{
		"[Content_Types].xml":        "<Types/>",
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Checks" r:id="r1"/><sheet name="Next" r:id="r2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="worksheets/sheet.xml"/><Relationship Id="r2" Target="/xl/worksheets/next.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       "<sst><si><r><t>Build</t></r><r><t> passed</t></r></si></sst>",
		"xl/worksheets/sheet.xml":    "<worksheet><sheetData>" + sheet + "</sheetData></worksheet>",
		"xl/worksheets/next.xml":     `<worksheet><sheetData><row><c t="inlineStr"><is><t>Next steps</t></is></c></row></sheetData></worksheet>`,
	} {
		file, err := book.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := book.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSpreadsheetAttachmentPreview(t *testing.T) {
	store := newStore(t)
	data := workbook(t, `<row r="3"><c r="B3" t="s"><v>0</v></c><c r="D3" t="b"><v>1</v></c></row>`)
	file, err := store.Save(t.Context(), "p1", "application/octet-stream", data, true)
	if err != nil || file.MIME != SpreadsheetMIME {
		t.Fatalf("save XLSX: %+v %v", file, err)
	}
	preview, err := store.PreviewSheet(t.Context(), file.ID, 0)
	if err != nil || preview.Truncated ||
		!reflect.DeepEqual(preview.Sheets, []string{"Checks", "Next"}) ||
		!reflect.DeepEqual(preview.Rows, [][]string{{"", "Build passed", "", "true"}}) ||
		!reflect.DeepEqual(preview.RowNumbers, []int{3}) {
		t.Fatalf("first worksheet: %+v %v", preview, err)
	}
	preview, err = store.PreviewSheet(t.Context(), file.ID, 1)
	if err != nil || !reflect.DeepEqual(preview.Rows, [][]string{{"Next steps"}}) {
		t.Fatalf("second worksheet: %+v %v", preview, err)
	}
	if _, err := store.PreviewSheet(t.Context(), file.ID, 2); err == nil {
		t.Fatal("accepted nonexistent worksheet")
	}
}

func TestSpreadsheetPreviewBoundsRowsAndColumns(t *testing.T) {
	row := `<row><c r="A1" t="inlineStr"><is><t>` + strings.Repeat("表", 4000) +
		`</t></is></c><c r="AO1"><v>42</v></c></row>`
	preview, err := previewSheet(t.Context(), workbook(t, row+strings.Repeat("<row/>", sheetRows)), 0)
	if err != nil || !preview.Truncated || len(preview.Rows) != sheetRows ||
		len(preview.Rows[0]) != 1 || len(preview.Rows[0][0]) != 4095 {
		t.Fatalf("unexpected preview limits: rows=%d truncated=%t err=%v", len(preview.Rows), preview.Truncated, err)
	}
}
