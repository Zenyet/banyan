// Package preview reads what binary files hold, to show them in the app:
// the text of documents, the cells of spreadsheets, and hex dumps of
// anything else. It is pure Go, with no UI of its own.
package preview

import (
	"fmt"
	"path"
	"strings"
)

// Kind is what a file holds that the app can show.
type Kind int

const (
	// Image is a picture: png, jpeg, gif, webp or bmp.
	Image Kind = iota
	// Spreadsheet is a workbook of sheets, as xlsx has them.
	Spreadsheet
	// Document is a file whose text reads in the diff viewer: docx, pptx,
	// odt, odp or ods.
	Document
	// Hex is anything else, whose bytes show as a hex dump.
	Hex
)

// KindOf says how the file at p is shown, by its extension: as a picture, a
// spreadsheet, a document's text, or a hex dump.
func KindOf(p string) Kind {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return Image
	case ".xlsx", ".xlsm":
		return Spreadsheet
	case ".docx", ".docm", ".pptx", ".pptm", ".odt", ".odp", ".ods":
		return Document
	}
	return Hex
}

// HexLimit is how many bytes of a file the hex dump shows.
const HexLimit = 64 << 10

// HexDump is data as lines of 16 bytes: their offset in hex, the bytes, and
// the printable ones beside them. It shows the first HexLimit bytes, and
// says whether it left any out.
func HexDump(data []byte) (string, bool) {
	truncated := len(data) > HexLimit
	if truncated {
		data = data[:HexLimit]
	}
	var b strings.Builder
	for off := 0; off < len(data); off += 16 {
		end := min(off+16, len(data))
		fmt.Fprintf(&b, "%08x  ", off)
		for i := 0; i < 16; i++ {
			if i == 8 {
				b.WriteByte(' ')
			}
			if off+i < end {
				fmt.Fprintf(&b, "%02x ", data[off+i])
			} else {
				b.WriteString("   ")
			}
		}
		b.WriteByte(' ')
		for _, c := range data[off:end] {
			if c >= 0x20 && c < 0x7f {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), truncated
}

// DocumentText is the text of a document file, read in reading order, with
// a line per paragraph and a tab between the cells of a table.
func DocumentText(p string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".docx", ".docm":
		return docxText(data)
	case ".pptx", ".pptm":
		return pptxText(data)
	case ".odt", ".odp", ".ods":
		return odfText(data)
	}
	return "", fmt.Errorf("%s is not a document", path.Base(p))
}

// Sheet is a tab of a spreadsheet.
type Sheet struct {
	Name string
	// Rows are the texts of the cells, row by row, the empty cells at the
	// end of a row left out.
	Rows [][]string
}

// Sheets reads a spreadsheet's sheets in order, with the text of each cell.
func Sheets(p string, data []byte) ([]Sheet, error) {
	switch strings.ToLower(path.Ext(p)) {
	case ".xlsx", ".xlsm":
		return xlsxSheets(data)
	}
	return nil, fmt.Errorf("%s is not a spreadsheet", path.Base(p))
}

// Change is how a cell changed between two versions of a spreadsheet.
type Change uint8

const (
	// Unchanged cells read the same in both versions.
	Unchanged Change = iota
	// Added cells are only in the new version.
	Added
	// Deleted cells are only in the old one.
	Deleted
	// Modified cells read differently in both.
	Modified
)

// CellDiff is a cell in both versions of a sheet.
type CellDiff struct {
	// Before and After are the cell's texts, empty where it is not there.
	Before, After string
	Change        Change
}

// SheetDiff is a sheet in both versions of a spreadsheet: the wider of the
// two, cell by cell.
type SheetDiff struct {
	Name string
	Rows [][]CellDiff
	// Columns is the widest row, at least 1.
	Columns int
	// Changed counts the cells added, deleted or modified.
	Changed int
}

// CompareSheets compares two versions of a spreadsheet's sheets, matching
// them by name: those of the new version in its order, then those only in
// the old one.
func CompareSheets(before, after []Sheet) []SheetDiff {
	var out []SheetDiff
	used := make([]bool, len(before))
	for i := range after {
		a := &after[i]
		var b *Sheet
		for j := range before {
			if before[j].Name == a.Name {
				b, used[j] = &before[j], true
				break
			}
		}
		out = append(out, diffSheet(a.Name, b, a))
	}
	for j := range before {
		if !used[j] {
			out = append(out, diffSheet(before[j].Name, &before[j], nil))
		}
	}
	return out
}

// diffSheet compares a sheet of both versions, either of which may be
// missing.
func diffSheet(name string, before, after *Sheet) SheetDiff {
	var br, ar [][]string
	if before != nil {
		br = before.Rows
	}
	if after != nil {
		ar = after.Rows
	}
	d := SheetDiff{Name: name, Rows: make([][]CellDiff, max(len(br), len(ar)))}
	for i := range d.Rows {
		var b, a []string
		if i < len(br) {
			b = br[i]
		}
		if i < len(ar) {
			a = ar[i]
		}
		cols := max(len(b), len(a))
		row := make([]CellDiff, cols)
		for j := range row {
			var bv, av string
			if j < len(b) {
				bv = b[j]
			}
			if j < len(a) {
				av = a[j]
			}
			c := CellDiff{Before: bv, After: av}
			switch {
			case bv == av:
				c.Change = Unchanged
			case bv == "":
				c.Change = Added
			case av == "":
				c.Change = Deleted
			default:
				c.Change = Modified
			}
			if c.Change != Unchanged {
				d.Changed++
			}
			row[j] = c
		}
		d.Rows[i] = row
		d.Columns = max(d.Columns, cols)
	}
	if d.Columns == 0 {
		d.Columns = 1
	}
	return d
}

// tidyText trims the ends of lines and the blank lines that open and close
// a text, with blank runs kept to one line.
func tidyText(s string) string {
	var out []string
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
