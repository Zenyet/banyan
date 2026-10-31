package app

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/graph"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

const (
	rowHeight = 24
	// farDown is how many commits above one to go to are too many to load.
	farDown      = 100_000
	laneWidth    = 14
	graphPadding = 6
	// loadingDelay lets quick loads go without a spinner, so it doesn't
	// flicker.
	loadingDelay = 400 * time.Millisecond
	authorWidth  = 140
	timeWidth    = 130
)

// dateFilters are git log --since values of the Date filter.
var dateFilters = []string{"24.hours.ago", "7.days.ago", "30.days.ago", "1.year.ago"}

func dateFilterLabel(ix int) string {
	switch ix {
	case 0:
		return i18n.T("Last 24 Hours")
	case 1:
		return i18n.T("Last 7 Days")
	case 2:
		return i18n.T("Last 30 Days")
	}
	return i18n.T("Last Year")
}

// LogView is the Git "Log" tab: branches, commit graph, commit details and
// diff preview.
type LogView struct {
	ws      *Workspace
	commits []git.Commit
	// graph keeps the edges of whole, the selected commit, whole; cutGraph
	// has every long edge cut short.
	graph, cutGraph []graph.Row
	whole           string
	graphGen        int
	// onHead are indices into commits of those on the current branch, drawn
	// in bold when other branches are shown too, as JetBrains highlights
	// them.
	onHead map[int]bool
	// rows are indices into commits of those that pass the text filter.
	rows []int
	// selectedRow is the row chosen, and chosen the commits chosen with it
	// by Cmd- or Shift-click.
	selectedRow int
	chosen      ui.Selection[string]
	selected    string
	head        string
	list        ui.ListState
	query       string
	search      ui.Handle
	// focusSearchNext focuses the search box in the next frame.
	focusSearchNext bool
	branchFilter    string
	// authorFilter are the authors shown, any of them; git does the
	// filtering.
	authorFilter []string
	// knownAuthors are authors seen in the log while it wasn't filtered by
	// author, most active first, to choose from.
	knownAuthors []string
	// me is user.name, for "Me".
	me         string
	dateFilter int
	// pathFilter are files or folders relative to the repository root,
	// any of them, and recentPaths those filtered by before, newest first.
	pathFilter  []string
	recentPaths []string
	// grep is the search text git looked for in every message, past the
	// commits loaded, after "Search the whole history".
	grep string

	branchRows     []branchRow
	branchClosed   map[string]bool
	branchList     ui.ListState
	branchSelected int
	branchShown    int

	details     *git.CommitDetails
	base        int
	fileNodes   []*pathNode
	fileClosed  map[string]bool
	files       map[string]git.FileChange
	fileRows    []treeRow
	fileList    ui.ListState
	fileRow     int
	shownFile   string
	diff        *DiffPane
	loading     bool
	loadingFrom time.Time
	// limit is how many commits to load; it grows with "Load more".
	limit       int
	refsVersion uint64
	// pendingReveal is a commit to select once the log has loaded, from Go
	// to Revision.
	pendingReveal string
	logGen        int
	detailsGen    int
	// extrasLoaded says the details' branches and signature are in;
	// cancelExtras stops looking for them, and downloading the commit's
	// files in a partial clone.
	extrasLoaded bool
	cancelExtras context.CancelFunc
	shownRow     int

	branchesWidth  float32
	commitsPercent float32
	topPercent     float32
	filesPercent   float32
}

func newLogView(ws *Workspace) *LogView {
	lv := &LogView{
		ws:             ws,
		me:             ws.Model.Repo.UserName(),
		dateFilter:     -1,
		branchClosed:   map[string]bool{"group:" + i18n.T("Tags"): true},
		fileClosed:     map[string]bool{},
		diff:           newDiffPane(),
		limit:          settings.Read(func(s *settings.Settings) int { return s.LogPageSize }),
		selectedRow:    -1,
		shownRow:       -1,
		branchSelected: -1,
		branchShown:    -1,
		fileRow:        -1,
		branchesWidth:  230,
		commitsPercent: 70,
		topPercent:     64,
		filesPercent:   60,
	}
	lv.diff.Remote = ws.Model.Repo.IsPartialClone()
	lv.list.Key = func(i int) any { return lv.commits[lv.rows[i]].ID }
	lv.list.Selected = &lv.selectedRow
	lv.list.Selection = &lv.chosen
	lv.list.Label = func(i int) string { return lv.commits[lv.rows[i]].Subject }
	lv.branchList.Selected = &lv.branchSelected
	lv.fileList.Selected = &lv.fileRow
	lv.fileList.Key = func(i int) any { return "f:" + lv.fileRows[i].Node.Path }
	return lv
}

func (lv *LogView) modelChanged(e model.Event) {
	if e.Kind == model.Changed {
		lv.rebuildBranches()
		lv.reloadIfRefsMoved()
	}
}

func (lv *LogView) focusSearch() { lv.focusSearchNext = true }

// reloadIfRefsMoved reloads only when refs moved: working tree edits don't
// change the log.
func (lv *LogView) reloadIfRefsMoved() {
	if v := lv.ws.Model.RefsVersion; v != lv.refsVersion {
		lv.refsVersion = v
		lv.reload()
	}
}

// slowLoading reports whether loading has taken long enough to say so.
func (lv *LogView) slowLoading() bool {
	return lv.loading && time.Since(lv.loadingFrom) >= loadingDelay
}

func (lv *LogView) filter() git.LogFilter {
	f := git.LogFilter{Refs: lv.branchFilter, Authors: lv.authorFilter, Paths: lv.pathFilter, Text: lv.grep}
	if lv.dateFilter >= 0 {
		f.Since = dateFilters[lv.dateFilter]
	}
	return f
}

func (lv *LogView) reload() {
	repo := lv.ws.Model.Repo
	filter := lv.filter()
	limit := lv.limit
	lv.loading = true
	lv.loadingFrom = time.Now()
	lv.logGen++
	gen := lv.logGen
	go func() {
		time.Sleep(loadingDelay)
		model.Post(func() {})
	}()
	type result struct {
		commits []git.Commit
		graph   []graph.Row
		head    string
		onHead  map[int]bool
		err     error
	}
	go func() {
		var r result
		r.commits, r.err = repo.Log(&filter, limit)
		if r.err == nil {
			r.graph = graph.Layout(r.commits, "")
			r.head, _ = repo.Head()
			r.onHead = reachableFrom(r.commits, r.head)
		}
		model.Post(func() {
			if gen != lv.logGen {
				return
			}
			lv.loading = false
			if r.err != nil {
				lv.commits, lv.graph, lv.cutGraph, lv.onHead = nil, nil, nil, nil
				lv.ws.Model.Fail(r.err)
			} else {
				lv.commits, lv.graph, lv.cutGraph, lv.onHead, lv.head = r.commits, r.graph, r.graph, r.onHead, r.head
				if len(lv.authorFilter) == 0 {
					lv.knownAuthors = lv.authors()
				}
			}
			lv.whole = ""
			lv.graphGen++
			lv.applyFilter()
			if id := lv.pendingReveal; id != "" {
				lv.pendingReveal = ""
				if lv.rowOf(id) >= 0 {
					lv.revealCommit(id)
					return
				}
			}
			if lv.selected != "" && slices.ContainsFunc(lv.commits, func(c git.Commit) bool { return c.ID == lv.selected }) {
				// Branches containing the commit may have changed.
				lv.selectedRow = lv.rowOf(lv.selected)
				lv.shownRow = lv.selectedRow
				lv.loadDetails()
			} else if len(lv.rows) > 0 {
				lv.selectRow(0)
			} else {
				lv.selectRow(-1)
			}
			lv.keepEdgesWhole()
		})
	}()
}

func (lv *LogView) rowOf(id string) int {
	return slices.IndexFunc(lv.rows, func(ix int) bool { return lv.commits[ix].ID == id })
}

// branchRow is a row of the branches tree: a group, a folder of branch
// names, or a branch.
type branchRow struct {
	key    string
	label  string
	depth  int
	folder bool
	branch *git.Branch
}

func (lv *LogView) rebuildBranches() {
	m := lv.ws.Model
	var rows []branchRow
	for _, b := range m.BranchesOf(git.RefLocal) {
		if b.IsHead {
			rows = append(rows, branchRow{key: "b:HEAD", label: "HEAD (" + b.Name + ")", branch: &b})
		}
	}
	for _, g := range []struct {
		label string
		kind  git.RefKind
	}{{i18n.T("Local"), git.RefLocal}, {i18n.T("Remote"), git.RefRemote}, {i18n.T("Tags"), git.RefTag}} {
		branches := m.BranchesOf(g.kind)
		if len(branches) == 0 {
			continue
		}
		group := "group:" + g.label
		rows = append(rows, branchRow{key: group, label: g.label, folder: true})
		if lv.branchClosed[group] {
			continue
		}
		byName := map[string]git.Branch{}
		var names []string
		for _, b := range branches {
			byName[b.Name] = b
			names = append(names, b.Name)
		}
		var walk func(nodes []*pathNode, depth int)
		walk = func(nodes []*pathNode, depth int) {
			for _, n := range nodes {
				key := "b:" + g.label + ":" + n.Path
				if n.IsDir {
					rows = append(rows, branchRow{key: key, label: n.Name, depth: depth, folder: true})
					if !lv.branchClosed[key] {
						walk(n.Children, depth+1)
					}
					continue
				}
				b := byName[n.Path]
				rows = append(rows, branchRow{key: key, label: n.Name, depth: depth, branch: &b})
			}
		}
		walk(pathTree(names), 1)
	}
	lv.branchRows = rows
	lv.branchSelected, lv.branchShown = -1, -1
}

func (lv *LogView) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(lv.query))
	// Git has already matched every message, bodies included.
	searchedByGit := lv.grep != "" && strings.ToLower(lv.grep) == query
	lv.rows = lv.rows[:0]
	for ix, c := range lv.commits {
		if query == "" || searchedByGit || strings.HasPrefix(c.ID, query) ||
			strings.Contains(strings.ToLower(c.Subject), query) || strings.Contains(strings.ToLower(c.Author), query) ||
			slices.ContainsFunc(c.Refs, func(r git.RefLabel) bool { return strings.Contains(strings.ToLower(r.Name), query) }) {
			lv.rows = append(lv.rows, ix)
		}
	}
	// Commits no longer shown leave the choice.
	shown := map[string]bool{}
	for _, ix := range lv.rows {
		shown[lv.commits[ix].ID] = true
	}
	for id := range lv.chosen.All() {
		if !shown[id] {
			lv.chosen.Remove(id)
		}
	}
	lv.selectedRow = lv.rowOf(lv.selected)
	lv.shownRow = lv.selectedRow
}

func (lv *LogView) isFiltered() bool { return strings.TrimSpace(lv.query) != "" }

// isHistoryFiltered reports filters git applies, which leave gaps in the
// history.
func (lv *LogView) isHistoryFiltered() bool {
	return lv.dateFilter >= 0 || len(lv.pathFilter) > 0 || len(lv.authorFilter) > 0
}

// togglePathFilter adds path to the paths shown, or takes it out.
func (lv *LogView) togglePathFilter(path string) {
	if ix := slices.Index(lv.pathFilter, path); ix >= 0 {
		lv.pathFilter = slices.Delete(lv.pathFilter, ix, ix+1)
	} else {
		lv.recentPaths = append([]string{path}, slices.DeleteFunc(lv.recentPaths, func(p string) bool { return p == path })...)
		if len(lv.recentPaths) > 8 {
			lv.recentPaths = lv.recentPaths[:8]
		}
		lv.pathFilter = append(lv.pathFilter, path)
	}
	lv.reload()
}

func (lv *LogView) choosePathFilter() {
	root := lv.ws.Model.Repo.Root()
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: lv.ws.Shell.Win, Title: i18n.T("Filter"), Directory: false})
		if err != nil || len(paths) == 0 {
			return
		}
		model.Post(func() {
			r, _ := filepath.EvalSymlinks(root)
			p, _ := filepath.EvalSymlinks(paths[0])
			rel, err := filepath.Rel(r, p)
			switch {
			case err != nil || strings.HasPrefix(rel, ".."):
				lv.ws.Shell.Toast("error", i18n.Tf("{} is not inside the repository", paths[0]))
			case rel == ".":
				lv.pathFilter = nil
				lv.reload()
			case !slices.Contains(lv.pathFilter, filepath.ToSlash(rel)):
				lv.togglePathFilter(filepath.ToSlash(rel))
			}
		})
	}()
}

// toggleAuthorFilter adds author to the authors shown, or takes them out.
func (lv *LogView) toggleAuthorFilter(author string) {
	if ix := slices.Index(lv.authorFilter, author); ix >= 0 {
		lv.authorFilter = slices.Delete(lv.authorFilter, ix, ix+1)
	} else {
		lv.authorFilter = append(lv.authorFilter, author)
	}
	lv.reload()
}

// authorChoices are the authors to choose from: those seen, plus any
// chosen that weren't.
func (lv *LogView) authorChoices() []string {
	authors := slices.Clone(lv.knownAuthors)
	for _, a := range lv.authorFilter {
		if !slices.Contains(authors, a) {
			authors = append(authors, a)
		}
	}
	return authors
}

// authors are the authors of the loaded commits, most active first.
func (lv *LogView) authors() []string {
	counts := map[string]int{}
	for _, c := range lv.commits {
		counts[c.Author]++
	}
	authors := slices.Collect(maps.Keys(counts))
	sort.Slice(authors, func(i, j int) bool {
		if counts[authors[i]] != counts[authors[j]] {
			return counts[authors[i]] > counts[authors[j]]
		}
		return authors[i] < authors[j]
	})
	return authors
}

// goTo selects the commit rev names, loading and unfiltering the log as far
// as it takes to show it.
func (lv *LogView) goTo(rev string) {
	repo := lv.ws.Model.Repo
	go func() {
		id, above, err := repo.LocateCommit(rev)
		model.Post(func() {
			switch {
			case err != nil:
				lv.ws.Model.Fail(err)
			case lv.rowOf(id) >= 0:
				lv.revealCommit(id)
			default:
				lv.query = ""
				// Far down, as an old tag is in a big history, the log
				// starts there rather than loading everything above it.
				lv.branchFilter = ""
				if above > farDown {
					lv.branchFilter = rev
				} else {
					lv.limit = max(lv.limit, above+100)
				}
				lv.authorFilter, lv.dateFilter, lv.pathFilter = nil, -1, nil
				lv.pendingReveal = id
				lv.reload()
			}
		})
	}()
}

func (lv *LogView) revealCommit(id string) {
	if row := lv.rowOf(id); row >= 0 {
		lv.list.ScrollTo(row, ui.Center)
		lv.chosen.Clear()
		lv.selectRow(row)
	}
}

// selectRow chooses row, -1 for none, and shows its details.
func (lv *LogView) selectRow(row int) {
	lv.selectedRow = row
	lv.shownRow = row
	id := ""
	if row >= 0 && row < len(lv.rows) {
		id = lv.commits[lv.rows[row]].ID
		if lv.chosen.Len() <= 1 {
			lv.chosen.Clear()
			lv.chosen.Add(id)
		}
	}
	if id == lv.selected && lv.details != nil {
		return
	}
	lv.selected = id
	lv.base = 0
	lv.loadDetails()
	lv.keepEdgesWhole()
}

// keepEdgesWhole draws the selected commit's long edges whole rather than
// cut short, to show where its arrows lead, as JetBrains does.
func (lv *LogView) keepEdgesWhole() {
	row := slices.IndexFunc(lv.commits, func(c git.Commit) bool { return c.ID == lv.selected })
	hasCut := false
	if row >= 0 {
		for _, r := range lv.cutGraph {
			for _, a := range r.Arrows {
				if a.OtherEnd >= 0 && (a.Child == row || a.OtherEnd == row) {
					hasCut = true
				}
			}
		}
	}
	whole := ""
	if hasCut {
		whole = lv.selected
	}
	if whole == lv.whole {
		return
	}
	lv.whole = whole
	lv.graphGen++
	if whole == "" {
		lv.graph = lv.cutGraph
		return
	}
	gen, commits := lv.graphGen, lv.commits
	go func() {
		g := graph.Layout(commits, whole)
		model.Post(func() {
			if gen == lv.graphGen {
				lv.graph = g
			}
		})
	}()
}

// chosenIDs are the commits chosen, newest first, when several are.
func (lv *LogView) chosenIDs() []string {
	if lv.chosen.Len() < 2 {
		return nil
	}
	var ids []string
	for _, ix := range lv.rows {
		if id := lv.commits[ix].ID; lv.chosen.Has(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (lv *LogView) loadDetails() {
	lv.detailsGen++
	if lv.cancelExtras != nil {
		// Finding the branches of the commit shown before can take seconds
		// on a big history; it no longer matters.
		lv.cancelExtras()
		lv.cancelExtras = nil
	}
	if lv.selected == "" {
		lv.details = nil
		lv.files = nil
		lv.fileNodes, lv.fileRows = nil, nil
		lv.diff.Clear()
		return
	}
	gen, id, base, repo := lv.detailsGen, lv.selected, lv.base, lv.ws.Model.Repo
	go func() {
		d, err := repo.CommitDetailsAgainst(id, base)
		model.Post(func() {
			if gen != lv.detailsGen {
				return
			}
			if err != nil {
				lv.ws.Model.Fail(err)
				return
			}
			same := lv.details != nil && lv.details.ID == d.ID && lv.details.Base == d.Base
			lv.details = d
			lv.loadExtras(d)
			lv.files = map[string]git.FileChange{}
			var paths []string
			for _, f := range d.Files {
				lv.files[f.Path] = f
				paths = append(paths, f.Path)
			}
			lv.fileNodes = pathTree(paths)
			lv.fileRows = flatten(lv.fileNodes, lv.fileClosed)
			if same {
				return
			}
			// Preview the first file, like JetBrains' diff preview.
			lv.fileRow = slices.IndexFunc(lv.fileRows, func(r treeRow) bool { return !r.Node.IsDir })
			lv.shownFile = ""
			if lv.fileRow < 0 {
				lv.diff.ShowMessage("", i18n.T("No changed files"))
			} else {
				lv.showFile(lv.fileRows[lv.fileRow].Node.Path)
			}
		})
	}()
}

// loadExtras finds the branches that contain the commit shown and checks
// its signature, after its files and diff show: on a big history they take
// a while.
func (lv *LogView) loadExtras(d *git.CommitDetails) {
	ctx, cancel := context.WithCancel(context.Background())
	lv.cancelExtras = cancel
	lv.extrasLoaded = false
	repo := lv.ws.Model.Repo
	// In a partial clone, download what the commit changed at once: the
	// diff of each of its files would wait seconds for the remote.
	go repo.Prefetch(ctx, d.BaseParent(), d.ID)
	go func() {
		branches, sig, err := repo.CommitExtras(ctx, d.ID)
		model.Post(func() {
			if ctx.Err() != nil || lv.details != d {
				return
			}
			if err == nil {
				d.Branches, d.Signature = branches, sig
				lv.extrasLoaded = true
			}
		})
	}()
}

func (lv *LogView) showFile(path string) {
	d := lv.details
	file, ok := lv.files[path]
	if d == nil || !ok {
		return
	}
	lv.shownFile = path
	repo := lv.ws.Model.Repo
	title := file.Path + "  (" + short(d.ID) + ")"
	if file.OrigPath != "" {
		title = file.OrigPath + " → " + title
	}
	lv.diff.Load(title, func() (string, error) { return repo.CommitFileDiff(d, &file) })
}

func (lv *LogView) loadMore() {
	if !lv.loading && len(lv.commits) >= lv.limit {
		lv.limit += settings.Read(func(s *settings.Settings) int { return s.LogPageSize })
		lv.reload()
	}
}

func (lv *LogView) View(c *ui.Context) {
	ui.SplitVerticalPercent(c.Key("log-main"), &lv.topPercent, func() {
		ui.Split(c.Key("log-columns"), &lv.branchesWidth, func() {
			lv.branchesPanel(c)
		}, func() {
			ui.SplitPercent(c.Key("log-right"), &lv.commitsPercent, func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					lv.toolbar(c)
					lv.commitList(c)
				})
			}, func() {
				lv.detailsPanel(c)
			}).Fill()
		}).Fill()
	}, func() {
		lv.diff.View(c)
	}).Fill()
}

func (lv *LogView) branchesPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Surface).Children(func() {
		panelHeader(c, i18n.T("Branches"))
		ui.List(c.Key("branches"), &lv.branchList, len(lv.branchRows), func(i int) {
			r := lv.branchRows[i]
			row := ui.Row(c.Key(r.key)).Height(22).PaddingStart(float32(6 + r.depth*12)).PaddingEnd(6).Gap(4)
			if r.branch != nil {
				b := *r.branch
				row.ContextMenu(func(menu *ui.Menu) { branchMenu(menu, lv.ws, b) })
			}
			if r.folder && row.Clicked() {
				lv.branchClosed[r.key] = !lv.branchClosed[r.key]
				lv.rebuildBranches()
			}
			row.Children(func() {
				name, color := "git-branch", t.TextMuted
				switch {
				case r.folder && lv.branchClosed[r.key]:
					name = "chevron-right"
				case r.folder:
					name = "chevron-down"
				case r.branch.Kind == git.RefTag:
					name = "tag"
				}
				if r.branch != nil && r.branch.IsHead {
					color = ui.Hex("#e0a33d")
				}
				ui.Icon(c, icon(name)).FontSize(t.FontSize - 1).TextColor(color)
				ui.Text(c, r.label).SingleLine().Shrink(1)
				if r.branch != nil && r.branch.Track != "" {
					muted(c, formatTrack(r.branch.Track)).SingleLine()
				}
			})
		}).Grow(1).PaddingX(6)
	})
	// A branch chosen in the tree takes the log to its commit.
	if lv.branchSelected != lv.branchShown {
		lv.branchShown = lv.branchSelected
		if lv.branchSelected >= 0 && lv.branchSelected < len(lv.branchRows) {
			if b := lv.branchRows[lv.branchSelected].branch; b != nil {
				if lv.rowOf(b.Target) >= 0 {
					lv.revealCommit(b.Target)
				} else {
					// Not loaded yet, as with an old tag.
					lv.goTo(b.Name)
				}
			}
		}
	}
}

func (lv *LogView) branchNames() []string {
	var names []string
	for _, b := range lv.ws.Model.Branches {
		if b.Kind == git.RefLocal || b.Kind == git.RefRemote {
			names = append(names, b.Name)
		}
	}
	return slices.Compact(names)
}

func (lv *LogView) toolbar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c.Key("log-toolbar")).Height(36).PaddingX(8).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		before := lv.query
		search := ui.SearchField(c.Key("search"), &lv.query).Placeholder(i18n.T("Text or hash")).Width(240).Radius(14).Bind(&lv.search)
		if lv.focusSearchNext {
			lv.focusSearchNext = false
			lv.search.Focus()
		}
		if lv.query != before {
			// A new search is over the loaded commits again.
			if lv.grep != "" && lv.grep != strings.TrimSpace(lv.query) {
				lv.grep = ""
				lv.reload()
			} else {
				lv.applyFilter()
			}
		}
		if search.Submitted() {
			// Enter goes to the matching commits, for the arrow keys.
			lv.list.Handle.Focus()
		}

		label := i18n.T("Branch: All")
		if lv.branchFilter != "" {
			label = i18n.Tf("Branch: {}", lv.branchFilter)
		}
		filterButton(c.Key("branch-filter"), label).Menu(func(menu *ui.Menu) {
			if menu.Item(i18n.T("All")).Checked(lv.branchFilter == "").Chosen() {
				lv.branchFilter = ""
				lv.reload()
			}
			menu.Separator()
			for _, name := range lv.branchNames() {
				if menu.Item(name).Checked(lv.branchFilter == name).Chosen() {
					lv.branchFilter = name
					lv.reload()
				}
			}
		})

		label = i18n.T("User: All")
		switch {
		case len(lv.authorFilter) == 1 && lv.authorFilter[0] == lv.me:
			label = i18n.T("User: Me")
		case len(lv.authorFilter) == 1:
			label = i18n.Tf("User: {}", lv.authorFilter[0])
		case len(lv.authorFilter) > 1:
			label = i18n.Tf("User: {} +{}", lv.authorFilter[0], len(lv.authorFilter)-1)
		}
		filterButton(c.Key("author-filter"), label).Menu(func(menu *ui.Menu) {
			if menu.Item(i18n.T("All")).Checked(len(lv.authorFilter) == 0).Chosen() {
				lv.authorFilter = nil
				lv.reload()
			}
			if lv.me != "" && menu.Item(i18n.Tf("Me ({})", lv.me)).Checked(slices.Contains(lv.authorFilter, lv.me)).Chosen() {
				lv.toggleAuthorFilter(lv.me)
			}
			menu.Separator()
			for _, a := range lv.authorChoices() {
				if a != lv.me && menu.Item(a).Checked(slices.Contains(lv.authorFilter, a)).Chosen() {
					lv.toggleAuthorFilter(a)
				}
			}
		})

		label = i18n.T("Date: All")
		if lv.dateFilter >= 0 {
			label = i18n.Tf("Date: {}", dateFilterLabel(lv.dateFilter))
		}
		filterButton(c.Key("date-filter"), label).Menu(func(menu *ui.Menu) {
			if menu.Item(i18n.T("All")).Checked(lv.dateFilter < 0).Chosen() {
				lv.dateFilter = -1
				lv.reload()
			}
			menu.Separator()
			for ix := range dateFilters {
				if menu.Item(dateFilterLabel(ix)).Checked(lv.dateFilter == ix).Chosen() {
					lv.dateFilter = ix
					lv.reload()
				}
			}
		})

		label = i18n.T("Paths: All")
		switch {
		case len(lv.pathFilter) == 1:
			label = i18n.Tf("Paths: {}", lv.pathFilter[0])
		case len(lv.pathFilter) > 1:
			label = i18n.Tf("Paths: {} +{}", lv.pathFilter[0], len(lv.pathFilter)-1)
		}
		filterButton(c.Key("path-filter"), label).Menu(func(menu *ui.Menu) {
			if menu.Item(i18n.T("All")).Checked(len(lv.pathFilter) == 0).Chosen() {
				lv.pathFilter = nil
				lv.reload()
			}
			paths := slices.Clone(lv.pathFilter)
			for _, p := range lv.recentPaths {
				if !slices.Contains(paths, p) {
					paths = append(paths, p)
				}
			}
			if len(paths) > 0 {
				menu.Separator()
			}
			for _, p := range paths {
				if menu.Item(p).Checked(slices.Contains(lv.pathFilter, p)).Chosen() {
					lv.togglePathFilter(p)
				}
			}
			menu.Separator()
			if menu.Item(i18n.T("Select File or Folder…")).Chosen() {
				lv.choosePathFilter()
			}
		})

		ui.Spacer(c)
		if lv.slowLoading() {
			ui.Row(c).Gap(4).Children(func() {
				ui.Spinner(c).Size(12, 12)
				muted(c, i18n.T("Loading commits…"))
			})
		}
		muted(c, i18n.Tf("{} commits", len(lv.rows))).SingleLine()
		if iconButton(c.Key("refresh"), "refresh-cw", i18n.T("Refresh")).Clicked() {
			lv.ws.Model.Refresh()
			lv.reload()
		}
	})
}

func (lv *LogView) commitList(c *ui.Context) {
	t := c.Theme()
	showGraph := !lv.isFiltered() && !lv.isHistoryFiltered()
	hasMore := (!lv.isFiltered() || lv.grep != "") && len(lv.commits) >= lv.limit
	// The text search only saw the commits loaded; git can look further.
	offerGrep := lv.isFiltered() && lv.grep == "" && len(lv.commits) >= lv.limit
	extra := 0
	if hasMore || offerGrep {
		extra = 1
	}
	// The rows in view share the graph's width, so their subjects line up.
	first, last := lv.list.Visible()
	lanes := 1
	for row := max(first, 0); row <= last && row < len(lv.rows); row++ {
		if ix := lv.rows[row]; ix < len(lv.graph) {
			lanes = max(lanes, lv.graph[ix].Width)
		}
	}
	graphWidth := float32(8)
	if showGraph {
		graphWidth = graphPadding*2 + laneWidth*float32(lanes)
	}
	list := ui.List(c.Key("commits"), &lv.list, len(lv.rows)+extra, func(row int) {
		if row >= len(lv.rows) {
			lv.moreRow(c, offerGrep)
			return
		}
		lv.commitRow(c, row, showGraph, graphWidth)
	}).Grow(1).PaddingX(6).MinHeight(0).Children(func() {
		if len(lv.rows) == 0 && lv.slowLoading() {
			ui.Row(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Spinner(c).Size(16, 16)
				muted(c, i18n.T("Loading commits…"))
			})
		}
	})
	list.ContextMenu(func(menu *ui.Menu) {
		if ids := lv.chosenIDs(); len(ids) > 0 {
			commitsMenu(menu, lv.ws, ids)
			return
		}
		if lv.selectedRow < 0 || lv.selectedRow >= len(lv.rows) {
			return
		}
		commit := lv.commits[lv.rows[lv.selectedRow]]
		isHead := slices.ContainsFunc(commit.Refs, func(r git.RefLabel) bool { return r.Kind == git.RefHead })
		commitMenu(menu, lv.ws, commit.ID, commit.Subject, isHead)
	})
	// The choice moved, by a click or the keys.
	if lv.selectedRow != lv.shownRow {
		row := lv.selectedRow
		lv.shownRow = row
		id := ""
		if row >= 0 && row < len(lv.rows) {
			id = lv.commits[lv.rows[row]].ID
		}
		if id != lv.selected {
			lv.selected = id
			lv.base = 0
			lv.loadDetails()
			lv.keepEdgesWhole()
		}
	}
	_ = t
}

// moreRow loads the next page as it scrolls into view, like JetBrains, or
// offers to search every commit message.
func (lv *LogView) moreRow(c *ui.Context, offerGrep bool) {
	t := c.Theme()
	text := i18n.Tf("Showing the first {} commits. Load more…", lv.limit)
	query := strings.TrimSpace(lv.query)
	switch {
	case lv.loading:
		text = i18n.T("Loading…")
	case offerGrep:
		text = i18n.Tf("Searched the first {} commits. Search every commit message for “{}”", lv.limit, query)
	}
	row := ui.Row(c.Key("more")).Height(rowHeight).Center().TextColor(t.Accent).Cursor(ui.CursorPointer)
	row.Children(func() { ui.Text(c, text).SingleLine() })
	if row.Clicked() {
		if offerGrep {
			lv.grep = query
			lv.reload()
		} else {
			lv.loadMore()
		}
	}
	if !offerGrep && !lv.loading {
		lv.loadMore()
	}
}

func (lv *LogView) commitRow(c *ui.Context, row int, showGraph bool, graphWidth float32) {
	t := c.Theme()
	ix := lv.rows[row]
	commit := &lv.commits[ix]
	isHead := lv.head == commit.ID
	// Only worth it when commits of other branches are listed too.
	bold := lv.branchFilter == "" && len(lv.onHead) < len(lv.commits) && lv.onHead[ix]
	selected := lv.chosen.Has(commit.ID)
	secondary := t.TextMuted
	if selected {
		secondary = t.Text
	}
	ui.Row(c.Key(commit.ID)).Height(rowHeight).Children(func() {
		g := ui.Box(c.Key("graph")).Width(graphWidth).Height(rowHeight)
		if showGraph && ix < len(lv.graph) {
			r := lv.graph[ix]
			g.Draw(func(p *ui.Painter, b ui.Rect) { paintGraph(p, b, &r, isHead, t.Background) })
			if len(r.Arrows) > 0 {
				lv.arrowTargets(g, &r, ix)
			}
		}
		ui.Row(c).Grow(1).Shrink(1).MinWidth(0).Gap(4).ClipX().Children(func() {
			for _, ref := range commit.Refs {
				bg, fg := refColors(ref.Kind, t)
				ui.Box(c).PaddingX(6).Height(17).Radius(8.5).Background(bg).Center().Children(func() {
					ui.Text(c, ref.Name).FontSize(t.FontSize - 2).FontWeight(600).TextColor(fg).SingleLine()
				})
			}
			subject := ui.Text(c, commit.Subject).SingleLine().Shrink(1)
			if bold {
				subject.FontWeight(600)
			}
		})
		ui.Text(c, commit.Author).SingleLine().Width(authorWidth).PaddingX(8).TextColor(secondary)
		ui.Text(c, formatTime(commit.Time)).SingleLine().Width(timeWidth).PaddingX(8).TextColor(secondary)
	})
}

// arrowTargets lets a long edge's arrow take you to the commit at its
// other end, and say which it is.
func (lv *LogView) arrowTargets(g ui.Element, r *graph.Row, ix int) {
	x, y, ok := g.PointerPosition()
	if !ok {
		return
	}
	nodeX := graphPadding + laneWidth*(float32(r.Node)+0.5)
	for _, a := range r.Arrows {
		from, tip := arrowEnds(&a)
		const slack = 3
		minX, maxX := nodeX+min(from[0], tip[0])-slack, nodeX+max(from[0], tip[0])+slack
		minY, maxY := rowHeight/2+min(from[1], tip[1])-slack, rowHeight/2+max(from[1], tip[1])+slack
		if x < minX || x > maxX || y < minY || y > maxY {
			continue
		}
		var target, tip2 string
		if a.OtherEnd >= 0 {
			other := lv.commits[a.OtherEnd]
			target, tip2 = other.ID, short(other.ID)+"  "+other.Subject
		} else {
			target = lv.commits[a.Child].Parents[a.Parent]
			tip2 = i18n.Tf("{} (not loaded yet)", short(target))
		}
		g.Tooltip(tip2).Cursor(ui.CursorPointer)
		if g.Clicked() {
			lv.goTo(target)
		}
		return
	}
	_ = ix
}

// paintGraph draws a row of the commit graph.
func paintGraph(p *ui.Painter, b ui.Rect, r *graph.Row, isHead bool, background ui.Color) {
	x := func(lane int) float32 { return b.X + graphPadding + laneWidth*(float32(lane)+0.5) }
	top, mid, bottom := b.Y, b.Y+b.H/2, b.Y+b.H
	for _, e := range r.Edges() {
		y0, y1 := top, mid
		if e.Half == graph.Bottom {
			y0, y1 = mid, bottom
		}
		line(p, x(e.From), y0, x(e.To), y1, laneColor(e.Color))
	}
	// Cut edges, as arrows out of the node or into it.
	cx, cy := x(r.Node), mid
	for _, a := range r.Arrows {
		from, tip := arrowEnds(&a)
		fx, fy, tx, ty := cx+from[0], cy+from[1], cx+tip[0], cy+tip[1]
		color := laneColor(a.Color)
		line(p, fx, fy, tx, ty, color)
		dx, dy := tx-fx, ty-fy
		length := sqrt(dx*dx + dy*dy)
		ux, uy := dx/length, dy/length
		bx, by := tx-4.5*ux, ty-4.5*uy
		var head ui.Path
		head.MoveTo(tx, ty).LineTo(bx-3.5*uy, by+3.5*ux).LineTo(bx+3.5*uy, by-3.5*ux).Close()
		p.FillPath(&head, color)
	}
	radius := float32(3.5)
	if isHead {
		radius = 4.5
	}
	color := laneColor(r.Color)
	var dot ui.Path
	dot.Circle(cx, cy, radius)
	if isHead {
		p.FillPath(&dot, background)
		p.StrokePath(&dot, 2, color)
	} else {
		p.FillPath(&dot, color)
	}
}

// line draws a 1.5 DIP line of the graph. Painter.Line fills the rectangle
// between its ends, which only draws lines that run straight across or
// down; slanted ones are stroked as paths.
func line(p *ui.Painter, x0, y0, x1, y1 float32, c ui.Color) {
	if x0 == x1 || y0 == y1 {
		p.Line(x0, y0, x1, y1, 1.5, c)
		return
	}
	var path ui.Path
	path.MoveTo(x0, y0).LineTo(x1, y1)
	p.StrokePath(&path, 1.5, c)
}

// arrowEnds is where a cut edge's arrow runs, from its tail to its tip,
// relative to the center of its node: out of the node and down, or down
// into it, fanning out right, left, then further right. Drawn on the node,
// so it's clear whose edge it is.
func arrowEnds(a *graph.Arrow) ([2]float32, [2]float32) {
	dx, dy := float32(13), float32(5)
	switch a.Fan {
	case 0:
		dx, dy = 10, 10
	case 1:
		dx, dy = -10, 10
	}
	gap := 6 / sqrt(dx*dx+dy*dy)
	if a.Half == graph.Bottom {
		return [2]float32{dx * gap, dy * gap}, [2]float32{dx, dy}
	}
	return [2]float32{dx, -dy}, [2]float32{dx * gap, -dy * gap}
}

func (lv *LogView) detailsPanel(c *ui.Context) {
	ui.SplitVerticalPercent(c.Key("log-details"), &lv.filesPercent, func() {
		ui.Column(c).Fill().Children(func() { lv.filesPanel(c) })
	}, func() {
		ui.Scroll(c.Key("commit-info")).Fill().Padding(12).Children(func() { lv.commitInfo(c) })
	}).Fill()
}

func (lv *LogView) filesPanel(c *ui.Context) {
	t := c.Theme()
	d := lv.details
	changed := 0
	if d != nil {
		changed = len(d.Files)
	}
	panelHeader(c, i18n.Tf("Changed Files ({})", changed))
	if d != nil && len(d.Parents) > 1 {
		// For a merge commit: which parent its changes are shown against.
		ui.Row(c.Key("parents")).PaddingX(8).PaddingY(4).Gap(4).Wrap().BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			muted(c, i18n.T("Compare with"))
			segments(c, func() {
				for ix, parent := range d.Parents {
					if segment(c.Key(ix), i18n.Tf("Parent {} ({})", ix+1, short(parent)), ix == d.Base).Clicked() && lv.base != ix {
						lv.base = ix
						lv.loadDetails()
					}
				}
			})
		})
	}
	ui.List(c.Key("files"), &lv.fileList, len(lv.fileRows), func(i int) {
		r := lv.fileRows[i]
		n := r.Node
		row := ui.Row(c.Key(n.Path)).Height(22).PaddingStart(float32(6 + r.Depth*12)).PaddingEnd(6).Gap(4)
		row.ContextMenu(func(menu *ui.Menu) { lv.fileMenu(menu, n) })
		if n.IsDir && row.Clicked() {
			lv.fileClosed[n.Path] = !lv.fileClosed[n.Path]
			lv.fileRows = flatten(lv.fileNodes, lv.fileClosed)
		}
		row.Children(func() {
			change, isFile := lv.files[n.Path]
			name, color := "folder", t.TextMuted
			switch {
			case isFile && !n.IsDir:
				name, color = "file", statusColor(change.Status, t)
			case !lv.fileClosed[n.Path]:
				name = "folder-open"
			}
			if i == lv.fileRow {
				// The status's color is unreadable on the selection's.
				color = t.AccentText
			}
			ui.Icon(c, icon(name)).FontSize(t.FontSize - 1).TextColor(color)
			ui.Text(c, n.Name).SingleLine().TextColor(color).Shrink(1)
			if isFile && change.OrigPath != "" {
				muted(c, "from "+change.OrigPath).SingleLine().Shrink(1)
			}
		})
	}).Grow(1).PaddingX(6)
	if lv.fileRow >= 0 && lv.fileRow < len(lv.fileRows) {
		if n := lv.fileRows[lv.fileRow].Node; !n.IsDir && n.Path != lv.shownFile {
			lv.showFile(n.Path)
		}
	}
}

// fileMenu is the menu of a file, or every file under a folder, of the
// commit shown.
func (lv *LogView) fileMenu(menu *ui.Menu, n *pathNode) {
	d := lv.details
	if d == nil {
		return
	}
	ws, m := lv.ws, lv.ws.Model
	rev, base := d.ID, d.BaseParent()
	var chosen []git.FileChange
	for _, f := range d.Files {
		if f.Path == n.Path || n.IsDir && strings.HasPrefix(f.Path, n.Path+"/") {
			chosen = append(chosen, f)
		}
	}
	var paths []string
	for _, f := range chosen {
		paths = append(paths, f.Path)
	}
	for _, f := range chosen {
		if f.OrigPath != "" {
			paths = append(paths, f.OrigPath)
		}
	}
	count := len(chosen)
	if len(paths) > 0 {
		if menu.Item(i18n.T("Cherry-Pick Selected Changes")).Chosen() {
			ws.run(i18n.Tf("Cherry-pick {} from {}", fileCount(count), short(rev)), func(r *git.Repo) (string, error) {
				return r.ApplyCommitChanges(rev, base, paths, false)
			})
		}
		if menu.Item(i18n.T("Revert Selected Changes")).Chosen() {
			ws.run(i18n.Tf("Revert {} from {}", fileCount(count), short(rev)), func(r *git.Repo) (string, error) {
				return r.ApplyCommitChanges(rev, base, paths, true)
			})
		}
	}
	change, ok := lv.files[n.Path]
	if !ok || n.IsDir {
		return
	}
	path := change.Path
	menu.Separator()
	if menu.Item(i18n.T("Show History")).Chosen() {
		showHistory(ws, path)
	}
	if menu.Item(i18n.T("Filter the Log by This File")).Chosen() {
		lv.pathFilter = nil
		lv.togglePathFilter(path)
	}
	if menu.Item(i18n.T("Compare with Local")).Chosen() {
		openWorktreeDiff(ws, rev, "", path)
	}
	if menu.Item(i18n.T("Get from Revision")).Chosen() {
		run := func() {
			ws.run(i18n.Tf("Get {} from {}", path, short(rev)), func(r *git.Repo) (string, error) { return r.GetFromRevision(rev, path) })
		}
		if slices.ContainsFunc(m.Status, func(e git.StatusEntry) bool { return e.Path == path }) {
			ws.Shell.Confirm(i18n.T("Get from Revision"), i18n.Tf("{} has local changes. Replace it with its version in {}?", path, short(rev)), i18n.T("Replace"), run)
		} else {
			run()
		}
	}
	if change.Status == 'D' {
		return
	}
	if m.HasWeb {
		url := m.Web.FileURL(rev, path, 0)
		if menu.Item(i18n.Tf("Open on {}", m.Web.Name())).Chosen() {
			mygo.Shell.OpenExternal(url)
		}
		if menu.Item(i18n.T("Copy Link to File")).Chosen() {
			mygo.Clipboard.WriteText(url)
		}
	}
	if menu.Item(i18n.T("Annotate at This Revision")).Chosen() {
		annotate(ws, rev, path)
	}
}

func (lv *LogView) commitInfo(c *ui.Context) {
	t := c.Theme()
	d := lv.details
	if d == nil {
		muted(c, i18n.T("No commit selected"))
		return
	}
	ui.Column(c).Gap(8).Selectable().Children(func() {
		ui.Text(c, d.Message).FontWeight(600)
		ui.Column(c).Gap(2).TextColor(t.TextMuted).Children(func() {
			ui.Text(c, short(d.ID)+" "+d.Author)
			muted(c, "<"+d.Email+"> on "+formatFullTime(d.AuthorTime))
		})
		if d.Committer != d.Author || d.CommitTime != d.AuthorTime {
			muted(c, i18n.Tf("Committed by {} on {}", d.Committer, formatFullTime(d.CommitTime)))
		}
		if len(d.Parents) > 1 {
			var parents []string
			for _, p := range d.Parents {
				parents = append(parents, short(p))
			}
			muted(c, i18n.Tf("Merge of {}", strings.Join(parents, ", ")))
		}
		if !lv.extrasLoaded {
			muted(c, i18n.T("Finding the branches that contain it…"))
			return
		}
		branches := i18n.Tf("In {} branches: {}", len(d.Branches), strings.Join(d.Branches, ", "))
		if len(d.Branches) > 6 {
			branches = i18n.Tf("In {} branches: {}", len(d.Branches), strings.Join(d.Branches[:6], ", ")+", …")
		}
		muted(c, branches)
		signatureLine(c, d.Signature)
	})
}

// signatureLine says how a commit's signature checks out.
func signatureLine(c *ui.Context, s git.Signature) {
	t := c.Theme()
	var text string
	color := t.TextMuted
	switch s.Kind {
	case git.Unsigned:
		return
	case git.SignatureGood:
		text, color = i18n.Tf("Signed by {} (verified)", s.Signer), t.Success
	case git.SignatureUntrusted:
		text, color = i18n.Tf("Signed by {} (key not trusted)", s.Signer), t.Warning
	case git.SignatureBad:
		text, color = i18n.T("Bad signature"), t.Danger
	case git.SignatureExpired:
		text, color = i18n.T("Signature or key expired or revoked"), t.Warning
	default:
		text = i18n.T("Signed, but the signature can't be checked")
	}
	ui.Text(c, text).FontSize(t.FontSize - 1).TextColor(color)
}

func formatTrack(track string) string {
	var parts []string
	for _, part := range strings.Split(track, ", ") {
		if n, ok := strings.CutPrefix(part, "ahead "); ok {
			part = "↑" + n
		} else if n, ok := strings.CutPrefix(part, "behind "); ok {
			part = "↓" + n
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// reachableFrom are the indices of commits that head reaches through the
// parents listed.
func reachableFrom(commits []git.Commit, head string) map[int]bool {
	index := make(map[string]int, len(commits))
	for ix, c := range commits {
		index[c.ID] = ix
	}
	reached := map[int]bool{}
	start, ok := index[head]
	if !ok {
		return reached
	}
	stack := []int{start}
	for len(stack) > 0 {
		ix := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if reached[ix] {
			continue
		}
		reached[ix] = true
		for _, p := range commits[ix].Parents {
			if pix, ok := index[p]; ok {
				stack = append(stack, pix)
			}
		}
	}
	return reached
}
