package app

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/preview"
	"github.com/Zenyet/banyan/internal/settings"
)

// DiffSource is what a diff pane shows: a file's patch, its two versions
// for a binary file's preview, and where the file is.
type DiffSource struct {
	// Title is the file as the pane names it.
	Title string
	// Path is the file's path in the repository, in its new version: the
	// file the editor opens, when it is in the work tree and holds text.
	Path string
	// Patch makes the patch shown for a text file.
	Patch func() (string, error)
	// Old and New read the file's two versions, nil where the file was
	// not there: an added or deleted file. They are read for a binary
	// file's preview, off the main thread.
	Old, New func() ([]byte, error)
}

// readVersion reads one version of a file, nil when it was not there.
func readVersion(load func() ([]byte, error)) ([]byte, error) {
	if load == nil {
		return nil, nil
	}
	return load()
}

// blob reads a file at a revision, nil where it was not there.
func blob(repo *git.Repo, rev, path string) func() ([]byte, error) {
	return func() ([]byte, error) {
		if rev == "" || path == "" {
			return nil, nil
		}
		return repo.Blob(rev, path)
	}
}

// worktreeFile reads a file of the work tree, nil when it is not there.
func worktreeFile(root, path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// load reads what a source shows: the patch parsed, and for a binary file
// the preview of its versions.
func (s *DiffSource) load() ([]editor.FilePatch, *binaryPreview, error) {
	if s.Patch == nil {
		return nil, nil, nil
	}
	patch, err := s.Patch()
	if err != nil {
		return nil, nil, err
	}
	files, err := editor.ParsePatch(patch)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 || !files[0].Binary || s.Old == nil && s.New == nil {
		return files, nil, nil
	}
	old, err := readVersion(s.Old)
	if err != nil {
		return nil, nil, err
	}
	new, err := readVersion(s.New)
	if err != nil {
		return nil, nil, err
	}
	return files, newBinaryPreview(s.Path, old, new), nil
}

// DiffPane is the diff preview shared by the Log and Commit views.
type DiffPane struct {
	title   string
	message string
	state   *editor.DiffState
	// source is what the pane shows, and binary the preview of a binary
	// file, which shows in place of the text diff.
	source *DiffSource
	binary *binaryPreview
	// ws is the workspace whose editor opens the file, and editable
	// whether the file is in the work tree and holds text, at editLine.
	ws       *Workspace
	editable bool
	editLine int
	// source makes the patch shown, again whenever a diff option changes;
	// whitespace is the option it last ran with.
	whitespace bool
	generation int
	// line is a line of the new file to scroll to once shown, from 1.
	line int
	// toolbarWidth is the toolbar's width at the last frame, for whether
	// its options fit.
	toolbarWidth float32
	// loading is set while the loader runs, since loadingFrom.
	loading     bool
	loadingFrom time.Time
	// Remote says the patches come from a partial clone, whose file
	// contents may have to be downloaded first.
	Remote bool
	// WindowTitle, when set, lets the pane move to a window of its own,
	// which it names, and back, and hide: inWindow says it shows in that
	// window rather than in its view, and hidden that it shows nowhere
	// until Reveal. window is the window while it is open.
	WindowTitle func() string
	inWindow    bool
	hidden      bool
	window      *Shell
}

// loadingDelay is how long a diff takes before it says it's loading, so
// quick ones don't flicker.
const diffLoadingDelay = 250 * time.Millisecond

// diffOptionsWidth is the width of a diff's toolbar below which its
// options go into a menu.
const diffOptionsWidth = 560

func newDiffPane(ws *Workspace) *DiffPane {
	return &DiffPane{ws: ws, message: i18n.T("Select a file to see its changes")}
}

// Load shows the patch load makes, off the main thread. It runs again when
// the whitespace option changes.
func (p *DiffPane) Load(title string, load func() (string, error)) {
	p.Show(DiffSource{Title: title, Patch: load})
}

// Show shows what src holds, off the main thread: a text file's patch, or
// for a binary file the preview of its versions.
func (p *DiffPane) Show(src DiffSource) {
	p.source = &src
	p.title = src.Title
	p.binary = nil
	p.checkEditable()
	p.run()
	p.redraw()
}

// checkEditable says whether the file can be opened in the editor: it
// exists in the work tree, and holds text.
func (p *DiffPane) checkEditable() {
	p.editable = false
	if p.ws == nil || p.source == nil || p.source.Path == "" {
		return
	}
	// The files shown as pictures or documents are not text.
	switch preview.KindOf(p.source.Path) {
	case preview.Image, preview.Spreadsheet, preview.Document:
		return
	}
	full := filepath.Join(p.ws.Model.Repo.Root(), filepath.FromSlash(p.source.Path))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return
	}
	p.editable = isTextFile(full)
}

// isTextFile reports whether a file holds text: it has no NUL byte near its
// start.
func isTextFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8000)
	n, _ := f.Read(buf)
	return !strings.ContainsRune(string(buf[:n]), 0)
}

// Docked reports whether the pane shows in the view it belongs to, which
// leaves it no room otherwise.
func (p *DiffPane) Docked() bool { return !p.inWindow && !p.hidden }

// Reveal shows the pane where it last showed, in its view or in its
// window, which comes to the front.
func (p *DiffPane) Reveal() {
	p.hidden = false
	switch {
	case !p.inWindow:
	case p.window == nil:
		p.openWindow()
	default:
		p.window.Win.Show()
		p.window.Win.Focus()
	}
}

// Hide shows the pane nowhere until Reveal, closing its window.
func (p *DiffPane) Hide() {
	p.hidden = true
	p.closeWindow()
}

// detach moves the pane to a window of its own.
func (p *DiffPane) detach() {
	p.inWindow, p.hidden = true, false
	p.openWindow()
}

// Dock moves the pane back to its view from its window.
func (p *DiffPane) Dock() {
	p.inWindow, p.hidden = false, false
	p.closeWindow()
}

func (p *DiffPane) openWindow() {
	s := newWindow(mygo.WindowOptions{Title: p.WindowTitle(), Width: 1100, Height: 760, MinWidth: 480, MinHeight: 320},
		func(_ *Shell, c *ui.Context) { p.View(c) })
	s.onClosed = func() {
		p.window = nil
		// Closed from its title bar, the window hides the pane, which
		// Reveal brings back there.
		if p.inWindow {
			p.hidden = true
		}
		// The view it belongs to lays out again.
		model.Post(func() {})
	}
	p.window = s
}

// closeWindow closes the pane's window, if it is open.
func (p *DiffPane) closeWindow() {
	if w := p.window; w != nil {
		w.Win.Close()
	}
}

// redraw shows a change made to the pane in its window, which the view
// making it does not draw.
func (p *DiffPane) redraw() {
	if p.window != nil {
		p.window.Win.Invalidate()
	}
}

func (p *DiffPane) run() {
	p.whitespace = settings.Read(func(s *settings.Settings) bool { return s.IgnoreWhitespace })
	p.generation++
	generation, src, title := p.generation, p.source, p.title
	p.loading, p.loadingFrom = true, time.Now()
	go func() {
		time.Sleep(diffLoadingDelay)
		model.Post(func() {})
	}()
	go func() {
		files, bin, err := src.load()
		model.Post(func() {
			if p.generation != generation {
				return
			}
			p.loading = false
			if err != nil {
				p.setMessage(title, err.Error())
				return
			}
			p.apply(title, files, bin)
		})
	}()
}

// apply shows what the source loaded: a patch, or a binary file's preview.
func (p *DiffPane) apply(title string, files []editor.FilePatch, bin *binaryPreview) {
	p.title = title
	p.editLine = 1
	if len(files) > 0 && len(files[0].Hunks) > 0 {
		p.editLine = max(files[0].Hunks[0].NewStart, 1)
	}
	p.binary = bin
	switch {
	case bin != nil:
		// Pictures are decoded on the main thread, and a picture that
		// cannot be shown falls back to a dump.
		bin.prepare()
		p.message = ""
		p.state = bin.diffState()
		if p.state != nil && p.line > 0 {
			p.goToLine(p.line)
		}
	case len(files) == 0:
		p.message = i18n.T("No differences")
		p.state = nil
	case files[0].Binary:
		p.message = i18n.T("Binary file")
		p.state = nil
	default:
		p.message = ""
		p.state = &editor.DiffState{RenderSeparator: separator}
		p.state.SetPatch(files[0])
		if p.line > 0 {
			p.goToLine(p.line)
		}
	}
}

// ShowMessage shows a message in place of a diff.
func (p *DiffPane) ShowMessage(title, message string) {
	p.source, p.binary, p.editable = nil, nil, false
	p.generation++
	p.loading = false
	p.setMessage(title, message)
	p.redraw()
}

func (p *DiffPane) setMessage(title, message string) {
	p.title = title
	p.message = message
	p.state = nil
	p.binary = nil
}

func (p *DiffPane) Clear() { p.ShowMessage("", i18n.T("Select a file to see its changes")) }

// ScrollToLine scrolls to a line of the new version of the file shown,
// from 1, once it is.
func (p *DiffPane) ScrollToLine(line int) {
	p.line = line
	if p.state != nil {
		p.goToLine(line)
	}
	p.redraw()
}

func (p *DiffPane) goToLine(line int) {
	for i, h := range p.state.Hunks() {
		if h.NewEnd >= line-1 {
			p.state.GoToHunk(i)
			break
		}
	}
	p.line = 0
}

// textShown reports whether the pane shows a diff of lines, rather than
// pictures or cells, whose options differ.
func (p *DiffPane) textShown() bool {
	return p.binary == nil || p.binary.diffState() != nil
}

func (p *DiffPane) View(c *ui.Context) {
	t := c.Theme()
	unified := settings.Read(func(s *settings.Settings) bool { return s.UnifiedDiff })
	whitespace := settings.Read(func(s *settings.Settings) bool { return s.IgnoreWhitespace })
	if p.textShown() && p.source != nil && p.source.Patch != nil && whitespace != p.whitespace {
		p.run()
	}
	text := p.textShown()
	ui.Column(c).Grow(1).MinHeight(0).Background(t.Background).Children(func() {
		bar := ui.Row(c.Key("diff-toolbar")).Height(32).PaddingX(8).Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).ClipX()
		// A narrow pane gathers the options into a menu, to leave the
		// title room.
		measuredWidth(c, bar, &p.toolbarWidth)
		narrow := p.toolbarWidth > 0 && p.toolbarWidth < diffOptionsWidth
		bar.Children(func() {
			title := p.title
			if title == "" {
				title = i18n.T("Diff")
			}
			ui.Text(c, title).SingleLine().Grow(1).Shrink(1).MinWidth(0).Tooltip(title)
			if p.editable && p.ws != nil && p.ws.editor != nil {
				if iconButton(c.Key("edit"), "pencil", i18n.T("Edit File")).Clicked() {
					p.ws.editor.Open(p.source.Path, p.editLine)
					// The editor shows in the window the pane may not be
					// drawn in.
					if p.ws.Shell != nil && p.ws.Shell.Win != nil {
						p.ws.Shell.Win.Invalidate()
					}
				}
			}
			if text {
				if iconButton(c.Key("prev"), "arrow-up", i18n.T("Previous Change")).Clicked() && p.state != nil {
					i, _ := p.state.CurrentHunk()
					p.state.GoToHunk(max(i-1, 0))
				}
				if iconButton(c.Key("next"), "arrow-down", i18n.T("Next Change")).Clicked() && p.state != nil {
					i, _ := p.state.CurrentHunk()
					p.state.GoToHunk(min(i+1, len(p.state.Hunks())-1))
				}
			}
			if narrow {
				iconButton(c.Key("options"), "settings", i18n.T("View Options")).Menu(func(menu *ui.Menu) {
					if text && menu.Item(i18n.T("Ignore Whitespace")).Checked(whitespace).Chosen() {
						changeSettings(func(s *settings.Settings) { s.IgnoreWhitespace = !s.IgnoreWhitespace })
					}
					if text {
						menu.Separator()
					}
					if menu.Item(i18n.T("Side-by-side")).Checked(!unified).Chosen() {
						changeSettings(func(s *settings.Settings) { s.UnifiedDiff = false })
					}
					if menu.Item(i18n.T("Unified")).Checked(unified).Chosen() {
						changeSettings(func(s *settings.Settings) { s.UnifiedDiff = true })
					}
				})
			} else {
				if text {
					ws := toggleButton(c.Key("whitespace"), i18n.T("Ignore Whitespace"), whitespace).Tooltip(i18n.T("Leave out changes that only touch whitespace"))
					if ws.Clicked() {
						changeSettings(func(s *settings.Settings) { s.IgnoreWhitespace = !s.IgnoreWhitespace })
					}
					segments(c.Key("layout"), func() {
						if segment(c.Key("split"), i18n.T("Side-by-side"), !unified).Clicked() {
							changeSettings(func(s *settings.Settings) { s.UnifiedDiff = false })
						}
						if segment(c.Key("unified"), i18n.T("Unified"), unified).Clicked() {
							changeSettings(func(s *settings.Settings) { s.UnifiedDiff = true })
						}
					})
				}
			}
			if p.WindowTitle != nil {
				if p.inWindow {
					if iconButton(c.Key("dock"), "dock", i18n.T("Back to the Main Window")).Clicked() {
						p.Dock()
					}
				} else {
					if iconButton(c.Key("detach"), "open-window", i18n.T("Open in a New Window")).Clicked() {
						p.detach()
					}
					if iconButton(c.Key("hide"), "x", i18n.T("Hide the Diff (double-click a file to show it again)")).Clicked() {
						p.Hide()
					}
				}
			}
		})
		// What showed before is another file's: rather than leave it under
		// this one's title, say the diff is on its way.
		if p.loading && time.Since(p.loadingFrom) >= diffLoadingDelay {
			message := i18n.T("Loading…")
			if p.Remote {
				message = i18n.T("Downloading the file's contents from the remote (this repository is a partial clone)…")
			}
			ui.Column(c.Key("loading")).Grow(1).Center().Gap(8).Padding(16).Children(func() {
				ui.Spinner(c).Size(16, 16)
				ui.Text(c, message).TextColor(t.TextMuted).TextAlign(ui.Center)
			})
			return
		}
		if p.binary != nil && p.binary.diffState() == nil {
			if p.binary.note != "" {
				binaryNote(c, p.binary.note)
			}
			p.binary.View(c)
			return
		}
		if p.state == nil {
			emptyMessage(c, p.message)
			return
		}
		if p.binary != nil && p.binary.note != "" {
			binaryNote(c, p.binary.note)
		}
		p.state.Layout = editor.DiffSplit
		if unified {
			p.state.Layout = editor.DiffUnified
		}
		editor.DiffView(c.Key(p.state), p.state).Grow(1).FillWidth().MinHeight(0)
	})
}

// binaryNote says what a binary preview leaves out, in a slim bar.
func binaryNote(c *ui.Context, note string) {
	t := c.Theme()
	ui.Row(c.Key("preview-note")).Height(24).PaddingX(10).Gap(6).Shrink(0).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Background(t.Warning.Alpha(0.08)).Children(func() {
		ui.Icon(c, icon("info")).FontSize(t.FontSize - 1).TextColor(t.Warning)
		muted(c, note).SingleLine().Shrink(1).MinWidth(0)
	})
}

// separator draws a hunk's header as the viewer's own separator does,
// letting the pointer through it, every part of it, to the lines under it.
// MyGo lays separators over its editors rather than in them, so the wheel
// over one reached no editor and scrolled nothing. Separators of unchanged
// lines, which have buttons to expand them, keep the viewer's look; the
// patches shown here have none.
func separator(c *ui.Context, sep ui.Element, s *editor.DiffSeparator) {
	if s.Lines > 0 {
		editor.DefaultSeparator(c, sep, s)
		return
	}
	t := c.Theme()
	ct := s.Theme()
	sep.Padding(4).PassThrough().Children(func() {
		ui.Row(c).Grow(1).AlignSelf(ui.Stretch).PaddingX(t.Space(1.5)).Radius(6).
			Background(ct.Background.Mix(ct.Text, 0.06)).PassThrough().Children(func() {
			ui.Text(c, s.Header).Font(s.Font().Family).FontSize(s.Font().Size - 1).TextColor(t.TextMuted).
				SingleLine().Shrink(1).PassThrough()
		})
	})
}

// toggleButton is a small borderless button that shows whether it is on.
func toggleButton(c *ui.Context, label string, on bool) ui.Element {
	t := c.Theme()
	b := button(c, label).Height(24).PaddingX(10).FontSize(t.FontSize-1).Border(0, ui.Color{})
	switch {
	case on:
		b.Background(t.Accent.Alpha(0.16)).TextColor(t.Accent)
	case b.Hovered():
		b.Background(t.Text.Alpha(0.08))
	default:
		b.Background(ui.Color{}).TextColor(t.TextMuted)
	}
	return b
}
