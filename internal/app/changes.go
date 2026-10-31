package app

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/changelists"
	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/patch"
	"github.com/Zenyet/banyan/internal/settings"
)

// group is a group of the Commit tab's changes: the merge conflicts, a
// changelist, or the unversioned files.
type group struct {
	kind int
	// list is the changelist's name.
	list string
}

const (
	groupConflicts = iota
	groupList
	groupUnversioned
)

func groupOf(e *git.StatusEntry, lists *changelists.Changelists) group {
	switch {
	case e.IsConflicted():
		return group{kind: groupConflicts}
	case e.IsUntracked():
		return group{kind: groupUnversioned}
	}
	return group{groupList, lists.ListOf(e.Path)}
}

func (g group) title() string {
	switch {
	case g.kind == groupConflicts:
		return i18n.T("Merge Conflicts")
	case g.kind == groupUnversioned:
		return i18n.T("Unversioned Files")
	case g.list == changelists.Default:
		// The default list's name is shown in the interface language.
		return i18n.T("Changes")
	}
	return g.list
}

// changeRow is a row of the list: a group's header (entry -1), or one of
// its files.
type changeRow struct {
	group int
	entry int
}

// ChangesView is the "Commit" tab: local changes with checkboxes, the
// commit message and the diff preview.
type ChangesView struct {
	ws         *Workspace
	included   map[string]bool
	known      map[string]bool
	conflicted map[string]bool
	// prefilledMergeMessage is the merge message put into the message box,
	// cleared again when the merge ends if it was left as it was.
	prefilledMergeMessage *string
	collapsed             map[group]bool
	selected              string
	message               string
	messageBox            ui.Handle
	focusMessageNext      bool
	amend                 bool
	// options are the author, sign-off, signing and hooks for the next
	// commit.
	options git.CommitOptions
	diff    *DiffPane
	// hunks are those of the selected file, when it can be committed
	// partially.
	hunks   []patch.Hunk
	hunkGen int
	// excluded are the changed lines left out of the commit, per file.
	excluded map[string]map[patch.ChangedLine]bool
	// expanded are hunks of the selected file showing their lines.
	expanded map[string]bool
	groups   []struct {
		group   group
		entries []git.StatusEntry
	}
	rows        []changeRow
	list        ui.ListState
	listWidth   float32
	historyOpen bool
}

func newChangesView(ws *Workspace) *ChangesView {
	cv := &ChangesView{
		ws:         ws,
		included:   map[string]bool{},
		known:      map[string]bool{},
		conflicted: map[string]bool{},
		collapsed:  map[group]bool{},
		excluded:   map[string]map[patch.ChangedLine]bool{},
		expanded:   map[string]bool{},
		diff:       newDiffPane(),
		listWidth:  420,
	}
	cv.syncStatus()
	return cv
}

func (cv *ChangesView) focusMessage() { cv.focusMessageNext = true }

func (cv *ChangesView) changeCount() int { return len(cv.ws.Model.Status) }

func (cv *ChangesView) modelChanged(e model.Event) {
	switch e.Kind {
	case model.Changed:
		cv.syncStatus()
	case model.MessageRestored:
		// An undone commit's message, unless a new one is being written.
		if strings.TrimSpace(cv.message) == "" {
			cv.message = e.Text
		}
	}
}

// syncStatus includes new tracked changes of the active list by default,
// unversioned files not.
func (cv *ChangesView) syncStatus() {
	m := cv.ws.Model
	lists := m.Changelists
	paths := map[string]bool{}
	for _, e := range m.Status {
		paths[e.Path] = true
		isNew := !cv.known[e.Path]
		cv.known[e.Path] = true
		if e.IsConflicted() {
			cv.conflicted[e.Path] = true
		} else if cv.conflicted[e.Path] || (isNew && !e.IsUntracked() && lists.ListOf(e.Path) == lists.Active()) {
			// Freshly resolved conflicts are ready to commit.
			delete(cv.conflicted, e.Path)
			cv.included[e.Path] = true
		}
	}
	for _, set := range []map[string]bool{cv.known, cv.conflicted, cv.included} {
		for p := range set {
			if !paths[p] {
				delete(set, p)
			}
		}
	}
	for p := range cv.excluded {
		if !paths[p] {
			delete(cv.excluded, p)
		}
	}
	if m.State == git.Clean {
		if cv.prefilledMergeMessage != nil && *cv.prefilledMergeMessage == strings.TrimSpace(cv.message) {
			cv.message = ""
		}
		cv.prefilledMergeMessage = nil
	} else if cv.prefilledMergeMessage == nil && strings.TrimSpace(cv.message) == "" {
		message := m.Repo.MergeMessage()
		cv.prefilledMergeMessage = &message
		cv.message = message
	}
	if !paths[cv.selected] {
		cv.selected = ""
		if len(m.Status) > 0 {
			cv.selected = m.Status[0].Path
		}
	}
	cv.loadDiff()
}

func (cv *ChangesView) entry(path string) (git.StatusEntry, bool) {
	for _, e := range cv.ws.Model.Status {
		if e.Path == path {
			return e, true
		}
	}
	return git.StatusEntry{}, false
}

func (cv *ChangesView) selectPath(path string) {
	if cv.selected != path {
		clear(cv.expanded)
	}
	cv.selected = path
	cv.loadDiff()
}

// canCommitPartially reports whether a file can be split into hunks: only
// plain modifications of tracked files can.
func canCommitPartially(e *git.StatusEntry) bool { return e.DisplayStatus() == 'M' && e.OrigPath == "" }

func (cv *ChangesView) loadDiff() {
	e, ok := cv.entry(cv.selected)
	if !ok {
		cv.diff.ShowMessage("", i18n.T("No local changes"))
		cv.hunks = nil
		return
	}
	repo := cv.ws.Model.Repo
	// The diff shown follows the whitespace option; the hunks to commit
	// always come from the full patch, as the commit applies it.
	cv.diff.Load(e.Path, func() (string, error) {
		return repo.WorktreeDiff(&e, settings.Read(func(s *settings.Settings) bool { return s.IgnoreWhitespace }))
	})
	cv.hunkGen++
	gen := cv.hunkGen
	if !canCommitPartially(&e) {
		cv.hunks = nil
		return
	}
	go func() {
		p, err := repo.WorktreeDiff(&e, false)
		model.Post(func() {
			if gen != cv.hunkGen {
				return
			}
			cv.hunks = nil
			if err == nil {
				cv.hunks = patch.Parse(p).Hunks
			}
			// Forget exclusions of lines that no longer exist.
			lines := changedLines(cv.hunks)
			for h := range cv.expanded {
				if !slices.ContainsFunc(cv.hunks, func(x patch.Hunk) bool { return x.Header == h }) {
					delete(cv.expanded, h)
				}
			}
			if ex := cv.excluded[e.Path]; ex != nil {
				for l := range ex {
					if !lines[l] {
						delete(ex, l)
					}
				}
				if len(ex) == 0 {
					delete(cv.excluded, e.Path)
				}
			}
		})
	}()
}

// changedLines are every changed line of hunks.
func changedLines(hunks []patch.Hunk) map[patch.ChangedLine]bool {
	lines := map[patch.ChangedLine]bool{}
	for _, h := range hunks {
		for _, ix := range h.Changes() {
			lines[patch.ChangedLine{Header: h.Header, Index: ix}] = true
		}
	}
	return lines
}

func (cv *ChangesView) toggle(path string) {
	delete(cv.excluded, path)
	cv.included[path] = !cv.included[path]
	if !cv.included[path] {
		delete(cv.included, path)
	}
}

func (cv *ChangesView) isPartial(path string) bool {
	return cv.included[path] && len(cv.excluded[path]) > 0
}

func (cv *ChangesView) isLineIncluded(path string, line patch.ChangedLine) bool {
	return cv.included[path] && !cv.excluded[path][line]
}

// hunkInclusion is how many of the hunk's changed lines are included, out
// of how many.
func (cv *ChangesView) hunkInclusion(path string, h *patch.Hunk) (int, int) {
	changes := h.Changes()
	included := 0
	for _, ix := range changes {
		if cv.isLineIncluded(path, patch.ChangedLine{Header: h.Header, Index: ix}) {
			included++
		}
	}
	return included, len(changes)
}

// setLinesIncluded includes or leaves out some changed lines of the
// selected file. Including lines of an unchecked file includes just those
// lines.
func (cv *ChangesView) setLinesIncluded(lines []patch.ChangedLine, include bool) {
	path := cv.selected
	all := changedLines(cv.hunks)
	if !cv.included[path] {
		cv.included[path] = true
		ex := map[patch.ChangedLine]bool{}
		for l := range all {
			ex[l] = true
		}
		cv.excluded[path] = ex
	}
	ex := cv.excluded[path]
	if ex == nil {
		ex = map[patch.ChangedLine]bool{}
		cv.excluded[path] = ex
	}
	for _, l := range lines {
		if include {
			delete(ex, l)
		} else {
			ex[l] = true
		}
	}
	switch {
	case len(ex) == len(all):
		delete(cv.excluded, path)
		delete(cv.included, path)
	case len(ex) == 0:
		delete(cv.excluded, path)
	}
}

// toggleHunk includes a partly included hunk whole.
func (cv *ChangesView) toggleHunk(h *patch.Hunk) {
	included, total := cv.hunkInclusion(cv.selected, h)
	var lines []patch.ChangedLine
	for _, ix := range h.Changes() {
		lines = append(lines, patch.ChangedLine{Header: h.Header, Index: ix})
	}
	cv.setLinesIncluded(lines, included < total)
}

func (cv *ChangesView) toggleGroup(g group, include bool) {
	lists := cv.ws.Model.Changelists
	for _, e := range cv.ws.Model.Status {
		if groupOf(&e, lists) == g {
			if include {
				cv.included[e.Path] = true
			} else {
				delete(cv.included, e.Path)
			}
		}
	}
}

func (cv *ChangesView) setAmend(amend bool) {
	cv.amend = amend
	if amend && strings.TrimSpace(cv.message) == "" {
		cv.message, _ = cv.ws.Model.Repo.LastCommitMessage()
	}
}

func (cv *ChangesView) commit(push bool) {
	m := cv.ws.Model
	message := strings.TrimSpace(cv.message)
	if message == "" {
		m.Fail(fmt.Errorf("%s", i18n.T("Commit message is empty")))
		return
	}
	var paths []string
	for _, e := range m.Status {
		if cv.included[e.Path] {
			paths = append(paths, e.Path)
			if e.OrigPath != "" {
				paths = append(paths, e.OrigPath)
			}
		}
	}
	if len(paths) == 0 && !cv.amend && m.State == git.Clean {
		m.Fail(fmt.Errorf("%s", i18n.T("No changes selected to commit")))
		return
	}
	var partial []git.PartialFile
	for path, ex := range cv.excluded {
		if cv.included[path] && len(ex) > 0 {
			var lines []patch.ChangedLine
			for l := range ex {
				lines = append(lines, l)
			}
			partial = append(partial, git.PartialFile{Path: path, Excluded: lines})
		}
	}
	var full []string
	for _, p := range paths {
		if !slices.ContainsFunc(partial, func(f git.PartialFile) bool { return f.Path == p }) {
			full = append(full, p)
		}
	}
	amend, options := cv.amend, cv.options
	// Like JetBrains, the author applies to one commit only.
	cv.options.Author = ""
	label := i18n.T("Commit")
	if push {
		label = i18n.T("Commit and Push")
	}
	settings.AddMessage(message)
	clear(cv.excluded)
	model.Run(m, label, func(r *git.Repo) (string, error) {
		var err error
		if len(partial) == 0 {
			_, err = r.Commit(message, full, amend, &options)
		} else {
			_, err = r.CommitPartial(message, full, partial, amend, &options)
		}
		if err == nil && push {
			_, err = r.Push()
		}
		return "", err
	})
	cv.message = ""
	cv.amend = false
}

func (cv *ChangesView) rollback(paths []string) {
	var entries []git.StatusEntry
	for _, e := range cv.ws.Model.Status {
		if slices.Contains(paths, e.Path) && !e.IsUntracked() {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return
	}
	cv.ws.Shell.Confirm(i18n.T("Rollback Changes"), i18n.Tf("Discard local changes in {} file(s)? This cannot be undone.", len(entries)), i18n.T("Rollback"), func() {
		cv.ws.run(i18n.T("Rollback"), func(r *git.Repo) (string, error) { return "", r.Rollback(entries) })
	})
}

func (cv *ChangesView) includedPaths() []string {
	var paths []string
	for p := range cv.included {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// promptShelve asks for a name, then shelves paths.
func promptShelve(ws *Workspace, paths []string) {
	def := ""
	if len(paths) > 0 {
		def = ws.Model.Changelists.ListOf(paths[0])
	}
	title := i18n.Tf("Shelve {} Files", len(paths))
	if len(paths) == 1 {
		title = i18n.T("Shelve 1 File")
	}
	ws.Shell.PromptText(title, i18n.T("Shelve"), def, func(name string) { ws.Model.Shelve(name, paths) })
}

func (cv *ChangesView) buildRows() {
	m := cv.ws.Model
	lists := m.Changelists
	order := []group{{kind: groupConflicts}}
	for _, name := range lists.Names() {
		order = append(order, group{groupList, name})
	}
	order = append(order, group{kind: groupUnversioned})
	// Every changelist shows, even empty, once there is more than one.
	showEmptyLists := len(lists.Names()) > 1
	cv.groups = cv.groups[:0]
	for _, g := range order {
		var members []git.StatusEntry
		for _, e := range m.Status {
			if groupOf(&e, lists) == g {
				members = append(members, e)
			}
		}
		if len(members) > 0 || (showEmptyLists && g.kind == groupList) {
			cv.groups = append(cv.groups, struct {
				group   group
				entries []git.StatusEntry
			}{g, members})
		}
	}
	cv.rows = cv.rows[:0]
	for gi, g := range cv.groups {
		cv.rows = append(cv.rows, changeRow{gi, -1})
		if !cv.collapsed[g.group] {
			for ei := range g.entries {
				cv.rows = append(cv.rows, changeRow{gi, ei})
			}
		}
	}
}

func (cv *ChangesView) View(c *ui.Context) {
	t := c.Theme()
	cv.buildRows()
	ui.Split(c.Key("changes"), &cv.listWidth, func() {
		ui.Column(c).Fill().Background(t.Surface).Children(func() {
			panelHeader(c, i18n.T("Local Changes"))
			cv.toolbar(c)
			if len(cv.groups) == 0 {
				emptyMessage(c, i18n.T("No local changes"))
			} else {
				ui.List(c.Key("changes-list"), &cv.list, len(cv.rows), func(i int) {
					r := cv.rows[i]
					g := cv.groups[r.group]
					if r.entry < 0 {
						cv.groupHeader(c, g.group, g.entries)
					} else {
						cv.entryRow(c, &g.entries[r.entry])
					}
				}).Grow(1).PaddingX(6).MinHeight(0)
			}
			cv.commitBox(c)
		})
	}, func() {
		ui.Column(c).Fill().Children(func() {
			if e, ok := cv.entry(cv.selected); ok && e.IsConflicted() {
				cv.conflictCard(c, &e)
			}
			if len(cv.hunks) > 0 {
				cv.hunkRows(c)
			}
			cv.diff.View(c)
		})
	}).Fill()
}

func (cv *ChangesView) toolbar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c.Key("changes-toolbar")).Height(36).PaddingX(8).Gap(2).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		if iconButton(c.Key("refresh"), "refresh-cw", i18n.T("Refresh")).Clicked() {
			cv.ws.Model.Refresh()
		}
		if iconButton(c.Key("rollback"), "undo-2", i18n.T("Rollback Selected Changes")).Clicked() {
			cv.rollback(cv.includedPaths())
		}
		if iconButton(c.Key("stash"), "archive", i18n.T("Stash Changes...")).Clicked() {
			openStashDialog(cv.ws, cv.includedPaths())
		}
		if iconButton(c.Key("patch"), "file-diff", i18n.T("Apply Patch...")).Clicked() {
			applyPatch(cv.ws)
		}
		if iconButton(c.Key("shelve"), "inbox", i18n.T("Shelve Selected Changes...")).Clicked() {
			if paths := cv.includedPaths(); len(paths) == 0 {
				cv.ws.Model.Fail(fmt.Errorf("%s", i18n.T("No changes selected to shelve")))
			} else {
				promptShelve(cv.ws, paths)
			}
		}
		ui.Spacer(c)
		muted(c, i18n.Tf("{} of {} selected", len(cv.included), cv.changeCount())).SingleLine()
	})
}

func (cv *ChangesView) groupHeader(c *ui.Context, g group, entries []git.StatusEntry) {
	t := c.Theme()
	m := cv.ws.Model
	all := len(entries) > 0 && !slices.ContainsFunc(entries, func(e git.StatusEntry) bool { return !cv.included[e.Path] })
	active := g.kind == groupList && m.Changelists.Active() == g.list
	header := ui.Row(c.Key(g)).Height(24).PaddingX(8).Gap(6).Cursor(ui.CursorPointer)
	if header.Clicked() {
		cv.collapsed[g] = !cv.collapsed[g]
	}
	if g.kind == groupList {
		var paths []string
		for _, e := range entries {
			paths = append(paths, e.Path)
		}
		header.ContextMenu(func(menu *ui.Menu) { cv.changelistMenu(menu, g.list, active, paths) })
	}
	header.Children(func() {
		chevron := "chevron-down"
		if cv.collapsed[g] {
			chevron = "chevron-right"
		}
		ui.Icon(c, icon(chevron)).FontSize(t.FontSize - 1)
		checked := all
		if ui.Checkbox(c.Key("check"), &checked, "").Changed() {
			cv.toggleGroup(g, checked)
		}
		ui.Text(c, g.title()).FontWeight(600).SingleLine()
		if active {
			ui.Box(c).PaddingX(4).Radius(3).Background(t.SurfaceHover).Children(func() {
				ui.Text(c, i18n.T("active")).FontSize(t.FontSize - 2)
			})
		}
		muted(c, fileCount(len(entries)))
	})
}

func (cv *ChangesView) entryRow(c *ui.Context, e *git.StatusEntry) {
	t := c.Theme()
	path := e.Path
	dir, name := "", path
	if ix := strings.LastIndexByte(path, '/'); ix >= 0 {
		dir, name = path[:ix], path[ix+1:]
	}
	color := statusColor(e.DisplayStatus(), t)
	row := ui.Row(c.Key(path)).Height(24).PaddingStart(28).PaddingEnd(8).Gap(6)
	switch {
	case cv.selected == path:
		row.Background(selectionBG)
	case row.Hovered():
		row.Background(hoverBG(t))
	}
	if row.Clicked() {
		cv.selectPath(path)
		if e.IsConflicted() && row.DoubleClicked() {
			openMergeTool(cv.ws, path)
		}
	}
	entry := *e
	row.ContextMenu(func(menu *ui.Menu) { cv.entryMenu(menu, entry) })
	row.Children(func() {
		checked := cv.included[path]
		if ui.Checkbox(c.Key("include"), &checked, "").Changed() {
			cv.toggle(path)
		}
		ui.Image(c, fileIcon(path)).Size(t.FontSize+1, t.FontSize+1)
		ui.Text(c, name).TextColor(color).SingleLine()
		muted(c, dir).SingleLine().Shrink(1)
		if cv.isPartial(path) {
			ui.Box(c).PaddingX(4).Radius(3).Background(t.SurfaceHover).Children(func() {
				ui.Text(c, "partial").FontSize(t.FontSize - 2)
			})
		}
	})
}

func (cv *ChangesView) entryMenu(menu *ui.Menu, e git.StatusEntry) {
	ws, m := cv.ws, cv.ws.Model
	path := e.Path
	full := filepath.Join(m.Repo.Root(), filepath.FromSlash(path))
	if !e.IsConflicted() {
		if !e.IsUntracked() {
			menu.Submenu(i18n.T("Move to Another Changelist"), func(sub *ui.Menu) {
				current := m.Changelists.ListOf(path)
				for _, name := range m.Changelists.Names() {
					if name != current && sub.Item(name).Chosen() {
						m.ChangeLists(func(l *changelists.Changelists) error { return l.MoveTo([]string{path}, name) })
					}
				}
				sub.Separator()
				if sub.Item(i18n.T("New Changelist...")).Chosen() {
					ws.Shell.PromptText(i18n.T("New Changelist"), i18n.T("Create"), "", func(name string) {
						m.ChangeLists(func(l *changelists.Changelists) error { return l.MoveTo([]string{path}, name) })
					})
				}
			})
		}
		if menu.Item(i18n.T("Shelve...")).Chosen() {
			promptShelve(ws, []string{path})
		}
		menu.Separator()
	}
	switch {
	case e.IsConflicted():
		if menu.Item(i18n.T("Merge...")).Chosen() {
			openMergeTool(ws, path)
		}
		menu.Separator()
		yes, no := true, false
		for _, r := range []struct {
			label string
			ours  *bool
		}{{i18n.T("Accept Yours"), &yes}, {i18n.T("Accept Theirs"), &no}, {i18n.T("Mark as Resolved"), nil}} {
			if menu.Item(r.label).Chosen() {
				ws.run(i18n.Tf("Resolve {}", path), func(repo *git.Repo) (string, error) { return repo.ResolveConflict(path, r.ours) })
			}
		}
	case !e.IsUntracked():
		if menu.Item(i18n.T("Show History")).Chosen() {
			showHistory(ws, path)
		}
		if menu.Item(i18n.T("Annotate")).Chosen() {
			annotate(ws, "HEAD", path)
		}
		menu.Separator()
		if menu.Item(i18n.T("Rollback...")).Chosen() {
			cv.rollback([]string{path})
		}
	default:
		if menu.Item(i18n.T("Add to VCS")).Chosen() {
			ws.run(i18n.Tf("Add {}", path), func(r *git.Repo) (string, error) { return r.Git("add", "--", path) })
		}
		if menu.Item(i18n.T("Add to .gitignore")).Chosen() {
			ws.run(i18n.Tf("Ignore {}", path), func(r *git.Repo) (string, error) { return "", r.AddToGitignore(path) })
		}
	}
	menu.Separator()
	if menu.Item(i18n.T("Copy Path")).Chosen() {
		mygo.Clipboard.WriteText(full)
	}
	if menu.Item(revealLabel()).Chosen() {
		mygo.Shell.ShowItemInFolder(full)
	}
}

// changelistMenu is the menu of a changelist's header.
func (cv *ChangesView) changelistMenu(menu *ui.Menu, name string, active bool, paths []string) {
	ws, m := cv.ws, cv.ws.Model
	if !active && menu.Item(i18n.T("Set as Active Changelist")).Chosen() {
		m.ChangeLists(func(l *changelists.Changelists) error { return l.SetActive(name) })
	}
	if menu.Item(i18n.T("New Changelist...")).Chosen() {
		ws.Shell.PromptText(i18n.T("New Changelist"), i18n.T("Create"), "", func(n string) {
			m.ChangeLists(func(l *changelists.Changelists) error { return l.Create(n) })
		})
	}
	if menu.Item(i18n.T("Rename Changelist...")).Chosen() {
		ws.Shell.PromptText(i18n.T("Rename Changelist"), i18n.T("Rename"), name, func(n string) {
			m.ChangeLists(func(l *changelists.Changelists) error { return l.Rename(name, n) })
		})
	}
	if menu.Item(i18n.T("Delete Changelist")).Chosen() {
		m.ChangeLists(func(l *changelists.Changelists) error { return l.Delete(name) })
	}
	if len(paths) > 0 {
		menu.Separator()
		if menu.Item(i18n.T("Shelve Changes...")).Chosen() {
			promptShelve(ws, paths)
		}
	}
}

// hunkRows are checkboxes for the hunks of the selected file and, once
// expanded, for their lines, like the gutter checkboxes of JetBrains'
// commit diff.
func (cv *ChangesView) hunkRows(c *ui.Context) {
	t := c.Theme()
	path := cv.selected
	ui.Column(c.Key("hunks")).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		muted(c, i18n.T("Changes to commit in this file")).Padding(4, 8, 0, 8)
		ui.Scroll(c).MaxHeight(240).PaddingY(4).Children(func() {
			for ix := range cv.hunks {
				h := &cv.hunks[ix]
				line := h.FirstChangedLine()
				included, total := cv.hunkInclusion(path, h)
				expanded := cv.expanded[h.Header]
				row := ui.Row(c.Key(h.Header)).Height(24).PaddingX(8).Gap(8)
				if row.Hovered() {
					row.Background(hoverBG(t))
				}
				if row.Clicked() {
					cv.diff.ScrollToLine(line)
				}
				row.Children(func() {
					chevron, tip := "chevron-right", i18n.T("Choose lines")
					if expanded {
						chevron, tip = "chevron-down", i18n.T("Hide lines")
					}
					if iconButton(c.Key("expand"), chevron, tip).Clicked() {
						cv.expanded[h.Header] = !expanded
					}
					checked := included > 0
					if ui.Checkbox(c.Key("include"), &checked, "").Changed() {
						cv.toggleHunk(h)
					}
					muted(c, i18n.Tf("Line {}", line)).SingleLine()
					context := h.Context()
					if context == "" {
						context = i18n.Tf("Change {}", ix+1)
					}
					ui.Text(c, context).SingleLine().Grow(1).Shrink(1)
					if included > 0 && included < total {
						muted(c, fmt.Sprintf("%d/%d lines", included, total))
					}
					ui.Text(c, fmt.Sprintf("+%d", h.Additions())).FontSize(t.FontSize - 1).TextColor(t.Success)
					ui.Text(c, fmt.Sprintf("−%d", h.Deletions())).FontSize(t.FontSize - 1).TextColor(t.Danger)
				})
				if !expanded {
					continue
				}
				for _, lineIx := range h.Changes() {
					text := h.Lines[lineIx]
					key := patch.ChangedLine{Header: h.Header, Index: lineIx}
					checked := cv.isLineIncluded(path, key)
					added := strings.HasPrefix(text, "+")
					newLine := h.NewLineAt(lineIx)
					color := t.Danger
					if added {
						color = t.Success
					}
					lr := ui.Row(c.Key(key)).Height(22).PaddingStart(44).PaddingEnd(8).Gap(8).Background(color.Alpha(0.08)).Font(monoFont).FontSize(t.FontSize - 1)
					if lr.Clicked() {
						cv.diff.ScrollToLine(newLine)
					}
					lr.Children(func() {
						on := checked
						if ui.Checkbox(c.Key("include"), &on, "").Changed() {
							cv.setLinesIncluded([]patch.ChangedLine{key}, on)
						}
						ui.Text(c, fmt.Sprint(newLine)).Width(36).TextColor(t.TextMuted)
						lt := ui.Text(c, text).SingleLine().Grow(1).Shrink(1).TextColor(color)
						if !checked {
							lt.Strikethrough().Opacity(0.6)
						}
					})
				}
			}
		})
	})
}

// conflictCard says what can be done with the selected conflicted file,
// above its diff.
func (cv *ChangesView) conflictCard(c *ui.Context, e *git.StatusEntry) {
	t := c.Theme()
	ws, m := cv.ws, cv.ws.Model
	path := e.Path
	ours, theirs, _ := e.ConflictSides()
	oursName, theirsName := "yours", "theirs"
	if op := m.Operation; op != nil {
		oursName, theirsName = op.Ours, op.Theirs
	}
	ui.Row(c.Key("conflict-card")).Padding(12).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Background(t.Warning.Alpha(0.08)).Children(func() {
		ui.Icon(c, icon("triangle-alert")).TextColor(t.Warning)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, i18n.Tf("{} has merge conflicts", path)).FontWeight(600)
			muted(c, i18n.Tf("Yours ({}): {}  ·  Theirs ({}): {}", oursName, ours.Label(), theirsName, theirs.Label())).SingleLine()
		})
		yes, no := true, false
		if button(c.Key("yours"), i18n.T("Accept Yours")).Clicked() {
			ws.run(i18n.Tf("Accept yours for {}", path), func(r *git.Repo) (string, error) { return r.ResolveConflict(path, &yes) })
		}
		if button(c.Key("theirs"), i18n.T("Accept Theirs")).Clicked() {
			ws.run(i18n.Tf("Accept theirs for {}", path), func(r *git.Repo) (string, error) { return r.ResolveConflict(path, &no) })
		}
		if e.CanMergeText() && primaryButton(c.Key("merge"), i18n.T("Merge…")).Clicked() {
			openMergeTool(ws, path)
		}
	})
}

// optionsSummary says what the next commit does beyond the defaults, e.g.
// "Author: Ada · Sign-off".
func (cv *ChangesView) optionsSummary() string {
	o := &cv.options
	var parts []string
	if o.Author != "" {
		name, _, _ := strings.Cut(o.Author, "<")
		parts = append(parts, i18n.Tf("Author: {}", strings.TrimSpace(name)))
	}
	if o.SignOff {
		parts = append(parts, i18n.T("Sign-off"))
	}
	if o.Sign != nil {
		if *o.Sign {
			parts = append(parts, i18n.T("Signed"))
		} else {
			parts = append(parts, i18n.T("Not signed"))
		}
	}
	if o.SkipHooks {
		parts = append(parts, i18n.T("Hooks skipped"))
	}
	return strings.Join(parts, " · ")
}

func (cv *ChangesView) commitBox(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("commit-box")).Padding(10).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		box := ui.TextArea(c.Key("message"), &cv.message).Placeholder(i18n.T("Commit Message")).Lines(4, 12).FillWidth().Radius(10).Bind(&cv.messageBox)
		if cv.focusMessageNext {
			cv.focusMessageNext = false
			cv.messageBox.Focus()
		}
		// Cmd-Enter in the message commits, as in JetBrains.
		if box.Shortcut(ui.Cmd, ui.KeyEnter) {
			cv.commit(false)
		}
		for _, problem := range messageProblems(cv.message) {
			muted(c, problem)
		}
		if summary := cv.optionsSummary(); summary != "" {
			ui.Text(c, summary).FontSize(t.FontSize - 1).TextColor(t.Warning).SingleLine()
		}
		ui.Row(c).Gap(8).Children(func() {
			amend := cv.amend
			if ui.Checkbox(c.Key("amend"), &amend, i18n.T("Amend")).Changed() {
				cv.setAmend(amend)
			}
			iconButton(c.Key("history"), "clock", i18n.T("Commit Message History")).Menu(func(menu *ui.Menu) {
				messages := settings.Get().Messages
				if len(messages) == 0 {
					menu.Item(i18n.T("No recent messages")).Disabled(true)
					return
				}
				for _, message := range messages {
					subject, _, _ := strings.Cut(message, "\n")
					label := subject
					if utf8.RuneCountInString(subject) > 70 {
						label = string([]rune(subject)[:70])
					}
					if label != subject || strings.Contains(message, "\n") {
						label += "…"
					}
					if menu.Item(label).Chosen() {
						cv.message = message
					}
				}
			})
			if iconButton(c.Key("options"), "settings", i18n.T("Commit Options…")).Clicked() {
				openCommitOptions(cv.ws, cv.options, func(o git.CommitOptions) { cv.options = o })
			}
			ui.Spacer(c)
			if button(c.Key("commit-push"), i18n.T("Commit and Push")).Clicked() {
				cv.commit(true)
			}
			if primaryButton(c.Key("commit"), i18n.T("Commit")).Clicked() {
				cv.commit(false)
			}
		})
	})
}

// messageProblems says where a commit message strays from the usual form:
// a subject of at most 72 characters, then a blank line before the body.
// Shown, not enforced.
func messageProblems(message string) []string {
	const subjectLimit = 72
	lines := strings.Split(strings.TrimSuffix(message, "\n"), "\n")
	var problems []string
	if n := utf8.RuneCountInString(strings.TrimRight(lines[0], " \t\r")); n > subjectLimit {
		problems = append(problems, i18n.Tf("The subject is {} characters; {} or fewer reads best in logs", n, subjectLimit))
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		problems = append(problems, i18n.T("Leave a blank line between the subject and the body"))
	}
	return problems
}
