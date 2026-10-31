package app

import (
	"slices"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// repoWindow is a window of repositories, one tab each, like the several
// roots of a JetBrains project: open another repository into the window,
// switch between them, or fetch them all at once.
type repoWindow struct {
	shell  *Shell
	tabs   []*Workspace
	active int
}

// repoWindows are the open repository windows, oldest first.
var repoWindows []*repoWindow

func repoWindowOf(s *Shell) *repoWindow {
	for _, rw := range repoWindows {
		if rw.shell == s {
			return rw
		}
	}
	return nil
}

func focusedRepoWindow() *repoWindow { return repoWindowOf(shellOf(mygo.FocusedWindow())) }

// openRepos opens repositories as tabs of the repository window in front
// (or the last one used), unless the settings or newWindow say a window of
// their own; one open already is shown where it is.
func openRepos(repos []*git.Repo, newWindow bool) {
	if len(repos) == 0 {
		return
	}
	if len(repos) == 1 && !newWindow && showExisting(repos[0].Root()) {
		return
	}
	for _, r := range repos {
		settings.AddRecent(r.Root())
	}
	var target *repoWindow
	if !newWindow && !settings.Read(func(s *settings.Settings) bool { return s.OpenInNewWindow }) {
		target = focusedRepoWindow()
		if target == nil && len(repoWindows) > 0 {
			target = repoWindows[len(repoWindows)-1]
		}
	}
	if target == nil {
		target = &repoWindow{}
		repoWindows = append(repoWindows, target)
		target.shell = newWindow_(target)
	}
	for _, r := range repos {
		target.add(r)
	}
	if newWindow || len(target.tabs) == len(repos) {
		target.activate(0)
	}
	target.shell.Win.Show()
	target.shell.Win.Focus()
	closeWelcome()
}

func newWindow_(rw *repoWindow) *Shell {
	// The title bar hides, its traffic lights inset over the view's own,
	// which holds the toolbar, as macOS apps have it.
	s := newWindow(mygo.WindowOptions{Title: "Banyan", Width: 1440, Height: 920, MinWidth: 720, MinHeight: 480,
		TitleBarStyle: mygo.TitleBarHiddenInset, TitleBarHeight: titleBarHeight},
		func(s *Shell, c *ui.Context) { rw.view(c) })
	s.onClosed = func() {
		for _, ws := range rw.tabs {
			ws.Close()
		}
		repoWindows = slices.DeleteFunc(repoWindows, func(w *repoWindow) bool { return w == rw })
	}
	return s
}

// showExisting switches to the tab showing root, in whichever window has
// it.
func showExisting(root string) bool {
	for _, rw := range repoWindows {
		if ix := rw.indexOf(root); ix >= 0 {
			rw.activate(ix)
			rw.shell.Win.Show()
			rw.shell.Win.Focus()
			return true
		}
	}
	return false
}

// siblingModels are the repositories in the other tabs of the window
// showing m.
func siblingModels(m *model.Model) []*model.Model {
	for _, rw := range repoWindows {
		var others []*model.Model
		found := false
		for _, ws := range rw.tabs {
			if ws.Model == m {
				found = true
			} else {
				others = append(others, ws.Model)
			}
		}
		if found {
			return others
		}
	}
	return nil
}

func (rw *repoWindow) indexOf(root string) int {
	return slices.IndexFunc(rw.tabs, func(ws *Workspace) bool { return ws.Model.Repo.Root() == root })
}

// add opens repo in a new tab and shows it, or shows its tab.
func (rw *repoWindow) add(repo *git.Repo) {
	if ix := rw.indexOf(repo.Root()); ix >= 0 {
		rw.activate(ix)
		return
	}
	rw.tabs = append(rw.tabs, newWorkspace(rw.shell, model.New(repo)))
	rw.activate(len(rw.tabs) - 1)
}

func (rw *repoWindow) activate(ix int) {
	if ix < 0 || ix >= len(rw.tabs) {
		return
	}
	if ix != rw.active {
		rw.tabs[ix].Model.RefreshIfStale()
	}
	rw.active = ix
	rw.shell.Win.SetTitle(rw.tabs[ix].Model.Repo.Name() + " – Banyan")
	rw.remember()
	rw.shell.Win.Invalidate()
}

func (rw *repoWindow) close(ix int) {
	if ix < 0 || ix >= len(rw.tabs) {
		return
	}
	if len(rw.tabs) == 1 {
		rw.shell.Win.Close()
		return
	}
	rw.tabs[ix].Close()
	rw.tabs = slices.Delete(rw.tabs, ix, ix+1)
	active := rw.active
	if active > ix || active == len(rw.tabs) {
		active = max(active-1, 0)
	}
	rw.active = -1
	rw.activate(active)
}

func (rw *repoWindow) step(delta int) {
	if n := len(rw.tabs); n > 1 {
		rw.activate(((rw.active+delta)%n + n) % n)
	}
}

func (rw *repoWindow) fetchAll() {
	for _, ws := range rw.tabs {
		m := ws.Model
		model.Run(m, i18n.Tf("Fetch {}", m.Repo.Name()), (*git.Repo).Fetch)
	}
}

// updateAll runs Update Project in every tab whose branch tracks a remote
// one, the way Settings say to bring changes in.
func (rw *repoWindow) updateAll() {
	method := settings.Read(func(s *settings.Settings) git.UpdateMethod { return s.UpdateMethod })
	for _, ws := range rw.tabs {
		m := ws.Model
		if !slices.ContainsFunc(m.Branches, func(b git.Branch) bool { return b.IsHead && b.Upstream != "" }) {
			continue
		}
		model.Run(m, i18n.Tf("Update {}", m.Repo.Name()), func(r *git.Repo) (string, error) { return r.UpdateProject(method) })
	}
}

func (rw *repoWindow) closeOthers(keep int) {
	if keep < 0 || keep >= len(rw.tabs) {
		return
	}
	for ix, ws := range rw.tabs {
		if ix != keep {
			ws.Close()
		}
	}
	rw.tabs = []*Workspace{rw.tabs[keep]}
	rw.active = -1
	rw.activate(0)
}

// moveToNewWindow moves tab ix to a window of its own.
func (rw *repoWindow) moveToNewWindow(ix int) {
	if len(rw.tabs) < 2 {
		return
	}
	root := rw.tabs[ix].Model.Repo.Root()
	rw.close(ix)
	if repo, err := git.Open(root); err == nil {
		openRepos([]*git.Repo{repo}, true)
	}
}

// moveTab puts the tab at from where to is.
func (rw *repoWindow) moveTab(from, to int) {
	if from == to || from < 0 || to < 0 || from >= len(rw.tabs) || to >= len(rw.tabs) {
		return
	}
	active := rw.tabs[rw.active]
	tab := rw.tabs[from]
	rw.tabs = slices.Insert(slices.Delete(rw.tabs, from, from+1), to, tab)
	rw.active = slices.Index(rw.tabs, active)
	rw.remember()
}

// remember keeps the window's repositories, to open again at the next
// launch, the one shown first.
func (rw *repoWindow) remember() {
	var roots []string
	for _, ws := range rw.tabs {
		roots = append(roots, ws.Model.Repo.Root())
	}
	if rw.active >= 0 && rw.active < len(roots) {
		active := roots[rw.active]
		roots = append([]string{active}, slices.Delete(roots, rw.active, rw.active+1)...)
	}
	if !slices.Equal(settings.Read(func(s *settings.Settings) []string { return s.Session }), roots) {
		settings.Update(func(s *settings.Settings) { s.Session = roots })
	}
}

func (rw *repoWindow) view(c *ui.Context) {
	t := c.Theme()
	if len(rw.tabs) == 0 {
		return
	}
	if files := c.Root().DroppedFiles(); len(files) > 0 {
		// A folder dropped on the window opens as another repository.
		openPath(rw.shell, files[0], false)
	}
	ws := rw.tabs[rw.active]
	ws.header(c)
	if len(rw.tabs) > 1 {
		ui.Row(c.Key("repo-tabs")).Height(36).PaddingX(12).Gap(4).Background(chromeBG(t)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
			ClipX().Children(func() {
			for ix := range rw.tabs {
				rw.tab(c, ix)
			}
			if iconButton(c.Key("open-tab"), "plus", i18n.T("Open Repository…")).Radius(14).Clicked() {
				promptOpenRepo(rw.shell)
			}
		})
	}
	ui.Column(c.Key(ws)).Grow(1).MinHeight(0).Children(func() { ws.View(c) })
}

// draggedTab is a tab being dragged to another place in the tab bar.
type draggedTab struct{ ix int }

func (rw *repoWindow) tab(c *ui.Context, ix int) {
	t := c.Theme()
	m := rw.tabs[ix].Model
	active := ix == rw.active
	tab := ui.Row(c.Key(m)).Height(28).Radius(14).PaddingStart(12).PaddingEnd(3).Gap(6).Shrink(0).
		Drag(draggedTab{ix}).Tooltip(tilde(m.Repo.Root())).Cursor(ui.CursorPointer)
	if active {
		tab.Material(glass.Glass{})
	} else {
		tab.TextColor(t.TextMuted)
		if tab.Hovered() {
			tab.Background(t.Text.Alpha(0.06))
		}
	}
	if dropped, ok := ui.Drop[draggedTab](tab); ok {
		rw.moveTab(dropped.ix, ix)
	}
	if tab.Clicked() {
		rw.activate(ix)
	}
	tab.ContextMenu(func(menu *ui.Menu) {
		if menu.Item(i18n.T("Close Repository")).Chosen() {
			rw.close(ix)
		}
		if menu.Item(i18n.T("Close Other Repositories")).Chosen() {
			rw.closeOthers(ix)
		}
		if menu.Item(i18n.T("Open in New Window")).Chosen() {
			rw.moveToNewWindow(ix)
		}
		menu.Separator()
		if menu.Item(i18n.T("Copy Path")).Chosen() {
			mygo.Clipboard.WriteText(m.Repo.Root())
		}
		if menu.Item(revealLabel()).Chosen() {
			mygo.Shell.ShowItemInFolder(m.Repo.Root())
		}
	})
	tab.Children(func() {
		ui.Icon(c, icon("folder-git-2")).FontSize(t.FontSize - 1)
		ui.Text(c, m.Repo.Name()).SingleLine()
		if m.CurrentBranch != "" {
			muted(c, m.CurrentBranch).SingleLine()
		}
		if n := len(m.Status); n > 0 {
			muted(c, "· "+itoa(n))
		}
		if m.Busy != "" {
			ui.Text(c, "●").FontSize(t.FontSize - 2).TextColor(t.Accent)
		}
		if iconButton(c.Key("close"), "x", i18n.T("Close Repository")).Radius(11).Padding(3).Clicked() {
			rw.close(ix)
		}
	})
}
