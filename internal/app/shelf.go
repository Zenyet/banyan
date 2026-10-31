package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
)

// ShelfView is the Shelf tab: changes put aside with Shelve, like
// JetBrains' Shelf. Each shelf lists its files with their diffs and can be
// unshelved, all of it or one file at a time, back into the changelist it
// came from.
type ShelfView struct {
	ws *Workspace
	// selected is the shelf chosen, by id, and file the file of it whose
	// diff shows.
	selected, file string
	shown          string
	diff           *DiffPane
	width          float32
}

func (sv *ShelfView) shelf(id string) (git.Shelf, bool) {
	ix := slices.IndexFunc(sv.ws.Model.Shelves, func(s git.Shelf) bool { return s.ID == id })
	if ix < 0 {
		return git.Shelf{}, false
	}
	return sv.ws.Model.Shelves[ix], true
}

// sync keeps the choice on a shelf and file that still exist.
func (sv *ShelfView) sync() {
	shelves := sv.ws.Model.Shelves
	shelf, ok := sv.shelf(sv.selected)
	if !ok && len(shelves) > 0 {
		shelf, ok = shelves[0], true
	}
	if !ok {
		sv.selected, sv.file = "", ""
	} else {
		sv.selected = shelf.ID
		if !slices.Contains(shelf.Files, sv.file) {
			sv.file = ""
			if len(shelf.Files) > 0 {
				sv.file = shelf.Files[0]
			}
		}
	}
	key := sv.selected + "\x00" + sv.file
	if key == sv.shown {
		return
	}
	sv.shown = key
	if sv.selected == "" || sv.file == "" {
		message := i18n.T("Select a file")
		if len(shelves) == 0 {
			message = i18n.T("Nothing shelved")
		}
		sv.diff.ShowMessage("", message)
		return
	}
	repo, id, file := sv.ws.Model.Repo, sv.selected, sv.file
	sv.diff.Load(file, func() (string, error) { return repo.ShelfFilePatch(id, file) })
}

func (sv *ShelfView) View(c *ui.Context) {
	t := c.Theme()
	if sv.diff == nil {
		sv.diff = newDiffPane()
		sv.diff.WindowTitle = func() string { return i18n.Tf("Shelf – {}", sv.ws.Model.Repo.Name()) }
		sv.width = 420
	}
	sv.sync()
	ws := sv.ws
	list := func() {
		ui.Column(c).Fill().Background(t.Surface).Children(func() {
			panelHeader(c, i18n.T("Shelf"))
			ui.Row(c.Key("shelf-toolbar")).Height(36).PaddingX(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				b := ghostButton(c.Key("unshelve"), "undo-2", i18n.T("Unshelve")).Tooltip(i18n.T("Bring the selected shelf back into its changelist")).Disabled(sv.selected == "")
				if b.Clicked() {
					ws.Model.Unshelve(sv.selected, nil)
				}
			})
			if len(ws.Model.Shelves) == 0 {
				ui.Column(c).Grow(1).Center().Gap(8).Padding(16).TextColor(t.TextMuted).Children(func() {
					ui.Icon(c, icon("inbox")).FontSize(28)
					ui.Text(c, i18n.T("Nothing shelved"))
					ui.Text(c, i18n.T("Shelve changes from the Commit tab to put them aside and bring them back later.")).FontSize(t.FontSize - 1).TextAlign(ui.Center)
				})
				return
			}
			ui.Scroll(c.Key("shelves")).Grow(1).PaddingY(4).Children(func() {
				for _, shelf := range ws.Model.Shelves {
					sv.shelfRows(c, shelf)
				}
			})
		})
	}
	// The diff in a window of its own leaves the shelves the whole width.
	if sv.diff.Detached() {
		ui.Column(c.Key("shelf")).Fill().Children(list)
		return
	}
	ui.Split(c.Key("shelf"), &sv.width, list, func() {
		ui.Column(c).Fill().Children(func() { sv.diff.View(c) })
	}).Fill()
}

func (sv *ShelfView) shelfRows(c *ui.Context, shelf git.Shelf) {
	t := c.Theme()
	ws := sv.ws
	selected := sv.selected == shelf.ID
	row := ui.Row(c.Key(shelf.ID)).Height(40).PaddingX(8).Gap(8).Cursor(ui.CursorPointer)
	if row.Hovered() {
		row.Background(hoverBG(t))
	}
	if row.Clicked() {
		sv.selected, sv.file = shelf.ID, ""
	}
	row.ContextMenu(func(menu *ui.Menu) {
		if menu.Item(i18n.T("Unshelve")).Chosen() {
			ws.Model.Unshelve(shelf.ID, nil)
		}
		if menu.Item(i18n.T("Rename...")).Chosen() {
			ws.Shell.PromptText(i18n.T("Rename Shelf"), i18n.T("Rename"), shelf.Name, func(name string) {
				ws.run(i18n.T("Rename shelf"), func(r *git.Repo) (string, error) { return "", r.RenameShelf(shelf.ID, name) })
			})
		}
		menu.Separator()
		if menu.Item(i18n.T("Delete...")).Chosen() {
			ws.Shell.Confirm(i18n.T("Delete Shelf"), i18n.Tf("Delete the shelf '{}'? Its changes are lost.", shelf.Name), i18n.T("Delete"), func() {
				ws.run(i18n.T("Delete shelf"), func(r *git.Repo) (string, error) { return "", r.DeleteShelf(shelf.ID) })
			})
		}
	})
	row.Children(func() {
		chevron := "chevron-right"
		if selected {
			chevron = "chevron-down"
		}
		ui.Icon(c, icon(chevron)).FontSize(t.FontSize - 1)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, shelf.Name).FontWeight(600).SingleLine()
			muted(c, i18n.Tf("{} · from {} · {}", fileCount(len(shelf.Files)), shelf.Changelist, formatTime(shelf.Time))).SingleLine()
		})
	})
	if !selected {
		return
	}
	for _, file := range shelf.Files {
		dir, name := "", file
		if ix := strings.LastIndexByte(file, '/'); ix >= 0 {
			dir, name = file[:ix], file[ix+1:]
		}
		fr := ui.Row(c.Key(shelf.ID + "\x00" + file)).Height(24).PaddingStart(28).PaddingEnd(8).Gap(6)
		switch {
		case sv.file == file:
			fr.Background(selectionBG)
		case fr.Hovered():
			fr.Background(hoverBG(t))
		}
		if fr.Clicked() {
			sv.file = file
		}
		fr.ContextMenu(func(menu *ui.Menu) {
			if menu.Item(i18n.T("Unshelve This File")).Chosen() {
				ws.Model.Unshelve(shelf.ID, []string{file})
			}
		})
		fr.Children(func() {
			ui.Image(c, fileIcon(file)).Size(t.FontSize+1, t.FontSize+1)
			ui.Text(c, name).SingleLine()
			muted(c, dir).SingleLine().Shrink(1)
		})
	}
}
