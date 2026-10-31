package app

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// DiffPane is the diff preview shared by the Log and Commit views.
type DiffPane struct {
	title   string
	message string
	state   *editor.DiffState
	// loader makes the patch shown, again whenever a diff option changes;
	// whitespace is the option it last ran with.
	loader     func() (string, error)
	whitespace bool
	generation int
	// line is a line of the new file to scroll to once shown, from 1.
	line int
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

func newDiffPane() *DiffPane {
	return &DiffPane{message: i18n.T("Select a file to see its changes")}
}

// Load shows the patch load makes, off the main thread. It runs again when
// the whitespace option changes.
func (p *DiffPane) Load(title string, load func() (string, error)) {
	p.title = title
	p.loader = load
	p.run()
	p.redraw()
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
	generation, load, title := p.generation, p.loader, p.title
	p.loading, p.loadingFrom = true, time.Now()
	go func() {
		time.Sleep(diffLoadingDelay)
		model.Post(func() {})
	}()
	go func() {
		patch, err := load()
		model.Post(func() {
			if p.generation != generation {
				return
			}
			p.loading = false
			if err != nil {
				p.setMessage(title, err.Error())
				return
			}
			p.setPatch(patch)
		})
	}()
}

func (p *DiffPane) setPatch(patch string) {
	files, err := editor.ParsePatch(patch)
	switch {
	case err != nil:
		p.message = i18n.T("Cannot show diff") + ": " + err.Error()
		p.state = nil
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
	p.loader = nil
	p.generation++
	p.loading = false
	p.setMessage(title, message)
	p.redraw()
}

func (p *DiffPane) setMessage(title, message string) {
	p.title = title
	p.message = message
	p.state = nil
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

func (p *DiffPane) View(c *ui.Context) {
	t := c.Theme()
	unified := settings.Read(func(s *settings.Settings) bool { return s.UnifiedDiff })
	whitespace := settings.Read(func(s *settings.Settings) bool { return s.IgnoreWhitespace })
	if p.loader != nil && whitespace != p.whitespace {
		p.run()
	}
	ui.Column(c).Grow(1).MinHeight(0).Background(t.Background).Children(func() {
		ui.Row(c.Key("diff-toolbar")).Height(32).PaddingX(8).Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			title := p.title
			if title == "" {
				title = i18n.T("Diff")
			}
			ui.Text(c, title).SingleLine().Grow(1).Shrink(1)
			if iconButton(c.Key("prev"), "arrow-up", i18n.T("Previous Change")).Clicked() && p.state != nil {
				i, _ := p.state.CurrentHunk()
				p.state.GoToHunk(max(i-1, 0))
			}
			if iconButton(c.Key("next"), "arrow-down", i18n.T("Next Change")).Clicked() && p.state != nil {
				i, _ := p.state.CurrentHunk()
				p.state.GoToHunk(min(i+1, len(p.state.Hunks())-1))
			}
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
		if p.state == nil {
			emptyMessage(c, p.message)
			return
		}
		p.state.Layout = editor.DiffSplit
		if unified {
			p.state.Layout = editor.DiffUnified
		}
		editor.DiffView(c.Key(p.state), p.state).Grow(1).FillWidth().MinHeight(0)
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
