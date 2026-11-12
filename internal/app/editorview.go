package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// idleSave is how long typing rests before the editor saves.
const idleSave = 1500 * time.Millisecond

// The docked editor opens editorDefaultWidth wide, and leaves the view
// beside it editorMinWidth to drag it down to.
const (
	editorDefaultWidth = 520
	editorMinWidth     = 300
)

// EditorView is the files opened from diffs, each in a tab, edited with
// their changes against HEAD marked in the gutter: docked beside the view
// they were opened from, where it stays as the views change, or in a
// window of its own.
type EditorView struct {
	ws     *Workspace
	tabs   []*EditorTab
	active int
	// refs is the version of the repository's refs the tabs' HEAD texts
	// are from.
	refs uint64
	// shown says the editor shows, docked or in a window of its own, which
	// inWindow says, and window is while that window is open. split is the
	// room the view beside it takes, in DIPs.
	shown    bool
	inWindow bool
	window   *Shell
	split    float32
	// focused says the editor had the keyboard at the last frame, docked,
	// so that the shortcuts reach it before the views.
	focused bool
	// saveGen numbers the idle saves waiting, of which only the last one
	// saves.
	saveGen int
}

// EditorTab is a file open in the EditorView.
type EditorTab struct {
	view *EditorView
	// path is the file's path in the repository, with slashes, and full
	// where it is on disk.
	path  string
	full  string
	state editor.State
	// head is the file's text at HEAD, which headRev is the commit of.
	head    string
	headRev string
	// diff is the tab's changes since HEAD, made when shown, and showDiff
	// says they show in place of the text.
	diff     *editor.DiffState
	showDiff bool
	// mtime and size are of the file on disk as last read or written.
	mtime time.Time
	size  int64
	// diskText is a version on disk that differs from an edited buffer and
	// asks to be loaded; ignored is the one the user kept theirs over.
	diskText string
	ignored  string
	conflict bool
	// line is a line to reveal once the tab shows, from 1.
	line int
	// focus asks for the keyboard once the tab shows.
	focus bool
	// seen is the version of the buffer at the last frame, whose edits
	// start the idle save.
	seen uint64
}

func newEditorView(ws *Workspace) *EditorView {
	return &EditorView{ws: ws, refs: ws.Model.RefsVersion}
}

func (ev *EditorView) current() *EditorTab {
	if ev.active < 0 || ev.active >= len(ev.tabs) {
		return nil
	}
	return ev.tabs[ev.active]
}

// Docked reports whether the editor shows beside the view, rather than
// hidden or in a window of its own.
func (ev *EditorView) Docked() bool {
	return ev.shown && !ev.inWindow && len(ev.tabs) > 0
}

// Open shows the file at path, a path in the repository, beside the view
// at line from 1 (0 for the top), in a tab of its own or in the one it
// has.
func (ev *EditorView) Open(path string, line int) {
	ws := ev.ws
	if ix := slices.IndexFunc(ev.tabs, func(t *EditorTab) bool { return t.path == path }); ix >= 0 {
		ev.activate(ix)
		if line > 0 {
			ev.tabs[ix].line = line
			ev.tabs[ix].showDiff = false
		}
		ev.reveal()
		return
	}
	full := filepath.Join(ws.Model.Repo.Root(), filepath.FromSlash(path))
	info, err := os.Stat(full)
	if err != nil {
		ws.toast("error", err.Error())
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		ws.toast("error", err.Error())
		return
	}
	t := &EditorTab{view: ev, path: path, full: full, line: line, focus: true}
	t.state.Buffer = editor.NewBuffer(string(data))
	t.state.Buffer.SetLanguage(editor.LanguageFor(path))
	t.state.LineNumbers = editor.LineNumbersOn
	t.seen = t.state.Buffer.Version()
	t.mtime, t.size = info.ModTime(), info.Size()
	ev.tabs = append(ev.tabs, t)
	ev.active = len(ev.tabs) - 1
	ev.reveal()
	ev.loadHead(t)
}

// activate shows the tab at ix.
func (ev *EditorView) activate(ix int) {
	if ix < 0 || ix >= len(ev.tabs) {
		return
	}
	ev.active = ix
}

// reveal shows the editor where it belongs: its window, or the side of the
// view.
func (ev *EditorView) reveal() {
	ev.shown = true
	if ev.inWindow {
		ev.openWindow()
	}
	ev.redraw()
}

// Toggle hides the editor, keeping its files open, or shows it again where
// it was.
func (ev *EditorView) Toggle() {
	if ev.shown {
		ev.Hide()
		return
	}
	ev.shown = true
	if ev.inWindow {
		ev.openWindow()
	}
	ev.redraw()
}

// Hide saves what the editor has open and hides it, its files open still.
func (ev *EditorView) Hide() {
	ev.SaveAll()
	ev.shown, ev.focused = false, false
	ev.closeWindow()
	ev.redraw()
}

// Detach moves the editor to a window of its own, leaving the view beside
// it the whole width.
func (ev *EditorView) Detach() {
	ev.shown, ev.inWindow = true, true
	ev.focused = false
	ev.openWindow()
	ev.redraw()
}

// Dock moves the editor back beside the view from its window.
func (ev *EditorView) Dock() {
	ev.inWindow, ev.shown = false, true
	ev.closeWindow()
	ev.redraw()
}

func (ev *EditorView) openWindow() {
	if ev.window != nil {
		ev.window.Win.Show()
		ev.window.Win.Focus()
		return
	}
	s := newWindow(mygo.WindowOptions{
		Title:     i18n.Tf("Editor – {}", ev.ws.Model.Repo.Name()),
		Width:     960,
		Height:    720,
		MinWidth:  420,
		MinHeight: 300,
	}, func(_ *Shell, c *ui.Context) { ev.panel(c) })
	s.onClosed = func() {
		// Closed from its title bar, the window hides the editor, which
		// Toggle shows again there; a window closed to dock it is no
		// longer ev's.
		if ev.window != s {
			return
		}
		ev.window = nil
		ev.shown = false
		ev.redraw()
	}
	ev.window = s
	if t := ev.current(); t != nil {
		t.focus = true
	}
	s.Win.Show()
	s.Win.Focus()
}

func (ev *EditorView) closeWindow() {
	if w := ev.window; w != nil {
		ev.window = nil
		w.Win.Close()
	}
}

// redraw shows what the editor changed in its window and in the view's.
func (ev *EditorView) redraw() {
	if ev.window != nil {
		ev.window.Win.Invalidate()
	}
	if ev.ws != nil && ev.ws.Shell != nil && ev.ws.Shell.Win != nil {
		ev.ws.Shell.Win.Invalidate()
	}
}

// Close saves what the editor has open, as closing the repository does,
// and closes its window.
func (ev *EditorView) Close() {
	ev.SaveAll()
	// The idle saves waiting find nothing to save.
	ev.saveGen++
	ev.closeWindow()
}

// find opens the find bar of the file shown, or of its changes, reporting
// whether a file is open to find in.
func (ev *EditorView) find() bool {
	t := ev.current()
	if t == nil {
		return false
	}
	if t.showDiff {
		t.changesSinceHead().Find("")
	} else {
		t.state.Find("")
	}
	ev.redraw()
	return true
}

// SaveCurrent saves the text of the tab shown, if it changed.
func (ev *EditorView) SaveCurrent() {
	if t := ev.current(); t != nil {
		t.save()
	}
}

// SaveAll saves the text of every tab that changed, as leaving the editor
// does.
func (ev *EditorView) SaveAll() {
	for _, t := range ev.tabs {
		t.save()
	}
}

// CloseCurrent saves and closes the tab shown, reporting whether there was
// one.
func (ev *EditorView) CloseCurrent() bool {
	if ev.current() == nil {
		return false
	}
	ev.closeTab(ev.active)
	return true
}

// closeTab saves and closes the tab at ix, hiding the editor once none is
// left.
func (ev *EditorView) closeTab(ix int) {
	if ix < 0 || ix >= len(ev.tabs) {
		return
	}
	ev.tabs[ix].save()
	ev.tabs = slices.Delete(ev.tabs, ix, ix+1)
	if len(ev.tabs) == 0 {
		ev.active = 0
		ev.shown, ev.focused = false, false
		ev.closeWindow()
		ev.redraw()
		return
	}
	switch {
	case ix < ev.active:
		ev.active--
	case ev.active >= len(ev.tabs):
		ev.active = len(ev.tabs) - 1
	}
}

// closeOthers saves and closes every tab but the one at keep.
func (ev *EditorView) closeOthers(keep int) {
	if keep < 0 || keep >= len(ev.tabs) {
		return
	}
	kept := ev.tabs[keep]
	for _, t := range ev.tabs {
		if t != kept {
			t.save()
		}
	}
	ev.tabs = []*EditorTab{kept}
	ev.active = 0
}

// closeAll saves and closes every tab, hiding the editor.
func (ev *EditorView) closeAll() {
	ev.SaveAll()
	ev.tabs = nil
	ev.active = 0
	ev.shown, ev.focused = false, false
	ev.closeWindow()
}

// modelChanged follows the repository: files changed on disk reload, and
// the marks against HEAD follow it as it moves.
func (ev *EditorView) modelChanged(e model.Event) {
	if e.Kind != model.Changed {
		return
	}
	for _, t := range ev.tabs {
		t.checkDisk()
	}
	if ev.ws.Model.RefsVersion != ev.refs {
		ev.refs = ev.ws.Model.RefsVersion
		for _, t := range ev.tabs {
			ev.loadHead(t)
		}
	}
}

// loadHead reads a file's text at HEAD in the background, to mark the
// changes of it in the gutter and show them as a diff.
func (ev *EditorView) loadHead(t *EditorTab) {
	repo := ev.ws.Model.Repo
	go func() {
		rev, ok := repo.Head()
		if !ok {
			return
		}
		data, err := repo.Blob(rev, t.path)
		if err != nil {
			return
		}
		model.Post(func() {
			if !slices.Contains(ev.tabs, t) || rev == t.headRev {
				return
			}
			t.head, t.headRev = string(data), rev
			// Every line of a file HEAD does not have is a change.
			t.state.DiffBase = editor.NewBuffer(t.head)
			t.diff = nil
		})
	}()
}

// watchIdle saves the tab once typing has rested: each edit starts a wait,
// and only the last of them saves.
func (ev *EditorView) watchIdle(t *EditorTab) {
	buf := t.state.Buffer
	if buf == nil {
		return
	}
	if v := buf.Version(); v != t.seen {
		t.seen = v
	} else {
		return
	}
	ev.saveGen++
	gen := ev.saveGen
	go func() {
		time.Sleep(idleSave)
		model.Post(func() {
			if gen != ev.saveGen || !slices.Contains(ev.tabs, t) {
				return
			}
			t.save()
			ev.redraw()
		})
	}()
}

// save writes the tab's text to its file when it changed, marks it saved,
// and tells the repository, so that the Commit view shows the new diff at
// once.
func (t *EditorTab) save() {
	buf := t.state.Buffer
	if buf == nil || !buf.Modified() {
		return
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(t.full); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.OpenFile(t.full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err == nil {
		// The text writes with the line endings the file had.
		_, err = buf.WriteTo(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		t.view.ws.toast("error", i18n.Tf("Could not save: {}", t.path+" — "+err.Error()))
		return
	}
	buf.MarkSaved()
	t.diskText, t.ignored, t.conflict = "", "", false
	if info, err := os.Stat(t.full); err == nil {
		t.mtime, t.size = info.ModTime(), info.Size()
	}
	t.view.ws.Model.RefreshStatus()
}

// checkDisk reloads a file changed on disk when the buffer has no edits,
// and asks what to do when it has.
func (t *EditorTab) checkDisk() {
	info, err := os.Stat(t.full)
	if err != nil {
		return
	}
	if info.Size() == t.size && info.ModTime().Equal(t.mtime) {
		return
	}
	data, err := os.ReadFile(t.full)
	if err != nil {
		return
	}
	text := string(data)
	t.mtime, t.size = info.ModTime(), info.Size()
	if text == t.ignored {
		return
	}
	buf := t.state.Buffer
	if buf == nil {
		return
	}
	if sameText(text, buf.String()) {
		// Our own save, or a copy of what the buffer holds.
		buf.MarkSaved()
		t.conflict, t.diskText = false, ""
		return
	}
	if !buf.Modified() {
		t.reload(text)
		return
	}
	t.diskText, t.conflict = text, true
}

// reload takes the version on disk in place of the buffer's, forgetting
// its undo history, as a file reloaded anew.
func (t *EditorTab) reload(text string) {
	if t.state.Buffer == nil {
		t.state.Buffer = editor.NewBuffer(text)
	} else {
		t.state.Buffer.Reset(text)
	}
	t.conflict, t.diskText, t.ignored = false, "", ""
	t.diff = nil
	if t.state.Buffer != nil {
		t.seen = t.state.Buffer.Version()
	}
	if info, err := os.Stat(t.full); err == nil {
		t.mtime, t.size = info.ModTime(), info.Size()
	}
}

// changesSinceHead is the tab's text against HEAD, editable where it
// shows.
func (t *EditorTab) changesSinceHead() *editor.DiffState {
	if t.diff == nil {
		t.diff = &editor.DiffState{
			Old:             editor.NewBuffer(t.head),
			New:             t.state.Buffer,
			Editable:        true,
			Font:            t.state.Font,
			RenderSeparator: separator,
		}
	}
	return t.diff
}

// sameText reports whether two texts read the same, whatever their line
// endings.
func sameText(a, b string) bool {
	if a == b {
		return true
	}
	return strings.ReplaceAll(a, "\r\n", "\n") == strings.ReplaceAll(b, "\r\n", "\n")
}

// unsaved reports whether the tab's text differs from its file.
func (t *EditorTab) unsaved() bool {
	return t.state.Buffer != nil && t.state.Buffer.Modified()
}

// View shows the editor beside the view, as the workspace lays it out.
func (ev *EditorView) View(c *ui.Context) { ev.panel(c) }

// panel shows the tabs, the tools of the tab shown, and its text, in the
// view or in the editor's window.
func (ev *EditorView) panel(c *ui.Context) {
	t := c.Theme()
	if len(ev.tabs) == 0 {
		emptyMessage(c, i18n.T("No files open"))
		return
	}
	if ev.active < 0 || ev.active >= len(ev.tabs) {
		ev.active = 0
	}
	tab := ev.tabs[ev.active]
	ui.Column(c.Key("editor")).Fill().MinWidth(0).Background(t.Background).Children(func() {
		ev.tabBar(c)
		ev.toolbar(c, tab)
		if tab.conflict {
			ev.conflictBar(c, tab)
		}
		ev.watchIdle(tab)
		if tab.showDiff {
			d := tab.changesSinceHead()
			d.Layout = editor.DiffSplit
			if settings.Read(func(s *settings.Settings) bool { return s.UnifiedDiff }) {
				d.Layout = editor.DiffUnified
			}
			v := editor.DiffView(c.Key("changes:"+tab.path), d).Grow(1).FillWidth().MinHeight(0)
			if !ev.inWindow {
				ev.focused = v.Focused()
			}
			return
		}
		if tab.line > 0 {
			tab.state.GoTo(editor.Pos{Line: tab.line - 1})
			tab.line = 0
		}
		v := editor.View(c.Key("text:"+tab.path), &tab.state).Grow(1).FillWidth().MinHeight(0)
		if !ev.inWindow {
			ev.focused = v.Focused()
		}
		if tab.focus {
			tab.focus = false
			v.Focus()
			ev.focused = true
		}
	})
}

// tabBar is the tabs of the files open, each with its icon, a dot when it
// has unsaved changes, and its close button, and the tools that move the
// editor to its window and hide it.
func (ev *EditorView) tabBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c.Key("editor-tabs")).Height(36).PaddingX(8).Gap(2).Shrink(0).Background(chromeBG(t)).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c.Key("tabs")).Grow(1).MinWidth(0).Gap(2).ClipX().Children(func() {
			for ix, tab := range ev.tabs {
				ev.tab(c, ix, tab)
			}
		})
		if ev.inWindow {
			if iconButton(c.Key("dock"), "dock", i18n.T("Put Back in Main Window")).Clicked() {
				ev.Dock()
			}
		} else if iconButton(c.Key("detach"), "open-window", i18n.T("Open in New Window")).Clicked() {
			ev.Detach()
		}
		if iconButton(c.Key("hide"), "chevrons-right", i18n.T("Hide Editor")).Clicked() {
			ev.Hide()
		}
	})
}

func (ev *EditorView) tab(c *ui.Context, ix int, tab *EditorTab) {
	t := c.Theme()
	active := ix == ev.active
	row := ui.Row(c.Key(tab.path)).Height(28).Radius(14).PaddingStart(10).PaddingEnd(3).Gap(6).
		Shrink(1).MinWidth(80).MaxWidth(220).Cursor(ui.CursorPointer).Tooltip(tab.path)
	if active {
		// The tab shown keeps its room; the others give way.
		row.Shrink(0).MaxWidth(280)
		selectedPill(row, t)
	} else {
		row.TextColor(t.TextMuted)
		if row.Hovered() {
			row.Background(t.Text.Alpha(0.06))
		}
	}
	if row.Clicked() {
		ev.activate(ix)
	}
	row.ContextMenu(func(menu *ui.Menu) {
		if menu.Item(i18n.T("Close Others")).Chosen() {
			ev.closeOthers(ix)
		}
		if menu.Item(i18n.T("Close All")).Chosen() {
			ev.closeAll()
		}
		menu.Separator()
		if menu.Item(i18n.T("Show History")).Chosen() {
			showHistory(ev.ws, tab.path)
		}
		if menu.Item(i18n.T("Copy Path")).Chosen() {
			mygo.Clipboard.WriteText(tab.full)
		}
		if menu.Item(revealLabel()).Chosen() {
			mygo.Shell.ShowItemInFolder(tab.full)
		}
	})
	row.Children(func() {
		ui.Image(c, fileIcon(tab.path)).Size(t.FontSize+1, t.FontSize+1)
		ui.Text(c, filepath.Base(tab.path)).SingleLine().Shrink(1).MinWidth(0)
		if tab.unsaved() {
			ui.Text(c, "●").FontSize(t.FontSize - 3).TextColor(t.Accent)
		}
		if iconButton(c.Key("close"), "x", i18n.T("Close Tab")).Radius(11).Padding(3).Shrink(0).Clicked() {
			ev.closeTab(ix)
		}
	})
}

// toolbar saves the tab, and switches between its text and its changes
// since HEAD.
func (ev *EditorView) toolbar(c *ui.Context, tab *EditorTab) {
	t := c.Theme()
	ui.Row(c.Key("editor-toolbar")).Height(32).PaddingX(8).Gap(4).Shrink(0).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		save := iconButton(c.Key("save"), "save", i18n.T("Save (⌘S)"))
		save.Disabled(!tab.unsaved())
		if save.Clicked() {
			tab.save()
		}
		toggle := toggleButton(c.Key("diff"), i18n.T("Changes since HEAD"), tab.showDiff)
		toggle.Tooltip(i18n.T("Show this file's changes against HEAD, edited where they show"))
		if toggle.Clicked() {
			tab.showDiff = !tab.showDiff
			if tab.showDiff {
				tab.diff = nil
			}
		}
		ui.Spacer(c)
		muted(c, tab.path).SingleLine().Shrink(1).MinWidth(0)
		if tab.unsaved() {
			ui.Text(c, i18n.T("Unsaved")).FontSize(t.FontSize - 2).TextColor(t.Warning).Shrink(0)
		}
	})
}

// conflictBar asks what to do with a file that changed on disk under
// edits.
func (ev *EditorView) conflictBar(c *ui.Context, tab *EditorTab) {
	t := c.Theme()
	ui.Row(c.Key("editor-conflict")).Height(30).Shrink(0).PaddingX(10).Gap(8).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Background(t.Warning.Alpha(0.1)).Children(func() {
		ui.Icon(c, icon("triangle-alert")).FontSize(t.FontSize - 1).TextColor(t.Warning)
		ui.Text(c, i18n.T("The file has changed on disk.")).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		if button(c.Key("load"), i18n.T("Load Disk Version")).Clicked() {
			tab.reload(tab.diskText)
		}
		if button(c.Key("keep"), i18n.T("Keep Mine")).Clicked() {
			tab.ignored = tab.diskText
			tab.conflict = false
		}
	})
}

// editorWindowOf is the editor whose window s is, nil for none.
func editorWindowOf(s *Shell) *EditorView {
	if s == nil {
		return nil
	}
	for _, rw := range repoWindows {
		for _, ws := range rw.tabs {
			if ev := ws.editor; ev != nil && ev.window == s {
				return ev
			}
		}
	}
	return nil
}
