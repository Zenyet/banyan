package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
)

// What a stopped merge, rebase, cherry-pick or revert needs from the user:
// the banner above the tabs, and JetBrains' "Conflicts" dialog listing the
// conflicted files with Accept Yours / Accept Theirs / Merge… for each.

// abortOperation asks, then aborts the stopped operation.
func abortOperation(s *Shell, m *model.Model) {
	op := m.Operation
	if op == nil {
		return
	}
	state := op.State
	noun := strings.ToLower(state.Noun())
	s.Confirm(i18n.Tf("Abort {}", state.Noun()),
		i18n.Tf("{}. Abort it and go back to how things were before the {} started? Conflicts resolved so far are lost.", op.Title, noun),
		i18n.Tf("Abort {}", state.Noun()), func() {
			model.Run(m, i18n.Tf("Abort {}", noun), func(r *git.Repo) (string, error) { return r.Abort(state) })
		})
}

// proceed commits the merge, or continues the rebase, cherry-pick or
// revert.
func proceed(m *model.Model) {
	state := m.State
	model.Run(m, state.ContinueLabel(), func(r *git.Repo) (string, error) { return r.ContinueOp(state) })
}

// operationBanner is the bar above the tabs while an operation is stopped.
func operationBanner(c *ui.Context, ws *Workspace) {
	m := ws.Model
	op := m.Operation
	if op == nil {
		return
	}
	t := c.Theme()
	conflicts := len(m.Conflicts())
	busy := m.Busy != ""
	name, accent := "circle-check", t.Success
	if conflicts > 0 {
		name, accent = "triangle-alert", t.Warning
	}
	detail := i18n.T("All conflicts resolved")
	switch {
	case conflicts == 1:
		detail = i18n.T("1 file has conflicts")
	case conflicts > 1:
		detail = i18n.Tf("{} files have conflicts", conflicts)
	}
	ui.Row(c.Key("banner")).Height(36).PaddingX(12).Gap(8).Background(accent.Alpha(0.12)).BorderWidth(0, 0, 1, 0).BorderColor(accent.Alpha(0.4)).Children(func() {
		ui.Icon(c, icon(name)).TextColor(accent)
		ui.Text(c, op.Title).FontWeight(600).SingleLine().Shrink(1)
		muted(c, detail).SingleLine()
		ui.Spacer(c)
		if conflicts > 0 {
			if primaryButton(c.Key("resolve"), i18n.T("Resolve Conflicts…")).Clicked() {
				openConflicts(ws.Shell, m)
			}
		} else if primaryButton(c.Key("continue"), op.State.ContinueLabel()).Disabled(busy).Clicked() {
			proceed(m)
		}
		if button(c.Key("abort"), i18n.Tf("Abort {}", op.State.Noun())).TextColor(t.Danger).Disabled(busy).Clicked() {
			abortOperation(ws.Shell, m)
		}
	})
}

func openConflicts(s *Shell, m *model.Model) {
	selected := ""
	s.Open(&Dialog{Title: i18n.T("Conflicts"), Width: 760, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		op, conflicts, busy := m.Operation, m.Conflicts(), m.Busy != ""
		if !slices.ContainsFunc(conflicts, func(e git.StatusEntry) bool { return e.Path == selected }) {
			selected = ""
			if len(conflicts) > 0 {
				selected = conflicts[0].Path
			}
		}
		intro := i18n.T("Conflicts")
		ours, theirs := "yours", "theirs"
		if op != nil {
			intro, ours, theirs = op.Title, op.Ours, op.Theirs
		}
		footer := func(extra func()) {
			ui.Row(c.Key("footer")).Gap(8).Children(func() {
				if op != nil && button(c.Key("abort"), i18n.Tf("Abort {}", op.State.Noun())).TextColor(t.Danger).Disabled(busy).Clicked() {
					abortOperation(s, m)
				}
				ui.Spacer(c)
				if button(c.Key("close"), i18n.T("Close")).Clicked() {
					d.Close()
				}
				if extra != nil {
					extra()
				}
			})
		}
		if len(conflicts) == 0 {
			ui.Row(c).Gap(8).Children(func() {
				ui.Icon(c, icon("circle-check")).TextColor(t.Success)
				ui.Text(c, i18n.Tf("All conflicts are resolved. {}.", intro))
			})
			if op != nil {
				muted(c, i18n.T("Review the result in the Commit tab, then finish the operation."))
			}
			footer(func() {
				if op != nil && primaryButton(c.Key("continue"), op.State.ContinueLabel()).Disabled(busy).Clicked() {
					d.Close()
					proceed(m)
				}
			})
			return
		}
		var entry *git.StatusEntry
		for i := range conflicts {
			if conflicts[i].Path == selected {
				entry = &conflicts[i]
			}
		}
		mergeable := entry != nil && entry.CanMergeText()
		ui.Text(c, i18n.Tf("{}. Resolve each file to continue:", intro))
		ui.Row(c).AlignItems(ui.Start).Gap(12).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Border(1, t.Border).Radius(t.Radius).Children(func() {
				ui.Row(c).Height(26).PaddingX(8).Gap(8).FontSize(t.FontSize-1).TextColor(t.TextMuted).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
					ui.Box(c).Width(12)
					ui.Text(c, i18n.T("Name")).Grow(1)
					ui.Text(c, i18n.Tf("Yours ({})", ours)).Width(130).SingleLine()
					ui.Text(c, i18n.Tf("Theirs ({})", theirs)).Width(130).SingleLine()
				})
				ui.Scroll(c.Key("files")).Height(220).PaddingY(4).Children(func() {
					for _, e := range conflicts {
						o, th, _ := e.ConflictSides()
						row := ui.Row(c.Key(e.Path)).Height(26).PaddingX(8).Gap(8).Cursor(ui.CursorPointer)
						switch {
						case e.Path == selected:
							row.Background(selectionBG)
						case row.Hovered():
							row.Background(hoverBG(t))
						}
						if row.Clicked() {
							selected = e.Path
							if e.CanMergeText() && row.DoubleClicked() {
								openMergeToolIn(s, m, e.Path)
							}
						}
						row.Children(func() {
							ui.Image(c, fileIcon(e.Path)).Size(t.FontSize+1, t.FontSize+1)
							ui.Text(c, e.Path).SingleLine().Grow(1).Shrink(1)
							ui.Text(c, o.Label()).Width(130).TextColor(t.TextMuted)
							ui.Text(c, th.Label()).Width(130).TextColor(t.TextMuted)
						})
					}
				})
			})
			ui.Column(c).Width(130).Gap(8).Children(func() {
				resolve := func(ours bool) {
					path, side := selected, ours
					label := i18n.Tf("Accept theirs for {}", path)
					if ours {
						label = i18n.Tf("Accept yours for {}", path)
					}
					model.Run(m, label, func(r *git.Repo) (string, error) { return r.ResolveConflict(path, &side) })
				}
				if button(c.Key("yours"), i18n.T("Accept Yours")).FillWidth().Disabled(entry == nil || busy).Clicked() {
					resolve(true)
				}
				if button(c.Key("theirs"), i18n.T("Accept Theirs")).FillWidth().Disabled(entry == nil || busy).Clicked() {
					resolve(false)
				}
				if primaryButton(c.Key("merge"), i18n.T("Merge…")).FillWidth().Disabled(!mergeable || busy).Clicked() {
					openMergeToolIn(s, m, selected)
				}
			})
		})
		if !mergeable && entry != nil {
			muted(c, i18n.T("One side deleted this file, so it can only be resolved by accepting a side."))
		}
		footer(nil)
	}})
}

func openMergeTool(ws *Workspace, path string) { openMergeToolIn(ws.Shell, ws.Model, path) }
