package app

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/preview"
)

// binaryPreview shows a binary file's two versions: pictures side by side,
// a spreadsheet's cells, or a document's text or a dump's bytes as a diff.
type binaryPreview struct {
	path string
	kind preview.Kind
	// old and new are the versions, nil where the file was not there.
	old, new []byte
	// beforePic and afterPic are the pictures of an image, decoded on the
	// main thread by prepare.
	beforePic, afterPic ui.ImageSource
	decoded             bool
	// sheets are the spreadsheet's sheets compared, and sheet and after
	// say which shows.
	sheets []preview.SheetDiff
	sheet  int
	after  bool
	cells  ui.ListState
	// diff is the text compared, of a document or a dump.
	diff *editor.DiffState
	// note says what the preview leaves out, as a dump cut short.
	note string
}

// newBinaryPreview parses the versions of a binary file. It runs off the
// main thread; pictures are decoded later, on it.
func newBinaryPreview(path string, old, new []byte) *binaryPreview {
	b := &binaryPreview{path: path, kind: preview.KindOf(path), old: old, new: new, after: true}
	switch b.kind {
	case preview.Image:
		// Decoded on the main thread, by prepare.
	case preview.Spreadsheet:
		before, _ := preview.Sheets(path, old)
		after, _ := preview.Sheets(path, new)
		b.sheets = preview.CompareSheets(before, after)
		if len(b.sheets) == 0 {
			b.hexPreview()
		}
	case preview.Document:
		oldText, oldErr := preview.DocumentText(path, old)
		newText, newErr := preview.DocumentText(path, new)
		if oldErr != nil && newErr != nil {
			b.hexPreview()
			break
		}
		b.diff = textDiff(oldText, newText)
	default:
		b.hexPreview()
	}
	return b
}

// hexPreview shows the versions as hex dumps of their first bytes.
func (b *binaryPreview) hexPreview() {
	b.kind = preview.Hex
	oldText, oldCut := preview.HexDump(b.old)
	newText, newCut := preview.HexDump(b.new)
	b.diff = textDiff(oldText, newText)
	if oldCut || newCut {
		b.note = i18n.Tf("Only the first {} KB of each version is shown.", preview.HexLimit/1024)
	}
}

// textDiff compares two texts, as a document's or a dump's.
func textDiff(oldText, newText string) *editor.DiffState {
	return &editor.DiffState{
		Old:             editor.NewBuffer(oldText),
		New:             editor.NewBuffer(newText),
		RenderSeparator: separator,
	}
}

// prepare decodes an image's pictures, on the main thread; failing, it
// shows the versions as a hex dump instead.
func (b *binaryPreview) prepare() {
	if b.decoded {
		return
	}
	b.decoded = true
	if b.kind != preview.Image {
		return
	}
	if len(b.new) > 0 {
		if img, err := ui.DecodeImage(b.new); err == nil {
			b.afterPic = img
		}
	}
	if len(b.old) > 0 {
		if img, err := ui.DecodeImage(b.old); err == nil {
			b.beforePic = img
		}
	}
	if b.beforePic == nil && b.afterPic == nil {
		b.hexPreview()
	}
}

// View shows the preview of a picture or a spreadsheet; a document or a
// dump shows in the pane's diff viewer, which diffState gives.
func (b *binaryPreview) View(c *ui.Context) {
	b.prepare()
	switch b.kind {
	case preview.Image:
		b.imageView(c)
	case preview.Spreadsheet:
		b.sheetView(c)
	}
}

// diffState is the text the preview compares, for a document or a dump.
func (b *binaryPreview) diffState() *editor.DiffState { return b.diff }

// imageView shows the two versions of a picture side by side.
func (b *binaryPreview) imageView(c *ui.Context) {
	t := c.Theme()
	ui.Row(c.Key("images")).Grow(1).Gap(1).Background(t.Border).Children(func() {
		b.imageSide(c, i18n.T("Before"), b.old, b.beforePic)
		b.imageSide(c, i18n.T("After"), b.new, b.afterPic)
	})
}

func (b *binaryPreview) imageSide(c *ui.Context, label string, data []byte, img ui.ImageSource) {
	t := c.Theme()
	ui.Column(c.Key(label)).Grow(1).Basis(0).MinWidth(0).FillHeight().Background(t.Background).Children(func() {
		ui.Row(c.Key("caption")).Height(28).PaddingX(10).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, label).FontWeight(600).SingleLine()
			if img == nil {
				muted(c, i18n.T("Not in this version"))
			} else {
				w, h := ui.ImageSize(img)
				muted(c, fmt.Sprintf("%d × %d", int(w), int(h))).SingleLine()
			}
			ui.Spacer(c)
			muted(c, formatBytes(len(data))).SingleLine()
		})
		if img == nil {
			emptyMessage(c, i18n.T("No picture"))
			return
		}
		ui.Box(c.Key("picture")).Grow(1).MinHeight(0).Padding(12).Children(func() {
			ui.Image(c, img).Fill().Fit(ui.Contain)
		})
	})
}

// sheetView shows the cells of a spreadsheet's sheet, the changed ones
// tinted, with a tab per sheet and a switch between the versions.
func (b *binaryPreview) sheetView(c *ui.Context) {
	t := c.Theme()
	if b.sheet >= len(b.sheets) {
		b.sheet = 0
	}
	if len(b.sheets) == 0 {
		emptyMessage(c, i18n.T("The spreadsheet is empty"))
		return
	}
	s := &b.sheets[b.sheet]
	ui.Column(c.Key("spreadsheet")).Fill().Children(func() {
		labels := make([]string, len(b.sheets))
		for i := range b.sheets {
			labels[i] = b.sheets[i].Name
		}
		ui.Row(c.Key("sheet-bar")).Height(36).PaddingX(8).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).ClipX().Children(func() {
			ui.Tabs(c.Key("sheets"), &b.sheet, labels...).Shrink(1).MinWidth(0)
			ui.Spacer(c)
			segments(c.Key("version"), func() {
				if segment(c.Key("before"), i18n.T("Before"), !b.after).Clicked() {
					b.after = false
				}
				if segment(c.Key("after"), i18n.T("After"), b.after).Clicked() {
					b.after = true
				}
			})
			muted(c, i18n.Tf("{} cells changed", s.Changed)).SingleLine().Shrink(0)
		})
		columns := make([]ui.TableColumn, s.Columns+1)
		columns[0] = ui.TableColumn{Width: 44, Fixed: true, RowHeader: true, Align: ui.End}
		for i := 0; i < s.Columns; i++ {
			columns[i+1] = ui.TableColumn{Title: columnName(i), ID: columnName(i), Width: 110, MinWidth: 40, MaxWidth: 420}
		}
		ui.Table(c.Key("cells"), &b.cells, columns, len(s.Rows), func(row, col int) {
			if row >= len(s.Rows) {
				return
			}
			if col == 0 {
				muted(c, strconv.Itoa(row+1))
				return
			}
			col--
			line := s.Rows[row]
			if col >= len(line) {
				return
			}
			b.cellView(c, line[col])
		}).Grow(1).MinHeight(0)
	})
}

// cellView is a cell of a sheet, tinted by how it changed, with what it
// reads in the other version as its tooltip.
func (b *binaryPreview) cellView(c *ui.Context, cell preview.CellDiff) {
	t := c.Theme()
	value, other := cell.After, cell.Before
	if !b.after {
		value, other = cell.Before, cell.After
	}
	if value == "" {
		return
	}
	var tint ui.Color
	switch {
	case cell.Change == preview.Modified:
		tint = t.Warning.Alpha(0.18)
	case cell.Change == preview.Added && b.after:
		tint = t.Success.Alpha(0.18)
	case cell.Change == preview.Deleted && !b.after:
		tint = t.Danger.Alpha(0.18)
	}
	box := ui.Row(c).Fill().PaddingX(6).Background(tint)
	if cell.Change != preview.Unchanged {
		if other == "" {
			other = i18n.T("empty")
		}
		if b.after {
			box.Tooltip(i18n.Tf("Before: {}", other))
		} else {
			box.Tooltip(i18n.Tf("After: {}", other))
		}
	}
	box.Children(func() {
		ui.Text(c, value).SingleLine().Shrink(1).MinWidth(0)
	})
}

// columnName is a spreadsheet column's name: A, B, …, Z, AA, AB, ….
func columnName(ix int) string {
	name := ""
	for ix++; ix > 0; ix = (ix - 1) / 26 {
		name = string(rune('A'+(ix-1)%26)) + name
	}
	return name
}

// formatBytes is a file's size as people read it.
func formatBytes(n int) string {
	switch {
	case n < 1024:
		return i18n.Tf("{} bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
