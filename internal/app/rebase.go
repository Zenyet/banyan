package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
)

// openRebase is "Interactively Rebase from Here": reorder, reword, squash,
// fixup or drop commits, like JetBrains' rebase editor.
func openRebase(ws *Workspace, from string) {
	repo := ws.Model.Repo
	go func() {
		commits, err := repo.RebaseCommits(from)
		model.Post(func() {
			if err != nil {
				ws.Model.Fail(errorf("%s: %v", i18n.T("Cannot rebase"), err))
				return
			}
			showRebase(ws, from, commits)
		})
	}()
}

type rebaseEntry struct {
	commit git.Commit
	action git.RebaseAction
}

func showRebase(ws *Workspace, from string, commits []git.Commit) {
	onto := i18n.T("the root")
	if len(commits) > 0 && len(commits[0].Parents) > 0 {
		onto = short(commits[0].Parents[0])
	}
	var entries []rebaseEntry
	for _, c := range commits {
		entries = append(entries, rebaseEntry{c, git.RebaseAction{Kind: git.Pick}})
	}
	selected := 0
	move := func(delta int) {
		target := selected + delta
		if target >= 0 && target < len(entries) {
			entries[selected], entries[target] = entries[target], entries[selected]
			selected = target
		}
	}
	validation := func() string {
		for _, e := range entries {
			if e.action.Kind == git.Drop {
				continue
			}
			if e.action.Kind == git.Squash || e.action.Kind == git.Fixup {
				return i18n.T("The first commit cannot be squashed or fixed up")
			}
			return ""
		}
		return ""
	}
	// unchanged is the original order, oldest first, with every commit
	// picked.
	unchanged := func() bool {
		for ix, e := range entries {
			if e.action.Kind != git.Pick || ix > 0 && (len(e.commit.Parents) == 0 || e.commit.Parents[0] != entries[ix-1].commit.ID) {
				return false
			}
		}
		return true
	}
	actions := []struct {
		kind  git.RebaseActionKind
		label string
		color string
	}{
		{git.Pick, i18n.T("Pick"), ""},
		{git.Squash, i18n.T("Squash"), "#9a6ad6"},
		{git.Fixup, i18n.T("Fixup"), "#9a6ad6"},
		{git.Edit, i18n.T("Edit"), "#d98c2b"},
		{git.Drop, i18n.T("Drop"), ""},
	}
	ws.Shell.Open(&Dialog{Title: i18n.T("Interactive Rebase"), Width: 860, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		muted(c, i18n.Tf("{} commits, oldest first. Rebasing onto {}.", len(entries), onto))
		ui.Scroll(c.Key("entries")).Height(360).Border(1, t.Border).Radius(t.Radius).Children(func() {
			for ix := range entries {
				e := &entries[ix]
				row := ui.Row(c.Key(e.commit.ID)).Height(30).PaddingX(8).Gap(8)
				switch {
				case ix == selected:
					row.Background(selectionBG)
				case row.Hovered():
					row.Background(hoverBG(t))
				}
				if row.Clicked() {
					selected = ix
				}
				row.Children(func() {
					label, color := i18n.T("Pick"), t.Text
					for _, a := range actions {
						if a.kind == e.action.Kind {
							label = a.label
							if a.color != "" {
								color = ui.Hex(a.color)
							}
						}
					}
					switch e.action.Kind {
					case git.Reword:
						label, color = i18n.T("Reword"), ui.Hex("#4d8ee8")
					case git.Drop:
						color = t.Danger
					}
					filterButton(c.Key("action"), label).Width(96).TextColor(color).Menu(func(menu *ui.Menu) {
						for _, a := range actions {
							if menu.Item(a.label).Chosen() {
								e.action = git.RebaseAction{Kind: a.kind}
								selected = ix
							}
						}
						if menu.Item(i18n.T("Reword...")).Chosen() {
							original := e.commit.Subject
							ws.Shell.PromptText(i18n.T("Reword Commit"), i18n.T("Reword"), original, func(message string) {
								e.action = git.RebaseAction{Kind: git.Reword, Message: message}
								selected = ix
							})
						}
					})
					ui.Text(c, short(e.commit.ID)).Width(70).TextColor(t.TextMuted)
					subject := e.commit.Subject
					if e.action.Kind == git.Reword {
						subject, _, _ = strings.Cut(e.action.Message, "\n")
					}
					s := ui.Text(c, subject).SingleLine().Grow(1).Shrink(1)
					switch e.action.Kind {
					case git.Drop:
						s.Strikethrough().TextColor(t.TextMuted)
					case git.Squash, git.Fixup:
						s.PaddingStart(16)
					}
					muted(c, e.commit.Author).SingleLine()
				})
			}
		})
		problem := validation()
		ui.Row(c).Gap(8).Children(func() {
			if iconButton(c.Key("up"), "arrow-up", i18n.T("Move Up")).Clicked() {
				move(-1)
			}
			if iconButton(c.Key("down"), "arrow-down", i18n.T("Move Down")).Clicked() {
				move(1)
			}
			if problem != "" {
				ui.Text(c, problem).TextColor(t.Danger)
			}
			ui.Spacer(c)
			if button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				d.Close()
			}
			if primaryButton(c.Key("start"), i18n.T("Start Rebasing")).Disabled(problem != "" || unchanged()).Clicked() {
				if refuseIfProtected(ws, from) {
					return
				}
				var plan []git.RebaseStep
				for _, e := range entries {
					plan = append(plan, git.RebaseStep{ID: e.commit.ID, Action: e.action})
				}
				d.Close()
				ws.run(i18n.T("Interactive rebase"), func(r *git.Repo) (string, error) { return r.InteractiveRebase(from, plan) })
			}
		})
	}})
}
