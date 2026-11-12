package preview

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// docxText reads the text of a Word document: a line per paragraph, a tab
// between the cells of a table, and a line per row.
func docxText(data []byte) (string, error) {
	files, err := zipFiles(data)
	if err != nil {
		return "", err
	}
	raw := zipRead(files, "word/document.xml")
	if raw == nil {
		return "", fmt.Errorf("the document has no word/document.xml")
	}
	var b strings.Builder
	inCell, skip := 0, 0
	inText := false
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "del", "instrText", "rPh":
				skip++
			case "tc":
				inCell++
			case "t":
				inText = skip == 0
			case "tab":
				if skip == 0 {
					b.WriteByte('\t')
				}
			case "br", "cr":
				if skip == 0 {
					b.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText && skip == 0 {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "del", "instrText", "rPh":
				if skip > 0 {
					skip--
				}
			case "t":
				inText = false
			case "p":
				// The paragraphs of a cell end with the cell.
				if skip == 0 && inCell == 0 {
					b.WriteByte('\n')
				}
			case "tc":
				inCell = max(inCell-1, 0)
				if skip == 0 {
					b.WriteByte('\t')
				}
			case "tr":
				if skip == 0 {
					b.WriteByte('\n')
				}
			}
		}
	}
	return tidyText(b.String()), nil
}

// pptxText reads the text of a presentation: a line per paragraph, and a
// blank line between slides, in their order.
func pptxText(data []byte) (string, error) {
	files, err := zipFiles(data)
	if err != nil {
		return "", err
	}
	var parts []string
	for name := range files {
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") && !strings.Contains(name, "/_rels/") {
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("the presentation has no slides")
	}
	sortBySlide(parts)
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		paragraphs(&b, zipRead(files, part))
	}
	return tidyText(b.String()), nil
}

// paragraphs reads the text of a slide: a line per paragraph, a tab
// between the cells of a table.
func paragraphs(b *strings.Builder, raw []byte) {
	if raw == nil {
		return
	}
	inCell := 0
	inText := false
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tc":
				inCell++
			case "t":
				inText = true
			case "br":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if inCell == 0 {
					b.WriteByte('\n')
				}
			case "tc":
				inCell = max(inCell-1, 0)
				b.WriteByte('\t')
			case "tr":
				b.WriteByte('\n')
			}
		}
	}
}

// sortBySlide orders slide parts by their numbers, slide2 before slide10.
func sortBySlide(parts []string) {
	for i := 1; i < len(parts); i++ {
		for j := i; j > 0 && naturalLess(parts[j], parts[j-1]); j-- {
			parts[j], parts[j-1] = parts[j-1], parts[j]
		}
	}
}
