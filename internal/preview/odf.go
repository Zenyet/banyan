package preview

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// odfText reads the text of an OpenDocument file: a line per paragraph, a
// tab between the cells of a table and a line per row, and a blank line
// between slides.
func odfText(data []byte) (string, error) {
	files, err := zipFiles(data)
	if err != nil {
		return "", err
	}
	raw := zipRead(files, "content.xml")
	if raw == nil {
		return "", fmt.Errorf("the document has no content.xml")
	}
	var b strings.Builder
	inCell, inParagraph, skip := 0, 0, 0
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
			case "annotation", "note":
				skip++
			case "table-cell":
				inCell++
			case "p", "h":
				inParagraph++
			case "tab":
				if skip == 0 {
					b.WriteByte('\t')
				}
			case "line-break":
				if skip == 0 {
					b.WriteByte('\n')
				}
			case "s":
				if skip == 0 {
					n := 1
					for _, a := range t.Attr {
						if a.Name.Local == "c" {
							if v, err := strconv.Atoi(a.Value); err == nil && v > 0 {
								n = v
							}
						}
					}
					b.WriteString(strings.Repeat(" ", n))
				}
			}
		case xml.CharData:
			if skip == 0 && inParagraph > 0 {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "annotation", "note":
				if skip > 0 {
					skip--
				}
			case "p", "h":
				inParagraph = max(inParagraph-1, 0)
				if skip == 0 && inCell == 0 {
					b.WriteByte('\n')
				}
			case "table-cell":
				inCell = max(inCell-1, 0)
				if skip == 0 {
					b.WriteByte('\t')
				}
			case "table-row":
				if skip == 0 {
					b.WriteByte('\n')
				}
			case "table", "page":
				if skip == 0 {
					b.WriteByte('\n')
				}
			}
		}
	}
	return tidyText(b.String()), nil
}
