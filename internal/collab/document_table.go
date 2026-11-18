package collab

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/antst/go-yjs/crdt"
	"github.com/google/uuid"
	"golang.org/x/net/html"
)

const maxTableCells = 32768

type documentColumn struct {
	id    string
	align string
	width int
}

type documentRow struct {
	id    string
	cells [][]documentInline
}

type documentTable struct {
	headerRow, headerColumn bool
	columns                 []documentColumn
	rows                    []documentRow
	merges                  []tableMerge
}

type tableMerge struct {
	id            string
	row, column   int
	height, width int
}

func parseDocumentTable(node *html.Node) (*documentTable, error) {
	table := &documentTable{headerRow: true}
	var rows []*html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		switch child.Data {
		case "thead", "tbody", "tfoot":
			for row := child.FirstChild; row != nil; row = row.NextSibling {
				if row.Type == html.ElementNode && row.Data == "tr" {
					rows = append(rows, row)
				}
			}
		case "tr":
			rows = append(rows, child)
		}
	}
	if len(rows) > maxTableCells {
		return nil, errors.New("document table has too many rows")
	}
	table.rows = make([]documentRow, len(rows))
	covered := make([]map[int]bool, len(rows))
	var colWidths []int
	for node := node.FirstChild; node != nil; node = node.NextSibling {
		if node.Data != "colgroup" {
			continue
		}
		for col := node.FirstChild; col != nil; col = col.NextSibling {
			if col.Data != "col" {
				continue
			}
			width, _ := strconv.Atoi(strings.TrimSuffix(styleValue(col, "width"), "px"))
			for range min(positiveAttribute(col, "span"), maxTableCells-len(colWidths)) {
				colWidths = append(colWidths, max(0, width))
			}
		}
	}
	firstCol, haveBody := true, false
	for r, row := range rows {
		c := 0
		for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type != html.ElementNode || cell.Data != "td" && cell.Data != "th" {
				continue
			}
			for covered[r][c] {
				c++
			}
			height := min(positiveAttribute(cell, "rowspan"), len(rows)-r)
			width := positiveAttribute(cell, "colspan")
			if c+width > maxTableCells/max(1, len(rows)) {
				return nil, errors.New("document table has too many cells")
			}
			if r == 0 {
				table.headerRow = table.headerRow && cell.Data == "th"
			}
			if c == 0 && (r != 0 || !table.headerRow) {
				firstCol, haveBody = firstCol && cell.Data == "th", true
			}
			for len(table.columns) < c+width {
				table.columns = append(table.columns, documentColumn{})
			}
			if align, _ := validAlign(styleValue(cell, "text-align")).(string); align != "" && table.columns[c].align == "" {
				table.columns[c].align = align
			}
			for len(table.rows[r].cells) <= c {
				table.rows[r].cells = append(table.rows[r].cells, nil)
			}
			table.rows[r].cells[c] = readChildrenInlines(cell)
			if height*width > 1 {
				table.merges = append(table.merges, tableMerge{row: r, column: c, height: height, width: width})
			}
			for y := r; y < r+height; y++ {
				if covered[y] == nil {
					covered[y] = make(map[int]bool)
				}
				for x := c; x < c+width; x++ {
					covered[y][x] = true
				}
			}
			c += width
		}
	}
	for i := range min(len(table.columns), len(colWidths)) {
		table.columns[i].width = colWidths[i]
	}
	table.headerColumn = firstCol && haveBody
	return table, nil
}

func positiveAttribute(node *html.Node, key string) int {
	n, _ := strconv.Atoi(attribute(node, key))
	return min(max(n, 1), maxTableCells)
}

func orderedKeys(container *crdt.YMap, order string) []string {
	if container == nil {
		return nil
	}
	orderKeys := map[string]string{}
	for id, value := range container.Entries() {
		if fields, ok := value.(*crdt.YMap); ok {
			orderKeys[id] = stringField(fields, order)
		}
	}
	keys := slices.Collect(maps.Keys(orderKeys))
	slices.SortFunc(keys, func(a, b string) int {
		if c := strings.Compare(orderKeys[a], orderKeys[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return keys
}

func readDocumentTable(block *crdt.YMap, props contentObject) (*documentTable, error) {
	table := &documentTable{headerRow: props["headerRow"] != false, headerColumn: props["headerColumn"] == true}
	columns, _ := block.Get("columns").(*crdt.YMap)
	rows, _ := block.Get("rows").(*crdt.YMap)
	cells, _ := block.Get("cells").(*crdt.YMap)
	rowIDs, columnIDs := orderedKeys(rows, "order"), orderedKeys(columns, "order")
	if len(rowIDs) > maxTableCells/max(1, len(columnIDs)) {
		return nil, errors.New("document table has too many cells")
	}
	for _, id := range columnIDs {
		fields := columns.Get(id).(*crdt.YMap)
		table.columns = append(table.columns, documentColumn{id: id, align: stringField(fields, "align"), width: int(documentNumber(fields.Get("width")))})
	}
	for _, id := range rowIDs {
		row := documentRow{id: id, cells: make([][]documentInline, len(columnIDs))}
		if cells != nil {
			for i, column := range columnIDs {
				text, _ := cells.Get(id + ":" + column).(*crdt.YText)
				row.cells[i] = readDocumentText(text)
			}
		}
		table.rows = append(table.rows, row)
	}
	if merges, ok := block.Get("merges").(*crdt.YMap); ok {
		ids := slices.Sorted(maps.Keys(merges.Entries()))
		for _, id := range ids {
			fields := toObject(merges.Get(id))
			top, bottom := mergeSpan(fields["rows"], rowIDs)
			left, right := mergeSpan(fields["columns"], columnIDs)
			if top < 0 || left < 0 || (bottom-top)*(right-left) < 2 {
				continue
			}
			if slices.ContainsFunc(table.merges, func(m tableMerge) bool {
				return top < m.row+m.height && m.row < bottom && left < m.column+m.width && m.column < right
			}) {
				continue
			}
			table.merges = append(table.merges, tableMerge{id: id, row: top, column: left, height: bottom - top, width: right - left})
		}
	}
	return table, nil
}

func mergeSpan(items any, all []string) (int, int) {
	list, _ := items.([]any)
	low, high := len(all), -1
	for _, raw := range list {
		if id, ok := raw.(string); ok {
			if index := slices.Index(all, id); index >= 0 {
				low, high = min(low, index), max(high, index)
			}
		}
	}
	if high < 0 {
		return -1, -1
	}
	return low, high + 1
}

func writeDocumentTable(block *crdt.YMap, next, old *documentTable) {
	rows, cols, cells, merges := ensureMap(block, "rows"), ensureMap(block, "columns"), ensureMap(block, "cells"), ensureMap(block, "merges")
	previousRows, previousColumns := orderedKeys(rows, "order"), orderedKeys(cols, "order")
	nextRows := make([]string, len(next.rows))
	nextCols := make([]string, len(next.columns))
	for i := range next.rows {
		if i < len(previousRows) {
			nextRows[i] = previousRows[i]
		} else {
			nextRows[i] = uuid.NewString()
		}
	}
	for i, column := range next.columns {
		if i < len(previousColumns) {
			nextCols[i] = previousColumns[i]
		} else {
			nextCols[i] = uuid.NewString()
		}
		fields := ensureMap(cols, nextCols[i])
		if column.align != "" && column.align != "left" {
			fields.Set("align", column.align)
		} else {
			fields.Delete("align")
		}
		if column.width > 0 {
			fields.Set("width", column.width)
		} else {
			fields.Delete("width")
		}
	}
	setSequence(rows, previousRows, nextRows, "order")
	setSequence(cols, previousColumns, nextCols, "order")
	for _, key := range cells.Keys() {
		row, column, _ := strings.Cut(key, ":")
		if !slices.Contains(nextRows, row) || !slices.Contains(nextCols, column) {
			cells.Delete(key)
		}
	}
	for i, row := range next.rows {
		for j, column := range nextCols {
			var value []documentInline
			if j < len(row.cells) {
				value = row.cells[j]
			}
			key := nextRows[i] + ":" + column
			text, _ := cells.Get(key).(*crdt.YText)
			if text == nil {
				text = crdt.NewYText("")
				cells.Set(key, text)
			}
			writeDocumentText(text, value)
		}
	}
	keep := map[string]bool{}
	for _, merge := range next.merges {
		id := ""
		if old != nil {
			for _, previous := range old.merges {
				if previous.row == merge.row && previous.column == merge.column && previous.width == merge.width && previous.height == merge.height {
					id = previous.id
					break
				}
			}
		}
		if id == "" {
			id = uuid.NewString()
		}
		keep[id] = true
		fields := contentObject{"rows": asAnyStrings(nextRows[merge.row : merge.row+merge.height]),
			"columns": asAnyStrings(nextCols[merge.column : merge.column+merge.width])}
		if !equalJSON(fields, merges.Get(id)) {
			merges.Set(id, sharedJSON(fields))
		}
	}
	for _, id := range merges.Keys() {
		if !keep[id] {
			merges.Delete(id)
		}
	}
}

func asAnyStrings(values []string) []any {
	result := make([]any, len(values))
	for i, v := range values {
		result[i] = v
	}
	return result
}

func writeMarkdownTable(table *documentTable) string {
	if table == nil || len(table.rows) == 0 || len(table.columns) == 0 {
		return ""
	}
	covered := map[[2]int]bool{}
	for _, span := range table.merges {
		for r := span.row; r < span.row+span.height; r++ {
			for c := span.column; c < span.column+span.width; c++ {
				if r != span.row || c != span.column {
					covered[[2]int{r, c}] = true
				}
			}
		}
	}
	rule := make([]string, len(table.columns))
	for i, column := range table.columns {
		switch column.align {
		case "center":
			rule[i] = ":-:"
		case "right":
			rule[i] = "--:"
		default:
			rule[i] = "---"
		}
	}
	lines := make([]string, 0, len(table.rows)+1)
	line := func(cells []string) string { return "| " + strings.Join(cells, " | ") + " |" }
	for i, row := range table.rows {
		words := make([]string, len(table.columns))
		for j, cell := range row.cells[:min(len(row.cells), len(words))] {
			if !covered[[2]int{i, j}] {
				words[j] = strings.ReplaceAll(strings.ReplaceAll(renderInlines(cell, false), "\\\n", "<br>"), "\n", " ")
			}
		}
		lines = append(lines, line(words))
		if i == 0 {
			lines = append(lines, line(rule))
		}
	}
	return strings.Join(lines, "\n")
}
