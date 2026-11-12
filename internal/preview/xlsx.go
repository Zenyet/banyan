package preview

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// workbookSheet is a sheet of a workbook: its name, and the part of the zip
// that holds its cells.
type workbookSheet struct {
	name string
	part string
}

// xlsxSheets reads an xlsx workbook's sheets in order, with each cell's
// text.
func xlsxSheets(data []byte) ([]Sheet, error) {
	files, err := zipFiles(data)
	if err != nil {
		return nil, err
	}
	shared := sharedStrings(zipRead(files, "xl/sharedStrings.xml"))
	sheets := workbookSheets(files)
	if len(sheets) == 0 {
		return nil, fmt.Errorf("the workbook has no sheets")
	}
	out := make([]Sheet, len(sheets))
	for i, s := range sheets {
		out[i] = Sheet{Name: s.name, Rows: worksheetRows(zipRead(files, s.part), shared)}
	}
	return out, nil
}

// sharedStrings reads the strings a worksheet's s cells point at.
func sharedStrings(raw []byte) []string {
	if raw == nil {
		return nil
	}
	var out []string
	var text strings.Builder
	inText, phonetic := false, 0
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				text.Reset()
			case "rPh":
				phonetic++
			case "t":
				inText = phonetic == 0
			}
		case xml.CharData:
			if inText {
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, text.String())
			case "rPh":
				if phonetic > 0 {
					phonetic--
				}
			case "t":
				inText = false
			}
		}
	}
	return out
}

// workbookSheets reads the sheets of a workbook, with the parts that hold
// them, falling back to the zip's worksheets in name order.
func workbookSheets(files map[string]*zip.File) []workbookSheet {
	rels := workbookRels(zipRead(files, "xl/_rels/workbook.xml.rels"))
	var sheets []workbookSheet
	dec := xml.NewDecoder(bytes.NewReader(zipRead(files, "xl/workbook.xml")))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		var name, rid string
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "name":
				name = a.Value
			case "id":
				rid = a.Value
			}
		}
		if name == "" {
			continue
		}
		part := rels[rid]
		if part == "" {
			part = path.Join("xl", "worksheets", "sheet"+strconv.Itoa(len(sheets)+1)+".xml")
		}
		sheets = append(sheets, workbookSheet{name: name, part: part})
	}
	if len(sheets) > 0 {
		return sheets
	}
	var parts []string
	for name := range files {
		if strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml") {
			parts = append(parts, name)
		}
	}
	sort.Slice(parts, func(i, j int) bool { return naturalLess(parts[i], parts[j]) })
	for i, part := range parts {
		sheets = append(sheets, workbookSheet{name: "Sheet " + strconv.Itoa(i+1), part: part})
	}
	return sheets
}

// workbookRels reads where a workbook's parts are, by the ids it names them
// by.
func workbookRels(raw []byte) map[string]string {
	out := map[string]string{}
	if raw == nil {
		return out
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var id, target string
		for _, a := range start.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id == "" || target == "" {
			continue
		}
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join("xl", target)
		}
		out[id] = path.Clean(target)
	}
	return out
}

// worksheetRows reads the cells of a worksheet part, by row and column,
// with the empty cells at the end of a row and the empty rows at the end
// left out.
func worksheetRows(raw []byte, shared []string) [][]string {
	if raw == nil {
		return nil
	}
	rows := map[int]map[int]string{}
	maxRow, maxCol := -1, 0
	row, nextRow, nextCol := 0, 0, 0
	col := 0
	typ := ""
	var value, inline strings.Builder
	inValue, inInline, inText := false, false, false
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				// A row without its number is the next one.
				row, nextCol = nextRow, 0
				if ix, ok := attrInt(t, "r"); ok {
					row = ix - 1
				}
				nextRow = row + 1
				maxRow = max(maxRow, row)
			case "c":
				col, typ = nextCol, ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						if ref := a.Value; ref != "" {
							col = cellColumn(ref)
						}
					case "t":
						typ = a.Value
					}
				}
				value.Reset()
				inline.Reset()
				inValue, inInline, inText = false, false, false
			case "v":
				inValue = true
			case "is":
				inInline = true
			case "t":
				inText = inInline
			}
		case xml.CharData:
			if inValue {
				value.Write(t)
			}
			if inText {
				inline.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "v":
				inValue = false
			case "c":
				cell := cellText(typ, value.String(), inline.String(), shared)
				if cell != "" {
					if rows[row] == nil {
						rows[row] = map[int]string{}
					}
					rows[row][col] = cell
				}
				maxCol = max(maxCol, col+1)
				nextCol = col + 1
			case "is":
				inInline = false
			}
		}
	}
	grid := make([][]string, maxRow+1)
	for r := 0; r <= maxRow; r++ {
		cells := rows[r]
		if len(cells) == 0 {
			continue
		}
		line := make([]string, maxCol)
		for c, text := range cells {
			if c < maxCol {
				line[c] = text
			}
		}
		for len(line) > 0 && line[len(line)-1] == "" {
			line = line[:len(line)-1]
		}
		grid[r] = line
	}
	for len(grid) > 0 && len(grid[len(grid)-1]) == 0 {
		grid = grid[:len(grid)-1]
	}
	return grid
}

// cellText is the text of a cell of a worksheet, by the type the sheet
// gives it.
func cellText(typ, value, inline string, shared []string) string {
	switch typ {
	case "s":
		if ix, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && ix >= 0 && ix < len(shared) {
			return shared[ix]
		}
		return ""
	case "b":
		switch strings.TrimSpace(value) {
		case "1":
			return "TRUE"
		case "0":
			return "FALSE"
		}
		return ""
	case "inlineStr":
		return strings.TrimSpace(inline)
	case "str":
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(value)
}

// cellColumn is the column of a cell reference as A1, from 0.
func cellColumn(ref string) int {
	n := 0
	for _, c := range ref {
		switch {
		case c >= 'A' && c <= 'Z':
			n = n*26 + int(c-'A') + 1
		case c >= 'a' && c <= 'z':
			n = n*26 + int(c-'a') + 1
		default:
			return max(n-1, 0)
		}
	}
	return max(n-1, 0)
}

// attrInt is an attribute of an element as an integer.
func attrInt(e xml.StartElement, name string) (int, bool) {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			n, err := strconv.Atoi(strings.TrimSpace(a.Value))
			return n, err == nil
		}
	}
	return 0, false
}

// naturalLess orders names with the numbers in them numerically, as
// sheet2 before sheet10.
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da != "" && db != "" {
			if len(da) != len(db) {
				return len(da) < len(db)
			}
			if da != db {
				return da < db
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

// digitPrefix is the digits that open s.
func digitPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
