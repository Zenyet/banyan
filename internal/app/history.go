package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
)

// showHistory is "Show History" for a file.
func showHistory(ws *Workspace, path string) {
	var revisions []git.FileRevision
	loading, selected := true, -1
	diff := newDiffPane()
	diff.ShowMessage("", i18n.T("Loading history…"))
	width := float32(460)
	var list ui.ListState
	repo := ws.Model.Repo
	choose := func(ix int) {
		if ix >= len(revisions) {
			diff.ShowMessage("", i18n.T("No history"))
			return
		}
		selected = ix
		r := revisions[ix]
		diff.Load(r.Path+"  ("+r.Commit.ShortID()+")", func() (string, error) { return repo.RevisionDiff(r.Commit.ID, r.Path) })
	}
	go func() {
		revs, err := repo.FileHistory(path)
		model.Post(func() {
			loading = false
			if err != nil {
				ws.Model.Fail(err)
				return
			}
			revisions = revs
			choose(0)
		})
	}()
	ws.Shell.Open(&Dialog{Title: i18n.Tf("History for {}", path), Width: 1200, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		framed(c, 640, func() {
			ui.Split(c.Key("history"), &width, func() {
				ui.Column(c).Fill().Children(func() {
					ui.Row(c).Height(28).PaddingX(8).Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
						if loading {
							ui.Spinner(c).Size(12, 12)
							muted(c, i18n.T("Loading…"))
						} else {
							muted(c, i18n.Tf("{} revisions of {}", len(revisions), path)).SingleLine()
						}
					})
					ui.List(c.Key("revisions"), &list, len(revisions), func(ix int) {
						r := revisions[ix]
						row := ui.Row(c.Key(ix)).Height(24).PaddingX(8).Gap(8).Cursor(ui.CursorPointer)
						switch {
						case ix == selected:
							row.Background(selectionBG)
						case row.Hovered():
							row.Background(hoverBG(t))
						}
						if row.Clicked() {
							choose(ix)
						}
						row.ContextMenu(func(menu *ui.Menu) {
							if menu.Item(i18n.T("Annotate at This Revision")).Chosen() {
								annotate(ws, r.Commit.ID, r.Path)
							}
							if menu.Item(i18n.T("Compare with Local")).Chosen() {
								openWorktreeDiff(ws, r.Commit.ID, "", r.Path)
							}
						})
						row.Children(func() {
							ui.Text(c, r.Commit.ShortID()).Width(64).TextColor(t.TextMuted).SingleLine()
							ui.Text(c, r.Commit.Subject).SingleLine().Grow(1).Shrink(1)
							ui.Text(c, r.Commit.Author).Width(90).TextColor(t.TextMuted).SingleLine()
							ui.Text(c, formatTime(r.Commit.Time)).Width(120).TextColor(t.TextMuted).SingleLine()
						})
					}).Grow(1)
				})
			}, func() {
				ui.Column(c).Fill().Children(func() { diff.View(c) })
			}).Fill()
		})
	}})
}

// annotate is "Annotate with Git Blame": each line of path at rev with the
// commit that last changed it, shaded by age.
func annotate(ws *Workspace, rev, path string) {
	var lines []git.BlameLine
	// ages runs from 0 for the oldest commit to 1 for the newest.
	ages := map[string]float32{}
	loading, selected := true, -1
	var list ui.ListState
	repo := ws.Model.Repo
	gen := 0
	var load func(r, p string, at int)
	load = func(r, p string, at int) {
		rev, path = r, p
		loading, selected = true, -1
		gen++
		g := gen
		go func() {
			ls, err := repo.Blame(r, p)
			model.Post(func() {
				if g != gen {
					return
				}
				loading = false
				if err != nil {
					ws.Model.Fail(err)
					return
				}
				type commitTime struct {
					time int64
					id   string
				}
				seen := map[string]bool{}
				var times []commitTime
				for _, l := range ls {
					if !seen[l.ID] {
						seen[l.ID] = true
						times = append(times, commitTime{l.Time, l.ID})
					}
				}
				sort.Slice(times, func(i, j int) bool { return times[i].time < times[j].time })
				n := float32(max(len(times), 2) - 1)
				ages = map[string]float32{}
				for ix, ct := range times {
					ages[ct.id] = float32(ix) / n
				}
				lines = ls
				if at > 0 && at <= len(lines) {
					selected = at - 1
					list.ScrollTo(selected, ui.Center)
				}
			})
		}()
	}
	load(rev, path, 0)
	ws.Shell.Open(&Dialog{Title: i18n.Tf("Annotate {} at {}", path, short(rev)), Width: 1200, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		muted(c, i18n.Tf("{} at {}. Click a line to see its commit.", path, short(rev)))
		framed(c, 600, func() {
			if loading {
				ui.Text(c, i18n.T("Loading…")).Padding(12)
			}
			ui.List(c.Key(rev+path), &list, len(lines), func(ix int) {
				l := lines[ix]
				firstOfRun := ix == 0 || lines[ix-1].ID != l.ID
				sameCommit := selected >= 0 && lines[selected].ID == l.ID
				row := ui.Row(c.Key(ix)).Height(20)
				switch {
				case ix == selected:
					row.Background(selectionBG)
				case sameCommit:
					row.Background(selectionBG.Alpha(0.11))
				}
				if row.Clicked() {
					selected = ix
				}
				row.Children(func() {
					// Newer commits get a stronger tint, like JetBrains'
					// annotation gutter.
					tint := laneColor(0).Alpha(0.06 + 0.3*ages[l.ID])
					ui.Row(c.Key("gutter")).Width(300).FillHeight().PaddingX(8).Gap(8).Background(tint).FontSize(t.FontSize - 1).
						TextColor(t.TextMuted).Tooltip(git.Short(l.ID) + " " + l.Summary).Children(func() {
						if firstOfRun {
							ui.Text(c, git.Short(l.ID)).Width(60).SingleLine()
							ui.Text(c, l.Author).Width(80).SingleLine()
							ui.Text(c, formatTime(l.Time)).Grow(1).SingleLine()
						}
					})
					ui.Text(c, fmt.Sprint(ix+1)).Width(36).PaddingStart(4).FontSize(t.FontSize - 1).TextColor(t.TextMuted)
					ui.Text(c, strings.ReplaceAll(l.Text, "\t", "    ")).Font(monoFont).NoWrap().SingleLine()
				})
			}).Grow(1)
		})
		if selected < 0 || selected >= len(lines) {
			return
		}
		// What can be done with the selected line's commit.
		l := lines[selected]
		ui.Row(c.Key("line-bar")).Gap(8).Children(func() {
			ui.Text(c, git.Short(l.ID)+"  "+l.Summary).SingleLine().Grow(1).Shrink(1)
			muted(c, l.Author+", "+formatTime(l.Time)).SingleLine()
			if button(c.Key("previous"), i18n.T("Annotate Previous Revision")).Disabled(l.Previous == "").Clicked() {
				// Blame the file as it was before the line's commit, at the
				// same line, the way JetBrains steps back through a line's
				// history.
				load(l.Previous, l.PreviousPath, l.OrigLine)
			}
			if button(c.Key("diff"), i18n.T("Show Diff")).Disabled(l.Previous == "").Clicked() {
				openWorktreeDiff(ws, l.Previous, l.ID, l.Path)
			}
			if m := ws.Model; m.HasWeb && button(c.Key("web"), i18n.Tf("Open on {}", m.Web.Name())).Clicked() {
				mygo.Shell.OpenExternal(m.Web.FileURL(l.ID, l.Path, l.OrigLine))
			}
			if button(c.Key("copy"), i18n.T("Copy Revision Number")).Clicked() {
				mygo.Clipboard.WriteText(l.ID)
			}
		})
	}})
}
