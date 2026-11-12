package preview

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// zipOf builds a zip of the parts, as the document files are.
func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, text := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		"photo.PNG": Image, "a.jpeg": Image, "a.gif": Image, "a.webp": Image, "a.bmp": Image,
		"book.xlsx": Spreadsheet,
		"doc.docx":  Document, "slides.pptx": Document, "text.odt": Document, "deck.odp": Document, "sheet.ods": Document,
		"file.pdf": Hex, "file.bin": Hex, "main.go": Hex, "icon.svg": Hex,
	}
	for name, want := range cases {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHexDump(t *testing.T) {
	data := make([]byte, 20)
	for i := range data {
		data[i] = byte(i)
	}
	dump, truncated := HexDump(data)
	if truncated {
		t.Fatal("a short dump is not truncated")
	}
	lines := strings.Split(strings.TrimSuffix(dump, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines: %q", len(lines), dump)
	}
	if want := "00000000  00 01 02 03 04 05 06 07  08 09 0a 0b 0c 0d 0e 0f  ................"; lines[0] != want {
		t.Errorf("line 1:\n%q\nwant\n%q", lines[0], want)
	}
	if want := "00000010  10 11 12 13                                       ...."; lines[1] != want {
		t.Errorf("line 2:\n%q\nwant\n%q", lines[1], want)
	}
	big, truncated := HexDump(make([]byte, HexLimit+1))
	if !truncated {
		t.Fatal("a dump past the limit is truncated")
	}
	if lines := strings.Count(big, "\n"); lines != HexLimit/16 {
		t.Errorf("%d lines of a cut dump", lines)
	}
}

// xlsxFile is a workbook of two sheets: strings, numbers, a gap between
// cells, an inline string and a boolean.
func xlsxFile(t *testing.T) []byte {
	t.Helper()
	return zipOf(t, map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Data" sheetId="1" r:id="rId1"/><sheet name="Notes" sheetId="2" r:id="rId2"/></sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/sheet2.xml"/>
</Relationships>`,
		"xl/sharedStrings.xml": `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Name</t></si><si><r><t>Rich</t></r><r><t> Text</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><t>Value</t></is></c></row>
<row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2"><v>42</v></c><c r="D2"><v>7</v></c></row>
<row r="3"><c r="A3" t="b"><v>1</v></c></row>
</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`,
	})
}

func TestSheetsReadsCells(t *testing.T) {
	sheets, err := Sheets("book.xlsx", xlsxFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0].Name != "Data" || sheets[1].Name != "Notes" {
		t.Fatalf("sheets: %+v", sheets)
	}
	want := [][]string{
		{"Name", "Value"},
		{"Rich Text", "42", "", "7"},
		{"TRUE"},
	}
	got := sheets[0].Rows
	if len(got) != len(want) {
		t.Fatalf("rows: %q", got)
	}
	for i := range want {
		if strings.Join(got[i], "|") != strings.Join(want[i], "|") {
			t.Errorf("row %d: %q, want %q", i+1, got[i], want[i])
		}
	}
	if len(sheets[1].Rows) != 0 {
		t.Errorf("an empty sheet has rows: %q", sheets[1].Rows)
	}
}

func TestCompareSheets(t *testing.T) {
	before := []Sheet{{Name: "Data", Rows: [][]string{{"a", "b"}, {"c", "d"}}}}
	after := []Sheet{
		{Name: "Data", Rows: [][]string{{"a", "B"}, {"c", "d"}, {"e"}}},
		{Name: "New", Rows: [][]string{{"x"}}},
	}
	sheets := CompareSheets(before, after)
	if len(sheets) != 2 || sheets[0].Name != "Data" || sheets[1].Name != "New" {
		t.Fatalf("sheets: %+v", sheets)
	}
	data := sheets[0]
	if data.Changed != 2 {
		t.Errorf("%d cells changed, want 2", data.Changed)
	}
	if cell := data.Rows[0][1]; cell.Change != Modified || cell.Before != "b" || cell.After != "B" {
		t.Errorf("modified cell: %+v", cell)
	}
	if cell := data.Rows[2][0]; cell.Change != Added {
		t.Errorf("added cell: %+v", cell)
	}
	if sheets[1].Changed != 1 {
		t.Errorf("a new sheet changed %d cells, want 1", sheets[1].Changed)
	}
	// A sheet only in the old version has all its cells deleted.
	before = append(before, Sheet{Name: "Gone", Rows: [][]string{{"z"}}})
	sheets = CompareSheets(before, after)
	last := sheets[len(sheets)-1]
	if last.Name != "Gone" || last.Rows[0][0].Change != Deleted || last.Changed != 1 {
		t.Errorf("deleted sheet: %+v", last)
	}
}

func TestDocumentTextReadsParagraphsAndTables(t *testing.T) {
	data := zipOf(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t xml:space="preserve"> world</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:r><w:delText>gone</w:delText></w:r><w:r><w:t>Bye</w:t></w:r></w:p>
</w:body></w:document>`,
	})
	text, err := DocumentText("notes.docx", data)
	if err != nil {
		t.Fatal(err)
	}
	want := "Hello world\nA\tB\nBye"
	if text != want {
		t.Errorf("text:\n%q\nwant\n%q", text, want)
	}
}

func TestDocumentTextReadsSlidesInOrder(t *testing.T) {
	slide := func(text string) string {
		return `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	data := zipOf(t, map[string]string{
		"ppt/slides/slide2.xml":            slide("Second"),
		"ppt/slides/slide10.xml":           slide("Tenth"),
		"ppt/slides/_rels/slide2.xml.rels": `<Relationships/>`,
	})
	text, err := DocumentText("deck.pptx", data)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Second\n\nTenth"; text != want {
		t.Errorf("text:\n%q\nwant\n%q", text, want)
	}
}

func TestDocumentTextReadsOpenDocuments(t *testing.T) {
	odt := zipOf(t, map[string]string{
		"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"><office:body><office:text>
<text:h>Title</text:h>
<text:p>One<text:s text:c="2"/>two</text:p>
<text:p>Three<text:tab/>four</text:p>
</office:text><office:spreadsheet><table:table><table:table-row><table:table-cell><text:p>A</text:p></table:table-cell><table:table-cell><text:p>B</text:p></table:table-cell></table:table-row><table:table-row><table:table-cell><text:p>C</text:p></table:table-cell></table:table-row></table:table></office:spreadsheet></office:body></office:document-content>`,
	})
	text, err := DocumentText("text.odt", odt)
	if err != nil {
		t.Fatal(err)
	}
	want := "Title\nOne  two\nThree\tfour\nA\tB\nC"
	if text != want {
		t.Errorf("text:\n%q\nwant\n%q", text, want)
	}
}
