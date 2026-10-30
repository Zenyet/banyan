package app

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

//go:embed banyan.png
var logoPNG []byte

var logo *ui.Bitmap

// welcome is the window shown when no repository is open, like JetBrains'
// welcome screen: open or clone a repository, reopen a recent one, or drop
// a folder onto the window.
var welcome *Shell

// welcomeClosedLast is set when the welcome window was the last window
// closed, which quits.
var welcomeClosedLast bool

// openWelcome shows the welcome window, or brings it forward if it is
// open.
func openWelcome() {
	if welcome != nil {
		welcome.Win.Show()
		welcome.Win.Focus()
		return
	}
	if logo == nil {
		logo, _ = ui.DecodeBitmap(logoPNG)
	}
	s := newWindow(mygo.WindowOptions{Title: i18n.T("Welcome to Banyan"), Width: 860, Height: 540}, welcomeView)
	s.onClosed = func() {
		welcome = nil
		welcomeClosedLast = len(shells) == 0
	}
	welcome = s
}

// closeWelcome closes the welcome window once a repository opens.
func closeWelcome() {
	if welcome != nil {
		w := welcome
		welcome = nil
		w.onClosed = nil
		w.Win.Close()
	}
}

func welcomeView(s *Shell, c *ui.Context) {
	t := c.Theme()
	root := ui.Row(c.Key("welcome")).Fill()
	if files := root.DroppedFiles(); len(files) > 0 {
		openPath(s, files[0], true)
	}
	dragging := root.FileDragOver()
	root.Children(func() {
		ui.Column(c.Key("actions")).Width(280).FillHeight().Padding(24).Gap(8).Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Column(c).AlignItems(ui.Center).Gap(4).PaddingY(16).Children(func() {
				if logo != nil {
					ui.Image(c, logo).Size(88, 88)
				}
				ui.Text(c, i18n.T("Banyan")).FontSize(t.FontSize + 7).Bold()
				muted(c, i18n.Tf("Version {}", Version))
			})
			if ui.PrimaryButton(c.Key("open"), i18n.T("Open Repository…")).FillWidth().Clicked() {
				promptOpenRepo(s)
			}
			if ui.Button(c.Key("clone"), i18n.T("Clone Repository…")).FillWidth().Clicked() {
				openClone(s)
			}
			ui.Spacer(c)
			if ghostButton(c.Key("settings"), "settings", i18n.T("Settings")).Clicked() {
				openSettings()
			}
		})
		ui.Column(c.Key("recent")).Grow(1).MinWidth(0).FillHeight().Padding(24, 16, 12, 16).Children(func() {
			ui.Text(c, i18n.T("Recent Repositories")).FontWeight(600).TextColor(t.TextMuted).Padding(0, 12, 8, 12)
			recent := settings.Get().Recent
			if len(recent) == 0 {
				ui.Column(c).Grow(1).Center().Gap(12).Margin(12).Radius(10).Border(2, t.Border).BorderStyle(ui.BorderDashed).TextColor(t.TextMuted).Children(func() {
					ui.Icon(c, icon("folder-open")).FontSize(28)
					ui.Text(c, i18n.T("Drag a repository folder here to open it"))
					ui.Text(c, i18n.T("or choose Open Repository… or Clone Repository…")).FontSize(t.FontSize - 1)
				})
			} else {
				ui.Scroll(c.Key("recent-list")).Grow(1).Children(func() {
					for ix, path := range recent {
						recentRow(s, c, ix, path)
					}
				})
			}
			muted(c, i18n.T("Tip: drop a folder anywhere on this window to open it.")).Padding(8, 12, 0, 12)
		})
	})
	if dragging {
		ui.Column(c.Key("drop")).Absolute().Top(12).Left(12).Right(12).Bottom(12).Center().Gap(8).Radius(10).
			Border(2, t.Accent).BorderStyle(ui.BorderDashed).Background(t.Background.Alpha(0.92)).PassThrough().Children(func() {
			ui.Icon(c, icon("folder-open")).FontSize(28).TextColor(t.Accent)
			ui.Text(c, i18n.T("Drop to open the repository")).FontSize(t.FontSize + 3).Bold()
		})
	}
}

func recentRow(s *Shell, c *ui.Context, ix int, path string) {
	t := c.Theme()
	info, err := os.Stat(path)
	exists := err == nil && info.IsDir()
	name := filepath.Base(path)
	row := ui.Row(c.Key(path)).PaddingX(12).PaddingY(8).Gap(12).Radius(t.Radius)
	hovered := row.Hovered()
	if hovered {
		row.Background(hoverBG(t))
	}
	if exists {
		row.Cursor(ui.CursorPointer)
		if row.Clicked() {
			openPath(s, path, true)
		}
	}
	row.Children(func() {
		color := laneColor(ix)
		initial := ""
		if r := []rune(name); len(r) > 0 {
			initial = strings.ToUpper(string(r[0]))
		}
		ui.Box(c).Size(32, 32).Radius(t.Radius).Center().Background(color.Alpha(0.18)).Children(func() {
			ui.Text(c, initial).Bold().TextColor(color)
		})
		col := ui.Column(c).Grow(1).Shrink(1).MinWidth(0)
		if !exists {
			col.Opacity(0.5)
		}
		col.Children(func() {
			ui.Text(c, name).FontWeight(600).SingleLine()
			where := tilde(path)
			if !exists {
				where = i18n.Tf("{} (not found)", where)
			}
			muted(c, where).SingleLine()
		})
		remove := iconButton(c.Key("remove"), "x", i18n.T("Remove from Recent"))
		if !hovered {
			remove.Invisible()
		}
		if remove.Clicked() {
			settings.RemoveRecent(path)
		}
	})
}
