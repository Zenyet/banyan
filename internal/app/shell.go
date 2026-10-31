package app

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// Shell is what every window of the app has around its content: the
// dialogs open over it, one above the other, and the toasts waiting to show.
type Shell struct {
	Win     *mygo.Window
	dialogs []*Dialog
	toasts  []ui.Toast
	// content builds the window's own interface.
	content func(c *ui.Context)
	// onClosed runs once the window is gone.
	onClosed func()
}

// Dialog is a dialog over a window, as GPUI Kit's open_dialog made them:
// Body builds its content for every frame while it is open.
type Dialog struct {
	Title string
	Width float32
	Body  func(c *ui.Context, d *Dialog)
	// OnDismiss runs when Escape or a click outside closes it.
	OnDismiss func()
	open      bool
}

// Close closes the dialog, from its body or anywhere.
func (d *Dialog) Close() { d.open = false }

var shells = map[*mygo.Window]*Shell{}

// newWindow opens a window of native UI around content.
func newWindow(opts mygo.WindowOptions, content func(s *Shell, c *ui.Context)) *Shell {
	s := &Shell{}
	s.content = func(c *ui.Context) { content(s, c) }
	opts.Content = ui.View(s.view)
	s.Win = mygo.NewWindow(opts)
	shells[s.Win] = s
	s.Win.OnClosed(func() {
		delete(shells, s.Win)
		if s.onClosed != nil {
			s.onClosed()
		}
		windowClosed()
	})
	return s
}

// shellOf is the shell of a window, nil for none of ours.
func shellOf(w *mygo.Window) *Shell {
	if w == nil {
		return nil
	}
	return shells[w]
}

// Open shows a dialog over the window.
func (s *Shell) Open(d *Dialog) {
	d.open = true
	if d.Width == 0 {
		d.Width = 440
	}
	s.dialogs = append(s.dialogs, d)
	s.Win.Invalidate()
}

// HasDialog reports whether a dialog is open over the window.
func (s *Shell) HasDialog() bool { return len(s.dialogs) > 0 }

// Toast shows a message near the bottom of the window; kind is "error",
// "warning", "success" or "".
func (s *Shell) Toast(kind, message string) {
	title, description, _ := strings.Cut(message, "\n")
	t := ui.Toast{Title: title, Description: strings.TrimSpace(description), Type: kind}
	if kind == "error" {
		t.Timeout = 10e9
	}
	s.toasts = append(s.toasts, t)
	s.Win.Invalidate()
}

func (s *Shell) view(c *ui.Context) {
	applyTheme(c)
	t := c.Theme()
	root := ui.Column(c).Fill().TextColor(t.Text)
	if c.Vibrancy() {
		// The window's material shows where the view draws no background:
		// its sidebar.
		c.Root().Background(ui.Transparent)
	} else {
		root.Background(t.Background)
	}
	root.Children(func() { s.content(c) })
	for _, d := range s.dialogs {
		was := d.open
		panel := ui.Modal(c.Key(d), &d.open, func() {
			if d.Title != "" {
				ui.Text(c, d.Title).FontSize(t.FontSize + 2).Bold()
			}
			d.Body(c, d)
		})
		panel.Width(d.Width).Radius(16)
		if was && !d.open && panel.Dismissed() && d.OnDismiss != nil {
			d.OnDismiss()
		}
	}
	open := s.dialogs[:0]
	for _, d := range s.dialogs {
		if d.open {
			open = append(open, d)
		}
	}
	s.dialogs = open
	for _, toast := range s.toasts {
		c.AddToast(toast)
	}
	s.toasts = nil
}

// applyTheme sets the theme the settings ask for on the window.
func applyTheme(c *ui.Context) {
	size := settings.Read(func(s *settings.Settings) int { return s.FontSize })
	t := *c.Theme()
	if size >= 9 && size <= 24 {
		t.FontSize = float32(size)
	}
	// Apple's system colors and the macOS window's grays.
	t.Spacing = 4
	t.Radius = 8
	if t.Dark {
		t.Background = ui.Hex("#1e1e1e")
		t.Surface = ui.Hex("#2c2c2e")
		t.SurfaceHover = ui.Hex("#3a3a3c")
		t.SurfacePressed = ui.Hex("#48484a")
		t.Border = ui.Hex("#38383a")
		t.Text = ui.Hex("#f5f5f7")
		t.TextMuted = ui.Hex("#98989d")
		t.Accent = ui.Hex("#0a84ff")
		t.AccentHover = ui.Hex("#3395ff")
		t.AccentPressed = ui.Hex("#0071e3")
		t.Danger = ui.Hex("#ff453a")
		t.Warning = ui.Hex("#ff9f0a")
		t.Success = ui.Hex("#30d158")
	} else {
		t.Background = ui.Hex("#ffffff")
		t.Surface = ui.Hex("#f5f5f7")
		t.SurfaceHover = ui.Hex("#ebebf0")
		t.SurfacePressed = ui.Hex("#e0e0e6")
		t.Border = ui.Hex("#e3e3e8")
		t.Text = ui.Hex("#1d1d1f")
		t.TextMuted = ui.Hex("#86868b")
		t.Accent = ui.Hex("#007aff")
		t.AccentHover = ui.Hex("#0a6fe0")
		t.AccentPressed = ui.Hex("#0062cc")
		t.Danger = ui.Hex("#ff3b30")
		t.Warning = ui.Hex("#ff9500")
		t.Success = ui.Hex("#34c759")
	}
	t.Selection = t.Accent.Alpha(0.25)
	t.Focus = t.Accent.Alpha(0.5)
	c.SetTheme(&t)
}

// buttons are a dialog's buttons at its bottom right: Cancel, then ok.
func dialogButtons(c *ui.Context, d *Dialog, okLabel string, danger, disabled bool, onOK func()) {
	ui.Row(c).Gap(8).Justify(ui.End).Margin(8, 0, 0, 0).Children(func() {
		if button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
			d.Close()
		}
		ok := primaryButton(c.Key("ok"), okLabel).Disabled(disabled)
		if danger {
			ok.Background(c.Theme().Danger)
		}
		if ok.Clicked() {
			onOK()
		}
	})
}

// PromptText asks for a line of text in a dialog.
func (s *Shell) PromptText(title, okLabel, value string, onOK func(string)) {
	submit := func(d *Dialog) {
		if v := strings.TrimSpace(value); v != "" {
			d.Close()
			onOK(v)
		}
	}
	s.Open(&Dialog{Title: title, Width: 420, Body: func(c *ui.Context, d *Dialog) {
		if ui.TextInput(c.Key("value"), &value).AutoFocus().FillWidth().Submitted() {
			submit(d)
		}
		dialogButtons(c, d, okLabel, false, strings.TrimSpace(value) == "", func() { submit(d) })
	}})
}

// PromptMessage asks for a commit message, with note above it. Cmd-Enter
// confirms too.
func (s *Shell) PromptMessage(title, note, okLabel, value string, onOK func(string)) {
	submit := func(d *Dialog) {
		if v := strings.TrimSpace(value); v != "" {
			d.Close()
			onOK(v)
		}
	}
	s.Open(&Dialog{Title: title, Width: 560, Body: func(c *ui.Context, d *Dialog) {
		if note != "" {
			ui.Text(c, note).TextColor(c.Theme().TextMuted)
		}
		area := ui.TextArea(c.Key("value"), &value).AutoFocus().FillWidth().Lines(6, 16).Font(monoFont)
		if area.Shortcut(ui.Cmd, ui.KeyEnter) {
			submit(d)
		}
		dialogButtons(c, d, okLabel, false, strings.TrimSpace(value) == "", func() { submit(d) })
	}})
}

// Confirm asks before doing something destructive.
func (s *Shell) Confirm(title, message, okLabel string, onOK func()) {
	s.Open(&Dialog{Title: title, Width: 440, Body: func(c *ui.Context, d *Dialog) {
		ui.Text(c, message).TextColor(c.Theme().TextMuted)
		dialogButtons(c, d, okLabel, true, false, func() {
			d.Close()
			onOK()
		})
	}})
}

// Report shows a model's events as toasts and dialogs in the window.
func (s *Shell) Report(m *model.Model, e model.Event) {
	switch e.Kind {
	case model.OpFailed:
		s.Toast("error", e.Text)
	case model.OpSucceeded:
		s.Toast("success", e.Text)
	case model.Conflicts:
		s.Toast("warning", i18n.Tf("{} stopped: resolve the conflicts to continue", e.Text))
		if !s.HasDialog() {
			openConflicts(s, m)
		}
	case model.CheckoutBlocked:
		offerSmartCheckout(s, m, e.Target, e.Remote, e.Text)
	}
}
