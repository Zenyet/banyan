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
	s := newWindow(withSidebar(mygo.WindowOptions{Title: i18n.T("Welcome to Banyan"), Width: 860, Height: 540}), welcomeView)
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
		sidebar(c, 280, func() {
			ui.Column(c.Key("actions")).Grow(1).Padding(0, 24, 24, 24).Gap(10).Children(func() {
				ui.Column(c).AlignItems(ui.Center).Gap(4).Padding(0, 0, 20, 0).Children(func() {
					if logo != nil {
						ui.Image(c, logo).Size(88, 88)
					}
					ui.Text(c, i18n.T("Banyan")).FontSize(t.FontSize + 9).Bold()
					muted(c, i18n.Tf("Version {}", Version))
				})
				// Glass would show nothing through over the system's material,
				// which the window draws under the view: plain capsules here.
				open := ui.ButtonBase(c.Key("open")).FillWidth().Height(36).Radius(18).Center().Background(t.Accent).
					Label(i18n.T("Open Repository…"))
				if open.Hovered() {
					open.Background(t.AccentHover)
				}
				open.Children(func() {
					ui.Text(c, i18n.T("Open Repository…")).FontWeight(600).TextColor(t.AccentText)
				})
				if open.Clicked() {
					promptOpenRepo(s)
				}
				clone := selectedPill(ui.ButtonBase(c.Key("clone")).FillWidth().Height(36).Radius(18).Center(), t).
					Label(i18n.T("Clone Repository…"))
				clone.Children(func() { ui.Text(c, i18n.T("Clone Repository…")).FontWeight(500) })
				if clone.Clicked() {
					openClone(s)
				}
				ui.Spacer(c)
				if ghostButton(c.Key("settings"), "settings", i18n.T("Settings")).Radius(14).Clicked() {
					openSettings()
				}
			})
		})
		ui.Column(c.Key("recent")).Grow(1).MinWidth(0).FillHeight().Background(t.Background).Children(func() {
			paneTitle(c, i18n.T("Recent Repositories"))
			recent := settings.Get().Recent
			ui.Column(c).Grow(1).MinHeight(0).Padding(0, 16, 12, 16).Children(func() {
				if len(recent) == 0 {
					ui.Column(c).Grow(1).Center().Gap(12).Margin(12).Radius(14).Border(2, t.Border).BorderStyle(ui.BorderDashed).TextColor(t.TextMuted).Children(func() {
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
	})
	if dragging {
		ui.Column(c.Key("drop")).Absolute().Top(12).Left(12).Right(12).Bottom(12).Center().Gap(8).Radius(16).
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
	row := ui.Row(c.Key(path)).PaddingX(12).PaddingY(8).Gap(12).Radius(10)
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
		ui.Box(c).Size(34, 34).Radius(9).Center().Background(color.Alpha(0.18)).Children(func() {
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
