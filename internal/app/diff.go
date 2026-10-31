package app

import (
	"time"

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
			if toggleButton(c.Key("split"), i18n.T("Side-by-side"), !unified).Clicked() {
				changeSettings(func(s *settings.Settings) { s.UnifiedDiff = false })
			}
			if toggleButton(c.Key("unified"), i18n.T("Unified"), unified).Clicked() {
				changeSettings(func(s *settings.Settings) { s.UnifiedDiff = true })
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
	b := ui.Button(c, label).PaddingX(8).PaddingY(3).FontSize(t.FontSize-1).Border(0, ui.Color{})
	switch {
	case on:
		b.Background(t.Accent.Alpha(0.18)).TextColor(t.Text)
	case b.Hovered():
		b.Background(hoverBG(t))
	default:
		b.Background(ui.Color{}).TextColor(t.TextMuted)
	}
	return b
}
