package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/console"
	"github.com/Zenyet/banyan/internal/i18n"
)

// ConsoleView is the Git console, like JetBrains' "Console" tab: every git
// command run by a user action, with its output.
type ConsoleView struct {
	ws   *Workspace
	list ui.ListState
}

func (cv *ConsoleView) View(c *ui.Context) {
	t := c.Theme()
	root := cv.ws.Model.Repo.Root()
	entries := console.EntriesFor(root)
	cv.list.FollowEnd = true
	ui.Row(c.Key("console-toolbar")).Height(32).PaddingX(8).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		muted(c, fmt.Sprintf("%d commands", len(entries))).Grow(1)
		if iconButton(c.Key("clear"), "trash", i18n.T("Clear")).Clicked() {
			console.Clear(root)
		}
	})
	if len(entries) == 0 {
		emptyMessage(c, i18n.T("Git commands run by your actions appear here"))
		return
	}
	ui.List(c.Key("console"), &cv.list, len(entries), func(i int) {
		e := entries[i]
		ui.Column(c.Key(i)).PaddingX(12).PaddingY(4).Gap(2).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Selectable().Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				name, color := "check", t.Success
				if !e.OK {
					name, color = "x", t.Danger
				}
				ui.Icon(c, icon(name)).FontSize(t.FontSize - 1).TextColor(color)
				muted(c, formatFullTime(e.At.Unix())).SingleLine()
				ui.Text(c, e.Command).Font(monoFont).Shrink(1)
				muted(c, fmt.Sprintf("%d ms", e.Millis)).SingleLine()
			})
			if e.Output != "" {
				color := t.TextMuted
				if !e.OK {
					color = t.Danger
				}
				ui.Text(c, e.Output).Font(monoFont).FontSize(t.FontSize - 1).TextColor(color).PaddingStart(24)
			}
		})
	}).Grow(1).MinHeight(0)
}
