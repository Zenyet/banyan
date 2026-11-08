package app

import (
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/i18n"
)

// The window's notifications, as macOS shows them: banners of Liquid
// Glass in the top right corner, under the toolbar, each with the symbol
// of its kind in its color, rather than the pill of a web page at the
// bottom.

const (
	notifyWidth = 360
	notifyIn    = 260 * time.Millisecond
	notifyOut   = 180 * time.Millisecond
)

// notifyKind is the look of a kind of toast: "error", "warning",
// "success", or "" for news.
func notifyKind(t *ui.Theme, kind string) (color ui.Color, iconName string) {
	switch kind {
	case "error":
		return t.Danger, "circle-x"
	case "warning":
		return t.Warning, "triangle-alert"
	case "success":
		return t.Success, "circle-check"
	}
	return t.Accent, "info"
}

// notifications builds the toasts of the window, the newest at the top.
func (s *Shell) notifications(c *ui.Context) {
	t := c.Theme()
	top := s.toastTop
	if top == 0 {
		top = 12
	}
	now := c.Now()
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(top, 14, 14, 14).Justify(ui.Start).AlignItems(ui.End).Gap(10)
		// When each came, for it to slide in.
		seen := make(map[string]time.Time, len(toasts))
		for _, ts := range toasts {
			at, ok := s.toastSeen[ts.ID]
			if !ok {
				at = now
			}
			seen[ts.ID] = at
		}
		s.toastSeen = seen
		for i := len(toasts) - 1; i >= 0; i-- {
			s.notification(c, t, toasts[i], now.Sub(seen[toasts[i].ID]))
		}
	})
}

func (s *Shell) notification(c *ui.Context, t *ui.Theme, ts ui.Toast, age time.Duration) {
	color, iconName := notifyKind(t, ts.Type)
	p := ui.ToastBase(c, ts)
	// A pane of Liquid Glass, as macOS's notification banners and the
	// toolbar's groups: round corners, frosted, a soft shadow under it.
	card := p.Root.Row().Width(notifyWidth).MaxWidth(c.Root().Bounds().W-28).Gap(12).Padding(12, 12, 12, 14).Radius(22).
		AlignItems(ui.Center).Material(glass.Glass{})
	shade := ui.RGBA(0, 0, 0, 0.14)
	if t.Dark {
		shade = ui.RGBA(0, 0, 0, 0.4)
	}
	card.Shadow(0, 10, 30, 0, shade)

	// It slides in from the right, and fades as its time ends.
	reduce := c.Preferences().ReduceMotion
	opacity, shift := float32(1), float32(0)
	if age < notifyIn && !reduce {
		k := float32(age) / float32(notifyIn)
		k = 1 - (1-k)*(1-k)*(1-k)
		opacity, shift = k, (1-k)*32
		c.AnimationFrame()
	}
	if left := p.Left(); left > 0 {
		if left < notifyOut {
			opacity = min(opacity, float32(left)/float32(notifyOut))
			c.AnimationFrame()
		} else {
			c.After(left - notifyOut)
		}
	}
	card.Opacity(opacity).Translate(shift, 0)
	// The buttons at its side show while the pointer is on it, as
	// macOS's close button on a banner.
	hover := card.Hovered() || card.FocusWithin()

	card.Children(func() {
		ui.Icon(c, icon(iconName)).FontSize(t.FontSize + 9).TextColor(color).Shrink(0)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, ts.Title).FontWeight(600).MaxLines(3)
			if ts.Description != "" {
				ui.Text(c, ts.Description).FontSize(t.FontSize - 1).TextColor(t.TextMuted).MaxLines(6).Selectable()
			}
			if ts.Action != "" {
				b := p.ActionButton().AlignSelf(ui.Start).Margin(6, 0, 0, 0).PaddingX(12).Height(26).Radius(13).
					Material(glass.Glass{Interactive: true}).TextColor(t.Accent).FontWeight(600).Cursor(ui.CursorPointer)
				b.Children(func() { ui.Text(c, ts.Action).SingleLine() })
			}
		})
		ui.Column(c).Shrink(0).Gap(4).AlignSelf(ui.Start).Opacity(boolOpacity(hover)).Children(func() {
			notifyButton(c, t, p.CloseButton(), "x", i18n.T("Close"))
			if ts.Type == "error" {
				message := strings.TrimSpace(ts.Title + "\n" + ts.Description)
				if notifyButton(c, t, ui.ButtonBase(c), "copy", i18n.T("Copy Message")).Clicked() {
					mygo.Clipboard.WriteText(message)
				}
			}
		})
	})
}

func boolOpacity(on bool) float32 {
	if on {
		return 1
	}
	return 0
}

// notifyButton is a small round button of icon at the side of a
// notification.
func notifyButton(c *ui.Context, t *ui.Theme, b ui.Element, iconName, label string) ui.Element {
	b.Size(22, 22).Radius(11).Center().Background(t.Text.Alpha(0.06)).Label(label).Tooltip(label).TextColor(t.TextMuted).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(t.Text.Alpha(0.08)).TextColor(t.Text)
	}
	b.Children(func() { ui.Icon(c, icon(iconName)).FontSize(t.FontSize - 1) })
	return b
}
