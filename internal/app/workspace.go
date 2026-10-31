package app

import (
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

// Workspace is one repository's tab: the toolbar with the branch popup and
// VCS operations, and the Log, Commit, Shelf and Console tabs.
type Workspace struct {
	Shell   *Shell
	Model   *model.Model
	log     *LogView
	changes *ChangesView
	shelf   *ShelfView
	console *ConsoleView
	tab     int
	off     func()
}

const (
	tabLog = iota
	tabCommit
	tabShelf
	tabConsole
)

func newWorkspace(s *Shell, m *model.Model) *Workspace {
	ws := &Workspace{Shell: s, Model: m}
	ws.log = newLogView(ws)
	ws.changes = newChangesView(ws)
	ws.shelf = &ShelfView{ws: ws}
	ws.console = &ConsoleView{ws: ws}
	ws.off = m.Subscribe(func(e model.Event) {
		s.Report(m, e)
		ws.log.modelChanged(e)
		ws.changes.modelChanged(e)
	})
	return ws
}

// Close stops the tab's background work.
func (ws *Workspace) Close() {
	ws.off()
	ws.Model.Close()
	// Its diffs in windows of their own go with it.
	ws.log.diff.closeWindow()
	ws.changes.diff.closeWindow()
	if ws.shelf.diff != nil {
		ws.shelf.diff.closeWindow()
	}
}

// run runs a git operation of the repository.
func (ws *Workspace) run(label string, op func(*git.Repo) (string, error)) {
	model.Run(ws.Model, label, op)
}

// Command runs a command of the keymap in this repository.
func (ws *Workspace) Command(id string) {
	switch id {
	case "find_action":
		openPalette(ws)
	case "commit":
		ws.tab = tabCommit
		ws.changes.focusMessage()
	case "push":
		openPush(ws)
	case "update":
		openUpdate(ws)
	case "fetch":
		ws.run(i18n.T("Fetch"), (*git.Repo).Fetch)
	case "apply_patch":
		applyPatch(ws)
	case "worktrees":
		openWorktrees(ws, "")
	case "submodules":
		openSubmodules(ws)
	case "show_log":
		ws.tab = tabLog
	case "show_commit":
		ws.tab = tabCommit
	case "show_console":
		ws.tab = tabConsole
	case "search_log":
		ws.tab = tabLog
		ws.log.focusSearch()
	case "go_to":
		ws.tab = tabLog
		ws.Shell.PromptText(i18n.T("Go to Hash, Branch or Tag"), i18n.T("Go"), "", ws.log.goTo)
	case "refresh":
		ws.Model.Refresh()
		ws.log.reload()
	}
}

func (ws *Workspace) View(c *ui.Context) {
	// The commands of the keymap that have no menu item of their own work
	// through the menus; Escape and the like are the views'.
	if files := c.Root().DroppedFiles(); len(files) > 0 {
		if repo, err := git.Open(files[0]); err != nil || repo.Root() != ws.Model.Repo.Root() {
			openPath(ws.Shell, files[0], false)
		}
	}
	operationBanner(c, ws)
	ui.Column(c.Key("content")).Grow(1).MinHeight(0).Children(func() {
		switch ws.tab {
		case tabLog:
			ws.log.View(c)
		case tabCommit:
			ws.changes.View(c)
		case tabShelf:
			ws.shelf.View(c)
		default:
			ws.console.View(c)
		}
	})
	ws.statusBar(c)
}

func (ws *Workspace) statusBar(c *ui.Context) {
	t := c.Theme()
	m := ws.Model
	ui.Row(c.Key("status")).Height(26).PaddingX(14).Gap(12).Background(chromeBG(t)).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).
		FontSize(t.FontSize - 1).TextColor(t.TextMuted).Children(func() {
		ui.Text(c, m.Repo.Root()).SingleLine().Shrink(1)
		ui.Spacer(c)
		if a, ok := m.Activity(); ok {
			ui.Row(c).Gap(4).Tooltip(a.Detail()).Children(func() {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, a.Label()).SingleLine()
			})
		}
		branch := m.CurrentBranch
		if branch == "" {
			branch = i18n.T("Detached HEAD")
		}
		ui.Text(c, branch).SingleLine()
	})
}

// titleBarHeight is the height of the window's title bar, which holds the
// toolbar.
const titleBarHeight = 52

// header is the window's title bar, on Liquid Glass: the repository and its
// branch, the views, and the VCS operations, clear of the window controls.
// Narrow windows show the operations' icons alone.
func (ws *Workspace) header(c *ui.Context) {
	t := c.Theme()
	m := ws.Model
	bar := c.TitleBar()
	compact := c.Root().Bounds().W < 1180
	ui.Row(c.Key("toolbar")).Height(titleBarHeight).Shrink(0).Padding(0, bar.Right+12, 0, bar.Left+12).Gap(8).
		Background(chromeBG(t)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).ClipX().DragWindow().Children(func() {
		glassGroup(c.Key("where"), func() {
			capsuleButton(c.Key("repo"), "folder-git-2", m.Repo.Name(), false, true).Menu(ws.repoMenu)
			capsuleButton(c.Key("branch"), "git-branch", ws.branchLabel(), false, true).Menu(ws.branchPopup)
		}).Shrink(1).MinWidth(0)
		ui.Spacer(c)
		ws.views(c)
		ui.Spacer(c)
		if m.Busy != "" {
			ui.Row(c).Gap(4).Shrink(1).MinWidth(0).FontSize(t.FontSize - 1).TextColor(t.TextMuted).Children(func() {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, m.Busy+"…").SingleLine().Shrink(1)
			})
		}
		glassGroup(c.Key("vcs"), func() {
			if capsuleButton(c.Key("fetch"), "refresh-ccw", i18n.T("Fetch"), compact, false).Clicked() {
				ws.Command("fetch")
			}
			if capsuleButton(c.Key("update"), "arrow-down-to-line", i18n.T("Update"), compact, false).Tooltip(i18n.T("Update Project (⌘T)")).Clicked() {
				openUpdate(ws)
			}
			if capsuleButton(c.Key("push"), "arrow-up-from-line", i18n.T("Push"), compact, false).Clicked() {
				openPush(ws)
			}
			capsuleButton(c.Key("stashes"), "archive", i18n.Tf("Stashes ({})", len(m.Stashes)), compact, true).Menu(ws.stashMenu)
		})
		themeIcon := "moon"
		if t.Dark {
			themeIcon = "sun"
		}
		toggle := ui.ButtonBase(c.Key("theme")).Size(36, 36).Radius(18).Center().Shrink(0).Material(glass.Glass{Interactive: true}).
			Label(i18n.T("Toggle Theme")).Tooltip(i18n.T("Toggle Theme"))
		toggle.Children(func() { ui.Icon(c, icon(themeIcon)).FontSize(t.FontSize + 2) })
		if toggle.Clicked() {
			dark := t.Dark
			changeSettings(func(s *settings.Settings) {
				if dark {
					s.Theme = settings.ThemeLight
				} else {
					s.Theme = settings.ThemeDark
				}
			})
		}
	})
}

// views switches among Log, Commit, Shelf and Console: a segmented
// control of glass, the chosen view lifted on a pill, with the count of
// changes and of shelves as badges.
func (ws *Workspace) views(c *ui.Context) {
	t := c.Theme()
	tabs := []struct {
		icon, label string
		count       int
	}{
		{"git-graph", i18n.T("Log"), 0},
		{"git-commit-horizontal", i18n.T("Commit"), ws.changes.changeCount()},
		{"inbox", i18n.T("Shelf"), len(ws.Model.Shelves)},
		{"square-terminal", i18n.T("Console"), 0},
	}
	glassGroup(c.Key("views"), func() {
		for ix, tab := range tabs {
			b := ui.Row(c.Key(ix)).Height(30).PaddingX(12).Gap(6).Radius(15).Cursor(ui.CursorPointer).Label(tab.label)
			if ix == ws.tab {
				selectedPill(b, t)
			} else {
				b.TextColor(t.TextMuted)
				if b.Hovered() {
					b.Background(t.Text.Alpha(0.06)).TextColor(t.Text)
				}
			}
			if b.Clicked() {
				ws.tab = ix
			}
			b.Children(func() {
				ui.Icon(c, icon(tab.icon)).FontSize(t.FontSize + 1)
				ui.Text(c, tab.label).SingleLine().FontWeight(500)
				if tab.count > 0 {
					ui.Text(c, itoa(tab.count)).FontSize(t.FontSize - 2).FontWeight(600).SingleLine().
						PaddingX(6).Height(16).Radius(8).Background(t.Text.Alpha(0.1)).TextColor(t.TextMuted)
				}
			})
		}
	})
}

func (ws *Workspace) branchLabel() string {
	m := ws.Model
	switch {
	case m.CurrentBranch != "" && m.State == git.Merging:
		return i18n.Tf("{} (Merging)", m.CurrentBranch)
	case m.CurrentBranch != "" && m.State == git.Rebasing:
		return i18n.Tf("{} (Rebasing)", m.CurrentBranch)
	case m.CurrentBranch != "":
		return m.CurrentBranch
	case m.State == git.Rebasing:
		return i18n.T("Rebasing")
	}
	return i18n.T("Detached HEAD")
}

func (ws *Workspace) repoMenu(menu *ui.Menu) {
	m := ws.Model
	if menu.Item(i18n.T("Open Repository…")).Chosen() {
		promptOpenRepo(ws.Shell)
	}
	if menu.Item(i18n.T("Clone Repository…")).Chosen() {
		openClone(ws.Shell)
	}
	if menu.Item(i18n.T("Manage Remotes…")).Chosen() {
		openRemotes(ws)
	}
	if menu.Item(i18n.T("Manage Worktrees…")).Chosen() {
		openWorktrees(ws, "")
	}
	if menu.Item(i18n.T("Submodules…")).Chosen() {
		openSubmodules(ws)
	}
	var recent []string
	for _, p := range settings.Get().Recent {
		if p != m.Repo.Root() {
			recent = append(recent, p)
		}
	}
	if len(recent) > 0 {
		menu.Separator()
		menu.Submenu(i18n.T("Recent Repositories"), func(sub *ui.Menu) {
			for _, p := range recent {
				if sub.Item(recentLabel(p)).Chosen() {
					openPath(ws.Shell, p, false)
				}
			}
		})
	}
}

// branchPopup is the toolbar's branch menu, with recently used branches
// and favorites first.
func (ws *Workspace) branchPopup(menu *ui.Menu) {
	m := ws.Model
	if menu.Item(i18n.T("New Branch...")).Chosen() {
		newBranch(ws, "HEAD")
	}
	if siblings := siblingModels(m); len(siblings) > 0 {
		all := append([]*model.Model{m}, siblings...)
		if menu.Item(i18n.Tf("New Branch in All Repositories ({})...", len(all))).Chosen() {
			ws.Shell.PromptText(i18n.T("New Branch in All Repositories"), i18n.T("Create"), "", func(name string) {
				for _, target := range all {
					model.Run(target, i18n.Tf("Create branch {} in {}", name, target.Repo.Name()), func(r *git.Repo) (string, error) {
						return r.CreateBranch(name, "HEAD", true)
					})
				}
			})
		}
	}
	if menu.Item(i18n.T("Checkout Tag or Revision...")).Chosen() {
		ws.Shell.PromptText(i18n.T("Checkout Tag or Revision"), i18n.T("Checkout"), "", func(rev string) {
			m.Checkout(rev, false)
		})
	}
	// ✓ marks the current branch and ★ the favorites, which come first in
	// their group.
	label := func(b *git.Branch) string {
		mark := ""
		if b.IsHead {
			mark = "✓ "
		} else if m.IsFavorite(b) {
			mark = "★ "
		}
		if b.Track != "" {
			return mark + b.Name + "  (" + b.Track + ")"
		}
		return mark + b.Name
	}
	type group struct {
		title    string
		branches []git.Branch
	}
	var groups []group
	var recent []git.Branch
	for _, name := range m.RecentBranches {
		ix := slices.IndexFunc(m.Branches, func(b git.Branch) bool { return b.Kind == git.RefLocal && b.Name == name && !b.IsHead })
		if ix >= 0 && len(recent) < 5 {
			recent = append(recent, m.Branches[ix])
		}
	}
	if len(recent) > 0 {
		groups = append(groups, group{i18n.T("Recent"), recent})
	}
	for _, g := range []struct {
		title string
		kind  git.RefKind
	}{{i18n.T("Local"), git.RefLocal}, {i18n.T("Remote"), git.RefRemote}} {
		branches := m.BranchesOf(g.kind)
		// Stable, so each part keeps git's order.
		slices.SortStableFunc(branches, func(a, b git.Branch) int {
			fa, fb := m.IsFavorite(&a), m.IsFavorite(&b)
			switch {
			case fa && !fb:
				return -1
			case fb && !fa:
				return 1
			}
			return 0
		})
		if len(branches) > 0 {
			groups = append(groups, group{g.title, branches})
		}
	}
	for _, g := range groups {
		menu.Separator()
		menu.Item(g.title).Disabled(true)
		for _, b := range g.branches {
			menu.Submenu(label(&b), func(sub *ui.Menu) { branchMenu(sub, ws, b) })
		}
	}
}

func (ws *Workspace) stashMenu(menu *ui.Menu) {
	m := ws.Model
	if len(m.Stashes) == 0 {
		menu.Item(i18n.T("No stashes")).Disabled(true)
		return
	}
	if menu.Item(i18n.T("Show Stashes…")).Chosen() {
		openStashViewer(ws)
	}
	menu.Separator()
	for _, stash := range m.Stashes {
		menu.Submenu(stash.Name+": "+stash.Message, func(sub *ui.Menu) {
			name := stash.Name
			if sub.Item(i18n.T("Pop")).Chosen() {
				ws.run(i18n.T("Pop")+" "+name, func(r *git.Repo) (string, error) { return r.StashPop(name) })
			}
			if sub.Item(i18n.T("Apply")).Chosen() {
				ws.run(i18n.T("Apply")+" "+name, func(r *git.Repo) (string, error) { return r.StashApply(name) })
			}
			if sub.Item(i18n.T("Drop")).Chosen() {
				ws.Shell.Confirm(i18n.T("Drop Stash"), i18n.T("Drop this stash? This cannot be undone."), i18n.T("Drop"), func() {
					ws.run(i18n.T("Drop")+" "+name, func(r *git.Repo) (string, error) { return r.StashDrop(name) })
				})
			}
		})
	}
}

// recentLabel is "name — ~/dir/…/parent" for the recent repositories menu.
func recentLabel(path string) string {
	name := path[strings.LastIndexAny(path, `/\`)+1:]
	dir := tilde(path[:max(strings.LastIndexAny(path, `/\`), 0)])
	if n := utf8.RuneCountInString(dir); n > 48 {
		runes := []rune(dir)
		dir = string(runes[:16]) + "…" + string(runes[n-28:])
	}
	return name + " — " + dir
}

// offerSmartCheckout is JetBrains' "Git Checkout Problem" dialog: stash
// and restore the local changes, or discard them.
func offerSmartCheckout(s *Shell, m *model.Model, target string, remote bool, message string) {
	files := git.BlockedFiles(message)
	s.Open(&Dialog{Title: i18n.T("Git Checkout Problem"), Width: 520, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		ui.Text(c, i18n.Tf("Your local changes to these files would be overwritten by checking out {}:", short(target)))
		ui.Scroll(c).MaxHeight(160).Padding(0, 8).Children(func() {
			for _, f := range files {
				ui.Text(c, f).Font(monoFont).FontSize(t.FontSize - 1)
			}
		})
		muted(c, i18n.T("Smart Checkout stashes the changes, checks out and restores them."))
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if button(c.Key("cancel"), i18n.T("Cancel")).Clicked() {
				d.Close()
			}
			if button(c.Key("force"), i18n.T("Force Checkout")).TextColor(t.Danger).Clicked() {
				d.Close()
				model.Run(m, i18n.Tf("Force checkout {}", short(target)), func(r *git.Repo) (string, error) { return r.ForceCheckout(target, remote) })
			}
			if primaryButton(c.Key("smart"), i18n.T("Smart Checkout")).Clicked() {
				d.Close()
				model.Run(m, i18n.Tf("Smart checkout {}", short(target)), func(r *git.Repo) (string, error) { return r.SmartCheckout(target, remote) })
			}
		})
	}})
}

// applyPatch asks for a patch file and applies it to the work tree.
func applyPatch(ws *Workspace) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: ws.Shell.Win, Title: i18n.T("Apply Patch")})
		if err != nil || len(paths) == 0 {
			return
		}
		file := paths[0]
		model.Post(func() {
			ws.run(i18n.T("Apply patch"), func(r *git.Repo) (string, error) { return r.ApplyPatch(file) })
		})
	}()
}

// revealLabel is what the file manager is called here.
func revealLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return i18n.T("Reveal in Finder")
	case "windows":
		return i18n.T("Show in Explorer")
	}
	return i18n.T("Open Containing Folder")
}
