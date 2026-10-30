package app

import (
	"github.com/egoist/mygo"

	"github.com/Zenyet/banyan/internal/i18n"
)

// setMenu sets the menu bar, again when the language or a shortcut
// changes.
func setMenu() {
	item := func(id string) *mygo.MenuItem {
		c := commandByID(id)
		return &mygo.MenuItem{
			Label:       c.Label,
			Accelerator: accelerator(c.Keys()),
			Click:       func(_ *mygo.MenuItem, w *mygo.Window) { runCommand(id, w) },
		}
	}
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			item("settings"),
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			item("quit"),
		}},
		{Label: i18n.T("File"), Submenu: []*mygo.MenuItem{
			item("open"),
			{Label: i18n.T("Clone Repository…"), Click: func(_ *mygo.MenuItem, w *mygo.Window) { openClone(targetShell(w)) }},
			mygo.Separator(),
			item("close_window"),
		}},
		{Role: mygo.RoleEditMenu},
		{Label: i18n.T("Git"), Submenu: []*mygo.MenuItem{
			item("find_action"),
			mygo.Separator(),
			item("commit"),
			item("push"),
			item("update"),
			item("fetch"),
			item("fetch_all"),
			item("update_all"),
			item("apply_patch"),
			item("worktrees"),
			item("submodules"),
			mygo.Separator(),
			item("show_log"),
			item("search_log"),
			item("go_to"),
			item("show_commit"),
			item("show_console"),
			item("refresh"),
		}},
		{Label: i18n.T("Window"), Submenu: []*mygo.MenuItem{
			item("next_repo"),
			item("previous_repo"),
			mygo.Separator(),
			{Role: mygo.RoleMinimize},
			{Role: mygo.RoleZoom},
		}},
	}))
}

// targetShell is the window a menu item acts on: the one given, else the
// one in front.
func targetShell(w *mygo.Window) *Shell {
	if s := shellOf(w); s != nil {
		return s
	}
	if s := shellOf(mygo.FocusedWindow()); s != nil {
		return s
	}
	return nil
}

// runCommand runs the command id in window w, or the one in front.
func runCommand(id string, w *mygo.Window) {
	s := targetShell(w)
	switch id {
	case "open":
		promptOpenRepo(s)
		return
	case "settings":
		openSettings()
		return
	case "quit":
		quit()
		return
	case "close_window":
		if s == nil {
			return
		}
		if rw := repoWindowOf(s); rw != nil && len(rw.tabs) > 1 {
			rw.close(rw.active)
			return
		}
		s.Win.Close()
		return
	}
	rw := repoWindowOf(s)
	if rw == nil || len(rw.tabs) == 0 {
		return
	}
	switch id {
	case "next_repo":
		rw.step(1)
	case "previous_repo":
		rw.step(-1)
	case "fetch_all":
		rw.fetchAll()
	case "update_all":
		rw.updateAll()
	default:
		rw.tabs[rw.active].Command(id)
	}
	s.Win.Invalidate()
}
