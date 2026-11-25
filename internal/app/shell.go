package app

import (
	"runtime"
	"strings"
	"time"

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
	// toastTop is how far from the top of the window the notifications
	// show, clear of the toolbar; toastSeen holds when each came.
	toastTop  float32
	toastSeen map[string]time.Time
	// content builds the window's own interface.
	content func(c *ui.Context)
	// onClosed runs once the window is gone.
	onClosed func()
	// size is the window's size at the last frame.
	size [2]float32
	// controlsInset is how far from the top of a window whose title bar
	// hides its controls are, inset over the view.
	controlsInset float32
}

// Dialog is a dialog over a window, as GPUI Kit's open_dialog made them:
// Body builds its content for every frame while it is open.
type Dialog struct {
	Title string
	Width float32
	// Fill makes the body fill the window edge to edge, as a diff
	// viewer's does, and Height is its content height. Without Fill the
	// window sizes to its content.
	Fill   bool
	Height float32
	Body   func(c *ui.Context, d *Dialog)
	// OnDismiss runs when Escape or the window's close control closes it.
	OnDismiss func()
	open      bool
	// win is the native window of a dialog in the app; a shell of the
	// tests draws its dialogs over its own window instead.
	win *mygo.Window
	// byCode is whether the body closed it, which runs no OnDismiss.
	byCode bool
	// blocked is whether a dialog over this one covers it, as a sheet
	// does on macOS, where child windows are not modal.
	blocked bool
}

// Close closes the dialog, from its body or anywhere.
func (d *Dialog) Close() {
	d.open = false
	d.byCode = true
	if d.win != nil {
		d.win.Close()
	}
}

// dismiss closes the dialog as Escape or the window's close control does,
// running OnDismiss.
func (d *Dialog) dismiss() {
	d.open = false
	if d.win != nil {
		d.byCode = false
		d.win.Close()
		return
	}
	d.byCode = true
	if d.OnDismiss != nil {
		d.OnDismiss()
	}
}

var shells = map[*mygo.Window]*Shell{}

// dialogWindows are the native windows of dialogs, which model.Post and
// settings changes redraw along with the shells.
var dialogWindows = map[*mygo.Window]bool{}

// invalidateWindows redraws every window of the app, dialogs included.
func invalidateWindows() {
	for w := range shells {
		w.Invalidate()
	}
	for w := range dialogWindows {
		w.Invalidate()
	}
}

// newWindow opens a window of native UI around content.
func newWindow(opts mygo.WindowOptions, content func(s *Shell, c *ui.Context)) *Shell {
	s := &Shell{toastTop: float32(opts.TitleBarHeight) + 10}
	if opts.TitleBarStyle == mygo.TitleBarHiddenInset {
		s.controlsInset = 40
	}
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

// Open shows a dialog: a native window of its own over the window, as
// JetBrains' dialogs are, or over the shell's window in the tests.
func (s *Shell) Open(d *Dialog) {
	d.open = true
	if d.Width == 0 {
		d.Width = 440
	}
	if s.Win == nil {
		s.dialogs = append(s.dialogs, d)
		return
	}
	s.openNative(d)
}

// openNative shows d in a native child window, sized to its content once
// that has laid out. On macOS it is a child window with the window's own
// traffic lights, over a blocked parent; elsewhere the system makes it
// modal to the parent.
func (s *Shell) openNative(d *Dialog) {
	parent := s.topWindow()
	if parent == nil {
		return
	}
	pb := parent.Bounds()
	width := min(int(d.Width), max(pb.Width-80, 280))
	maxHeight := max(pb.Height-120, 200)
	modal := runtime.GOOS != "darwin"
	if !modal {
		if prev := s.topDialog(); prev != nil {
			prev.blocked = true
			if prev.win != nil {
				prev.win.Invalidate()
			}
		}
	}
	var win *mygo.Window
	var root ui.Element
	shown := false
	var height, last float32
	stable, frames := 0, 0
	userSized := false
	var fitAt time.Time
	content := ui.View(func(c *ui.Context) {
		applyTheme(c)
		t := c.Theme()
		if c.Shortcut(0, ui.KeyEscape) {
			d.dismiss()
			return
		}
		if d.Fill {
			root = ui.Column(c).Fill().Background(t.Background).TextColor(t.Text)
			root.Children(func() {
				d.Body(c, d)
				if d.blocked {
					ui.Box(c.Key("blocked")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Modal().
						HandleInput(func(ui.InputEvent) bool { return true })
				}
			})
		} else {
			root = ui.Column(c).FillWidth().Padding(20).Gap(12).Background(t.Background).TextColor(t.Text)
			root.Children(func() {
				d.Body(c, d)
				if d.blocked {
					ui.Box(c.Key("blocked")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Modal().
						HandleInput(func(ui.InputEvent) bool { return true })
				}
			})
		}
		if d.Fill {
			return
		}
		// The body may load or grow after the window shows, as a dialog
		// that waits for a command does; follow it. Until its height has
		// settled, though, show nothing: a first frame can measure part
		// of the content, and the window would grow from there.
		frames++
		if h := root.Bounds().H; h > 0 {
			h = min(h, float32(maxHeight))
			if h < last-1 || h > last+1 {
				stable = 0
			} else {
				stable++
			}
			last = h
			settled := stable >= 1 || frames >= 30
			switch {
			case !shown && settled:
				shown = true
				height = h
				fitAt = time.Now()
				win.SetContentSize(width, int(h))
				pb = parent.Bounds()
				win.SetPosition(pb.X+(pb.Width-width)/2, pb.Y+(pb.Height-int(h))/2)
				win.SetOpacity(1)
				win.Focus()
			case shown && !userSized && (h < height-1 || h > height+1):
				height = h
				fitAt = time.Now()
				win.SetContentSize(width, int(h))
				pb = parent.Bounds()
				win.SetPosition(pb.X+(pb.Width-width)/2, pb.Y+(pb.Height-int(h))/2)
			}
		} else if !shown {
			// Lay out once more, now that the content has a size.
			c.AnimationFrame()
		}
	})
	s.dialogs = append(s.dialogs, d)
	opts := mygo.WindowOptions{
		Title: d.Title, Width: width, UseContentSize: true,
		Parent: parent, Modal: modal, SkipTaskbar: true, Content: content,
	}
	if d.Fill {
		h := int(d.Height)
		if h <= 0 {
			h = 620
		}
		opts.Height = h
		opts.MinWidth, opts.MinHeight = 360, 240
		opts.Opacity = 1
	} else {
		opts.Height = 120
		opts.Hidden = true
		opts.Opacity = 0
		opts.MinWidth, opts.MinHeight = 320, 160
	}
	win = mygo.NewWindow(opts)
	if !d.Fill {
		win.OnResize(func() {
			if shown && time.Since(fitAt) > 150*time.Millisecond {
				userSized = true
			}
		})
		// Lay out once while hidden, so the window first shows at its
		// size rather than growing from 120.
		win.ShowInactive()
	}
	if d.Fill {
		pb = parent.Bounds()
		win.SetPosition(pb.X+(pb.Width-width)/2, pb.Y+(pb.Height-int(opts.Height))/2)
		win.Focus()
	}
	d.win = win
	dialogWindows[win] = true
	if !d.open {
		win.Close()
	}
	if s.Win != nil {
		s.Win.Invalidate()
	}
	win.OnClosed(func() {
		delete(dialogWindows, win)
		d.open, d.win = false, nil
		s.remove(d)
		if !modal {
			if prev := s.topDialog(); prev != nil {
				prev.blocked = false
				if prev.win != nil {
					prev.win.Invalidate()
				}
			}
			if s.Win != nil {
				s.Win.Invalidate()
			}
		}
		if !d.byCode && d.OnDismiss != nil {
			d.OnDismiss()
		}
	})
}

// topDialog is the last dialog open, nil for none.
func (s *Shell) topDialog() *Dialog {
	for i := len(s.dialogs) - 1; i >= 0; i-- {
		if s.dialogs[i].win != nil {
			return s.dialogs[i]
		}
	}
	return nil
}

// topWindow is the window the next dialog goes over: the last dialog
// open, or the shell's own window.
func (s *Shell) topWindow() *mygo.Window {
	if d := s.topDialog(); d != nil {
		return d.win
	}
	return s.Win
}

// remove takes a closed dialog out of the stack.
func (s *Shell) remove(d *Dialog) {
	for i, x := range s.dialogs {
		if x == d {
			s.dialogs = append(s.dialogs[:i], s.dialogs[i+1:]...)
			return
		}
	}
}

// HasDialog reports whether a dialog is open over the window.
func (s *Shell) HasDialog() bool { return len(s.dialogs) > 0 }

// Toast shows a message as a notification in the top right corner of the
// window; kind is "error", "warning", "success" or "".
func (s *Shell) Toast(kind, message string) {
	s.ToastAction(kind, message, "", nil)
}

// ToastAction shows a notification with a button that runs onAction, as
// Undo after a change.
func (s *Shell) ToastAction(kind, message, action string, onAction func()) {
	title, description, _ := strings.Cut(message, "\n")
	t := ui.Toast{Title: title, Description: strings.TrimSpace(description), Type: kind, Action: action, OnAction: onAction}
	if kind == "error" {
		t.Timeout = 10e9
	} else if action != "" {
		// Time to notice the button after reading the message.
		t.Timeout = 15e9
	}
	s.toasts = append(s.toasts, t)
	// A shell of the tests has no window, and a window closed under a
	// repository still working has nothing to show.
	if s.Win != nil {
		s.Win.Invalidate()
	}
}

// windowClose is a dialog's window-style close control: the red light of
// a macOS window, or an X in the top right elsewhere.
func windowClose(c *ui.Context, d *Dialog) {
	t := c.Theme()
	if runtime.GOOS == "darwin" {
		b := ui.ButtonBase(c.Key("window-close")).Label(i18n.T("Close")).Tooltip(i18n.T("Close")).
			Size(12, 12).Radius(6).Background(ui.Hex("#ff5f57")).Border(1, ui.Hex("#e0443e")).Center().Shrink(0)
		if b.Hovered() {
			b.Children(func() { ui.Icon(c, icon("x")).FontSize(8).TextColor(ui.Hex("#7a100c")) })
		}
		if b.Clicked() {
			d.dismiss()
		}
		return
	}
	b := ui.ButtonBase(c.Key("window-close")).Label(i18n.T("Close")).Tooltip(i18n.T("Close")).
		Size(28, 24).Radius(6).Center().Shrink(0)
	color := t.TextMuted
	if b.Hovered() {
		b.Background(t.Danger.Alpha(0.14))
		color = t.Danger
	}
	b.Children(func() { ui.Icon(c, icon("x")).FontSize(t.FontSize + 1).TextColor(color) })
	if b.Clicked() {
		d.dismiss()
	}
}

func (s *Shell) view(c *ui.Context) {
	applyTheme(c)
	// A resized window builds one frame more, for the parts that fit
	// themselves to the width they had at the last one (measuredWidth).
	if w, h := c.Size(); [2]float32{w, h} != s.size {
		s.size = [2]float32{w, h}
		c.AnimationFrame()
	}
	t := c.Theme()
	root := ui.Column(c).Fill().TextColor(t.Text)
	if c.Vibrancy() {
		// The window's material shows where the view draws no background:
		// its sidebar.
		c.Root().Background(ui.Transparent)
	} else {
		root.Background(t.Background)
	}
	root.Children(func() {
		s.content(c)
		// macOS child windows are not modal: a dialog's window covers
		// this one, and this keeps the window under it from taking input.
		if s.Win != nil && runtime.GOOS == "darwin" && s.HasDialog() {
			ui.Box(c.Key("dialog-blocker")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Modal().
				HandleInput(func(ui.InputEvent) bool { return true })
		}
	})
	if s.Win == nil {
		for _, d := range s.dialogs {
			was := d.open
			panel := ui.Modal(c.Key(d), &d.open, func() {
				ui.Row(c.Key("dialog-title")).AlignItems(ui.Center).Gap(8).Children(func() {
					if runtime.GOOS == "darwin" {
						windowClose(c, d)
					}
					if d.Title != "" {
						ui.Text(c, d.Title).FontSize(t.FontSize + 2).Bold().SingleLine().Grow(1).Shrink(1).MinWidth(0)
					} else {
						ui.Spacer(c)
					}
					if runtime.GOOS != "darwin" {
						windowClose(c, d)
					}
				})
				d.Body(c, d)
			})
			// As macOS's windows and sheets: a hairline around, lighter in
			// the dark, and a deep, soft shadow.
			edge, shade := ui.RGBA(0, 0, 0, 0.12), ui.RGBA(0, 0, 0, 0.22)
			if t.Dark {
				edge, shade = ui.RGBA(255, 255, 255, 0.14), ui.RGBA(0, 0, 0, 0.55)
			}
			panel.Width(d.Width).Radius(16).Border(1, edge).Shadow(0, 18, 50, 0, shade)
			if s.controlsInset > 0 {
				// Centered, a dialog keeps clear of the window's controls
				// above it, however short the window.
				_, h := c.Size()
				panel.MaxHeight(h - 2*s.controlsInset)
			}
			if was && !d.open && panel.Dismissed() && d.OnDismiss != nil {
				d.OnDismiss()
			}
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
	s.notifications(c)
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
