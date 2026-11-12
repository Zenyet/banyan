package app

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/preview"
)

// editorWorkspace is a repository of the files, committed, in a workspace
// whose editor is ready.
func editorWorkspace(t *testing.T, files map[string]string) (*Workspace, *git.Repo) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	// Nothing in the background writes the repository as the test ends.
	run("config", "gc.auto", "0")
	run("config", "maintenance.auto", "false")
	for name, text := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "first")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := model.New(repo)
	t.Cleanup(m.Close)
	// The repository reads in the background, as a git running while the
	// temporary folder goes leaves it not empty.
	t.Cleanup(func() { settle(m) })
	ws := newWorkspace(&Shell{}, m)
	t.Cleanup(ws.editor.Close)
	settle(m)
	return ws, repo
}

// settle waits for the repository's background work to end.
func settle(m *model.Model) {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, busy := m.Activity(); !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitDiff waits for a diff pane to finish loading.
func waitDiff(t *testing.T, p *DiffPane) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); p.loading && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if p.loading {
		t.Fatal("the diff did not load")
	}
}

func TestEditorSavesOnClose(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"main.go": "package main\n"})
	ev := ws.editor
	ev.Open("main.go", 1)
	if len(ev.tabs) != 1 {
		t.Fatalf("%d tabs", len(ev.tabs))
	}
	tab := ev.tabs[0]
	if tab.unsaved() {
		t.Fatal("a file just opened is unsaved")
	}
	tab.state.Buffer.Set("package main\n\nfunc main() {}\n")
	if !tab.unsaved() {
		t.Fatal("an edit is not unsaved")
	}
	if !ev.CloseCurrent() {
		t.Fatal("no tab to close")
	}
	if len(ev.tabs) != 0 {
		t.Fatalf("%d tabs left", len(ev.tabs))
	}
	if ev.shown {
		t.Error("the editor shows with no files open")
	}
	data, err := os.ReadFile(filepath.Join(repo.Root(), "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "package main\n\nfunc main() {}\n"; string(data) != want {
		t.Errorf("saved:\n%q\nwant\n%q", data, want)
	}
}

func TestEditorSavesAllOnLeaving(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"a.go": "a\n", "b.go": "b\n"})
	ev := ws.editor
	ev.Open("a.go", 0)
	ev.Open("b.go", 0)
	for _, tab := range ev.tabs {
		tab.state.Buffer.Set(tab.path + " edited\n")
	}
	ws.setTab(tabCommit)
	for _, name := range []string{"a.go", "b.go"} {
		data, err := os.ReadFile(filepath.Join(repo.Root(), name))
		if err != nil {
			t.Fatal(err)
		}
		if want := name + " edited\n"; string(data) != want {
			t.Errorf("%s saved as %q, want %q", name, data, want)
		}
	}
}

func TestEditorReloadsFileChangedOnDisk(t *testing.T) {
	ws, _ := editorWorkspace(t, map[string]string{"main.go": "one\n"})
	ev := ws.editor
	ev.Open("main.go", 0)
	tab := ev.tabs[0]
	if err := os.WriteFile(tab.full, []byte("two, longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev.modelChanged(model.Event{Kind: model.Changed})
	if got := tab.state.Buffer.String(); got != "two, longer\n" {
		t.Fatalf("reloaded as %q", got)
	}
	if tab.unsaved() {
		t.Fatal("a reloaded file is unsaved")
	}
}

func TestEditorAsksBeforeReplacingEdits(t *testing.T) {
	ws, _ := editorWorkspace(t, map[string]string{"main.go": "one\n"})
	ev := ws.editor
	ev.Open("main.go", 0)
	tab := ev.tabs[0]
	tab.state.Buffer.Set("mine\n")
	if err := os.WriteFile(tab.full, []byte("theirs, longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev.modelChanged(model.Event{Kind: model.Changed})
	if !tab.conflict {
		t.Fatal("no conflict asked about")
	}
	if got := tab.state.Buffer.String(); got != "mine\n" {
		t.Fatalf("edits replaced by %q", got)
	}
	// Keeping the edits leaves them alone, and does not ask again.
	tab.ignored, tab.conflict = tab.diskText, false
	ev.modelChanged(model.Event{Kind: model.Changed})
	if tab.conflict {
		t.Fatal("asked again about the version kept over")
	}
	if got := tab.state.Buffer.String(); got != "mine\n" {
		t.Fatalf("edits replaced by %q", got)
	}
	// Loading the disk version takes it.
	tab.reload("theirs, longer\n")
	if got := tab.state.Buffer.String(); got != "theirs, longer\n" {
		t.Fatalf("disk version did not load: %q", got)
	}
}

// binaryPatch is the patch git makes of a binary file that changed.
const binaryPatch = "diff --git a/file b/file\nindex 1111111..2222222 100644\nBinary files a/file and b/file differ\n"

func TestDiffPanePreviewsBinaryFileAsHex(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"notes.txt": "hi\n"})
	if err := os.WriteFile(filepath.Join(repo.Root(), "blob.bin"), []byte{0, 1, 2, 3, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newDiffPane(ws)
	p.Show(DiffSource{
		Title: "blob.bin",
		Path:  "blob.bin",
		Patch: func() (string, error) { return binaryPatch, nil },
		Old:   func() ([]byte, error) { return []byte{0, 1, 2, 2, 0}, nil },
		New:   func() ([]byte, error) { return []byte{0, 1, 2, 3, 0}, nil },
	})
	waitDiff(t, p)
	if p.binary == nil {
		t.Fatal("no preview")
	}
	if p.binary.kind != preview.Hex {
		t.Fatalf("preview kind %v", p.binary.kind)
	}
	if p.state == nil {
		t.Fatal("no dump diff")
	}
	if p.editable {
		t.Error("a binary file is editable")
	}
	if !strings.Contains(p.state.Old.String(), "00000000  00 01 02 02 00") {
		t.Errorf("old dump:\n%s", p.state.Old.String())
	}
	if !strings.Contains(p.state.New.String(), "00000000  00 01 02 03 00") {
		t.Errorf("new dump:\n%s", p.state.New.String())
	}
	if added, deleted := p.state.Stats(); added+deleted != 2 {
		t.Errorf("the dump diff has %d added and %d deleted lines", added, deleted)
	}
}

// xlsxOf is a workbook of one sheet, its A1 cell the text.
func xlsxOf(t *testing.T, text string) []byte {
	t.Helper()
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>` + text + `</t></is></c></row></sheetData></worksheet>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDiffPanePreviewsSpreadsheetAsCells(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"book.xlsx": "old"})
	before, after := xlsxOf(t, "old"), xlsxOf(t, "new")
	if err := os.WriteFile(filepath.Join(repo.Root(), "book.xlsx"), after, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newDiffPane(ws)
	p.Show(DiffSource{
		Title: "book.xlsx",
		Path:  "book.xlsx",
		Patch: func() (string, error) { return binaryPatch, nil },
		Old:   func() ([]byte, error) { return before, nil },
		New:   func() ([]byte, error) { return after, nil },
	})
	waitDiff(t, p)
	if p.binary == nil || p.binary.kind != preview.Spreadsheet {
		t.Fatalf("preview: %+v", p.binary)
	}
	if len(p.binary.sheets) != 1 || p.binary.sheets[0].Changed != 1 {
		t.Fatalf("sheets: %+v", p.binary.sheets)
	}
	if p.state != nil {
		t.Error("a spreadsheet shows as a diff")
	}
	if p.editable {
		t.Error("a spreadsheet is editable")
	}
	tt := ui.NewTester(p.View, 800, 400)
	tt.Frame()
	if !tt.HasText("Data") {
		t.Error("no sheet tab:", tt.Texts())
	}
	if !tt.HasText("1 cells changed") {
		t.Error("no changed cell count:", tt.Texts())
	}
	if !tt.HasText("new") {
		t.Error("the new version's cell is not shown:", tt.Texts())
	}
	// The switch shows the old version's cells instead.
	if err := tt.Click("Before"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.binary.after {
		t.Error("the switch did not show the old version")
	}
	if !tt.HasText("old") || tt.HasText("new") {
		t.Errorf("the old version is not shown: %q", tt.Texts())
	}
}

func TestDiffPaneMarksTextFileEditable(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"main.go": "one\n"})
	if err := os.WriteFile(filepath.Join(repo.Root(), "main.go"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := newDiffPane(ws)
	p.Show(DiffSource{
		Title: "main.go",
		Path:  "main.go",
		Patch: func() (string, error) { return "@@ -1 +1 @@\n-one\n+two\n", nil },
		Old:   func() ([]byte, error) { return []byte("one\n"), nil },
		New:   func() ([]byte, error) { return []byte("two\n"), nil },
	})
	waitDiff(t, p)
	if !p.editable {
		t.Fatal("a text file in the work tree is not editable")
	}
	if p.editLine != 1 {
		t.Errorf("edit line %d", p.editLine)
	}
	tt := ui.NewTester(p.View, 800, 400)
	tt.Frame()
	if err := tt.Click("Edit File"); err != nil {
		t.Fatal(err)
	}
	if len(ws.editor.tabs) != 1 || ws.editor.tabs[0].path != "main.go" {
		t.Fatalf("the button did not open the file: %+v", ws.editor.tabs)
	}
	if !ws.editor.Docked() {
		t.Error("the editor did not show beside the view")
	}
	// A file that is not in the work tree, as one deleted, has no button.
	if err := os.Remove(filepath.Join(repo.Root(), "main.go")); err != nil {
		t.Fatal(err)
	}
	p.checkEditable()
	if p.editable {
		t.Error("a file not in the work tree is editable")
	}
}

// pngOf is a picture of width by height pixels, all of the colour.
func pngOf(t *testing.T, width, height int, fill color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDiffPanePreviewsImageSideBySide(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"logo.png": "old"})
	before := pngOf(t, 3, 2, color.RGBA{R: 255, A: 255})
	after := pngOf(t, 3, 2, color.RGBA{B: 255, A: 255})
	if err := os.WriteFile(filepath.Join(repo.Root(), "logo.png"), after, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newDiffPane(ws)
	p.Show(DiffSource{
		Title: "logo.png",
		Path:  "logo.png",
		Patch: func() (string, error) { return binaryPatch, nil },
		Old:   func() ([]byte, error) { return before, nil },
		New:   func() ([]byte, error) { return after, nil },
	})
	waitDiff(t, p)
	if p.binary == nil || p.binary.kind != preview.Image {
		t.Fatalf("preview: %+v", p.binary)
	}
	if p.binary.beforePic == nil || p.binary.afterPic == nil {
		t.Fatal("a side's picture did not decode")
	}
	if p.state != nil {
		t.Error("a picture shows as a diff")
	}
	if p.editable {
		t.Error("a picture is editable")
	}
	tt := ui.NewTester(p.View, 900, 400)
	tt.Frame()
	for _, want := range []string{"Before", "After", "3 × 2"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestDiffPanePreviewsDocumentAsText(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"notes.docx": "old"})
	doc := func(text string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("word/document.xml")
		if err != nil {
			t.Fatal(err)
		}
		content := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	before, after := doc("Hello"), doc("Hello there")
	if err := os.WriteFile(filepath.Join(repo.Root(), "notes.docx"), after, 0o644); err != nil {
		t.Fatal(err)
	}
	p := newDiffPane(ws)
	p.Show(DiffSource{
		Title: "notes.docx",
		Path:  "notes.docx",
		Patch: func() (string, error) { return binaryPatch, nil },
		Old:   func() ([]byte, error) { return before, nil },
		New:   func() ([]byte, error) { return after, nil },
	})
	waitDiff(t, p)
	if p.binary == nil || p.binary.kind != preview.Document {
		t.Fatalf("preview: %+v", p.binary)
	}
	if p.state == nil {
		t.Fatal("no text diff")
	}
	if got := p.state.Old.String(); got != "Hello" {
		t.Errorf("old text %q", got)
	}
	if got := p.state.New.String(); got != "Hello there" {
		t.Errorf("new text %q", got)
	}
	if p.editable {
		t.Error("a document is editable")
	}
}

// waitFor waits for a condition the background sets.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if !ok() {
		t.Fatal("the background did not get there")
	}
}

func TestEditorViewShowsTabsAndChanges(t *testing.T) {
	ws, _ := editorWorkspace(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	ev := ws.editor
	ev.Open("main.go", 2)
	if !ev.Docked() {
		t.Error("the editor did not dock")
	}
	tab := ev.tabs[0]
	waitFor(t, func() bool { return tab.headRev != "" })
	if tab.state.DiffBase == nil {
		t.Error("no changes against HEAD to mark")
	}
	tt := ui.NewTester(ev.View, 900, 600)
	tt.Frame()
	if !tt.HasText("main.go") {
		t.Fatalf("no tab shown: %q", tt.Texts())
	}
	if err := tt.Click("Changes since HEAD"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tab.showDiff {
		t.Fatal("the changes since HEAD did not show")
	}
	if hunk := tab.changesSinceHead().Hunks(); len(hunk) != 0 {
		t.Errorf("an unchanged file has %d hunks", len(hunk))
	}
	tab.state.Buffer.Set("package main\n\nfunc main() { println() }\n")
	if hunks := tab.changesSinceHead().Hunks(); len(hunks) != 1 {
		t.Errorf("an edited file has %d hunks", len(hunks))
	}
	tt.Frame()
	if err := tt.Click("Close Tab"); err != nil {
		t.Fatal(err)
	}
	if len(ev.tabs) != 0 {
		t.Fatalf("%d tabs left", len(ev.tabs))
	}
	if ev.shown {
		t.Error("the editor shows with no files open")
	}
	if ws.tab != tabLog {
		t.Errorf("closing the last tab changed the view to %d", ws.tab)
	}
}

func TestEditorHidesAndComesBack(t *testing.T) {
	ws, _ := editorWorkspace(t, map[string]string{"main.go": "one\n"})
	ev := ws.editor
	ev.Open("main.go", 1)
	if !ev.Docked() {
		t.Fatal("the editor did not dock")
	}
	ev.Hide()
	if ev.shown {
		t.Error("the editor shows hidden")
	}
	if len(ev.tabs) != 1 {
		t.Errorf("hiding closed %d files", 1-len(ev.tabs))
	}
	ev.Toggle()
	if !ev.Docked() {
		t.Error("the editor did not come back")
	}
	// Opening another file shows it again, at that file.
	ev.Hide()
	ev.Open("main.go", 2)
	if !ev.Docked() {
		t.Error("opening a file did not show the editor")
	}
	if got := ev.current().line; got != 2 {
		t.Errorf("the file opens at line %d", got)
	}
}

func TestWorkspaceShowsEditorBesideTheView(t *testing.T) {
	ws, _ := editorWorkspace(t, map[string]string{"main.go": "package main\n"})
	ws.log.rebuildBranches()
	ws.editor.Open("main.go", 1)

	tt := ui.NewTester(ws.View, 1200, 700)
	tt.Frame()
	// Both the view and the editor show, and the view still works.
	if !tt.HasText("main.go") {
		t.Errorf("no editor tab: %q", tt.Texts())
	}
	if !tt.HasText("Branches") {
		t.Errorf("the view beside the editor is gone: %q", tt.Texts())
	}
	// The editor stays beside the views as they change.
	ws.setTab(tabCommit)
	tt.Frame()
	if !tt.HasText("Local Changes") {
		t.Errorf("the Commit view did not show: %q", tt.Texts())
	}
	if !tt.HasText("main.go") {
		t.Errorf("the editor went with the view: %q", tt.Texts())
	}
	// Hiding it leaves the view the whole width.
	ws.editor.Hide()
	tt.Frame()
	if tt.HasText("Changes since HEAD") {
		t.Errorf("the editor still shows: %q", tt.Texts())
	}
	if !tt.HasText("Local Changes") {
		t.Errorf("the view went with the editor: %q", tt.Texts())
	}
}

func TestToolbarTogglesTheEditor(t *testing.T) {
	ws, _ := editorWorkspace(t, map[string]string{"main.go": "package main\n"})
	ws.editor.Open("main.go", 1)
	tt := ui.NewTester(ws.header, 1400, 60)
	tt.Frame()
	if !tt.HasText("Editor (1)") {
		t.Fatalf("no editor button: %q", tt.Texts())
	}
	if err := tt.Click("Editor (1)"); err != nil {
		t.Fatal(err)
	}
	if ws.editor.shown {
		t.Error("the button did not hide the editor")
	}
	tt.Frame()
	if err := tt.Click("Editor (1)"); err != nil {
		t.Fatal(err)
	}
	if !ws.editor.Docked() {
		t.Error("the button did not show the editor")
	}
}

func TestEditorSavesWhenTypingRests(t *testing.T) {
	ws, repo := editorWorkspace(t, map[string]string{"main.go": "one\n"})
	ev := ws.editor
	ev.Open("main.go", 1)
	tt := ui.NewTester(ev.View, 800, 600)
	tt.Frame()
	ev.tabs[0].state.Buffer.Set("one, edited\n")
	tt.Frame()
	// Typing rests: the editor saves it on its own.
	full := filepath.Join(repo.Root(), "main.go")
	var data []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		data, _ = os.ReadFile(full)
		if string(data) == "one, edited\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(data) != "one, edited\n" {
		t.Fatalf("not saved: %q", data)
	}
	if ev.tabs[0].unsaved() {
		t.Error("saved but still unsaved")
	}
}
