package files

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/daidr/Ariadne/internal/domainerr"
)

const sheetRows = 250
const sheetColumns = 40
const sheetXMLBytes = 16 << 20
const sheetCellBytes = 4096

func isSpreadsheet(data []byte) bool {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return false
	}
	book, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	_, typeErr := fs.Stat(book, "[Content_Types].xml")
	_, bookErr := fs.Stat(book, "xl/workbook.xml")
	return typeErr == nil && bookErr == nil
}

type SheetPreview struct {
	Sheets     []string   `json:"sheets"`
	Rows       [][]string `json:"rows"`
	RowNumbers []int      `json:"rowNumbers"`
	Truncated  bool       `json:"truncated"`
}

type sheetText struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func cellPrefix(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return value[:maximum]
}

func (value sheetText) preview() (string, bool) {
	initial := cellPrefix(value.Text, sheetCellBytes)
	if len(value.Runs) == 0 || len(initial) < len(value.Text) {
		return initial, len(initial) < len(value.Text)
	}
	var text strings.Builder
	text.WriteString(initial)
	for _, run := range value.Runs {
		part := cellPrefix(run.Text, sheetCellBytes-text.Len())
		text.WriteString(part)
		if len(part) < len(run.Text) {
			return text.String(), true
		}
	}
	return text.String(), false
}

func readSheetXML(book *zip.Reader, name string, value any) error {
	file, err := book.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return xml.NewDecoder(io.LimitReader(file, sheetXMLBytes)).Decode(value)
}

func (store Store) PreviewSheet(ctx context.Context, id string, index int) (SheetPreview, error) {
	file, err := store.Read(ctx, id)
	if err != nil {
		return SheetPreview{}, err
	}
	if file.MIME != SpreadsheetMIME || len(file.Data) > MaxPreviewBytes {
		return SheetPreview{}, domainerr.New("filePreview.error", nil)
	}
	view, err := previewSheet(ctx, file.Data, index)
	if err != nil && ctx.Err() == nil {
		return SheetPreview{}, domainerr.New("filePreview.error", nil)
	}
	return view, err
}

func previewSheet(ctx context.Context, data []byte, index int) (SheetPreview, error) {
	book, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return SheetPreview{}, err
	}
	var workbook struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := readSheetXML(book, "xl/workbook.xml", &workbook); err != nil {
		return SheetPreview{}, err
	}
	if index < 0 || index >= len(workbook.Sheets) {
		return SheetPreview{}, errors.New("spreadsheet page does not exist")
	}
	var relationships struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := readSheetXML(book, "xl/_rels/workbook.xml.rels", &relationships); err != nil {
		return SheetPreview{}, err
	}
	name := ""
	for _, rel := range relationships.Items {
		if rel.ID != workbook.Sheets[index].ID || rel.Mode == "External" {
			continue
		}
		if strings.HasPrefix(rel.Target, "/") {
			name = path.Clean(strings.TrimLeft(rel.Target, "/"))
		} else {
			name = path.Join("xl", rel.Target)
		}
		break
	}
	sheet, err := book.Open(name)
	if err != nil {
		return SheetPreview{}, err
	}
	defer sheet.Close()
	var shared struct {
		Values []sheetText `xml:"si"`
	}
	if err := readSheetXML(book, "xl/sharedStrings.xml", &shared); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return SheetPreview{}, err
	}
	view := SheetPreview{Rows: [][]string{}, RowNumbers: []int{}}
	remaining := 2 << 20
	nextRow := 1
	for _, page := range workbook.Sheets {
		view.Sheets = append(view.Sheets, page.Name)
	}
	decoder := xml.NewDecoder(io.LimitReader(sheet, sheetXMLBytes))
	for {
		if err := ctx.Err(); err != nil {
			return SheetPreview{}, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return view, nil
		}
		if err != nil {
			return SheetPreview{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		if len(view.Rows) == sheetRows {
			view.Truncated = true
			return view, nil
		}
		var row struct {
			Index int `xml:"r,attr"`
			Cells []struct {
				Reference string    `xml:"r,attr"`
				Type      string    `xml:"t,attr"`
				Value     string    `xml:"v"`
				Inline    sheetText `xml:"is"`
			} `xml:"c"`
		}
		if err := decoder.DecodeElement(&row, &start); err != nil {
			return SheetPreview{}, err
		}
		cells := []string{}
		for _, cell := range row.Cells {
			column := 0
			for _, letter := range cell.Reference {
				if letter < 'A' || letter > 'Z' || column > sheetColumns {
					break
				}
				column = column*26 + int(letter-'A'+1)
			}
			if column == 0 {
				column = len(cells) + 1
			}
			if column > sheetColumns {
				view.Truncated = true
				continue
			}
			value := cell.Value
			var shortened bool
			switch cell.Type {
			case "s":
				if i, err := strconv.Atoi(value); err == nil && i >= 0 && i < len(shared.Values) {
					value, shortened = shared.Values[i].preview()
				}
			case "inlineStr":
				value, shortened = cell.Inline.preview()
			case "b":
				value = strconv.FormatBool(value == "1")
			}
			prefix := cellPrefix(value, sheetCellBytes)
			view.Truncated = view.Truncated || shortened || len(prefix) < len(value)
			value = prefix
			remaining -= len(value)
			if remaining < 0 {
				view.Truncated = true
				return view, nil
			}
			for len(cells) < column {
				cells = append(cells, "")
			}
			cells[column-1] = value
		}
		view.Rows = append(view.Rows, cells)
		nextRow = max(row.Index, nextRow)
		view.RowNumbers = append(view.RowNumbers, nextRow)
		nextRow++
	}
}
