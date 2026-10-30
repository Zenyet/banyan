// Package model holds the shared state of a repository. Every git operation
// goes through here so that all views refresh together afterwards.
//
// A Model's fields are read and changed on the main thread only; git runs
// on goroutines, whose results come back through Post.
package model

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Zenyet/banyan/internal/changelists"
	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

// Post runs fn on the main thread and redraws the windows; the app sets it.
// It returns without waiting.
var Post = func(fn func()) { fn() }

type EventKind int

const (
	// Changed means refs, HEAD or the working tree may have changed.
	Changed EventKind = iota
	OpSucceeded
	OpFailed
	// Conflicts means the operation Text stopped with merge conflicts.
	Conflicts
	// MessageRestored carries an undone commit's message, for the next
	// commit.
	MessageRestored
	// CheckoutBlocked means a checkout of Target was refused because it
	// would overwrite local changes.
	CheckoutBlocked
)

type Event struct {
	Kind   EventKind
	Text   string
	Target string
	Remote bool
}

// Activity is work going on in the background that can take a while on a
// big repository, shown once it has.
type Activity int

const (
	Refreshing Activity = iota
	IndexingHistory
	IndexingPaths
)

func (a Activity) Label() string {
	switch a {
	case IndexingHistory:
		return i18n.T("Indexing history…")
	case IndexingPaths:
		return i18n.T("Indexing file history…")
	}
	return i18n.T("Checking for changes…")
}

func (a Activity) Detail() string {
	if a == Refreshing {
		return i18n.T("Reading the status of the work tree and the branches.")
	}
	return i18n.T("Writing git's commit-graph, once per repository, so the log and file history load quickly.")
}

// activityDelay is how long work goes on before it's shown, so quick work
// doesn't flicker.
const activityDelay = 400 * time.Millisecond

type activity struct {
	kind  Activity
	since time.Time
}

type Model struct {
	Repo          *git.Repo
	Branches      []git.Branch
	CurrentBranch string
	State         git.RepoState
	// Operation is the stopped merge, rebase, cherry-pick or revert.
	Operation *git.Operation
	Status    []git.StatusEntry
	Stashes   []git.Stash
	// Changelists sort local changes into named lists, kept in the git
	// directory.
	Changelists *changelists.Changelists
	Shelves     []git.Shelf
	// Web is where the repository's commits and files can be seen on the
	// web.
	Web    git.WebRemote
	HasWeb bool
	// Favorites are the full names of the branches marked as favorites.
	Favorites []string
	// RecentBranches are local branches checked out lately, most recent
	// first.
	RecentBranches []string
	// Busy is the label of the operation that is running, if any.
	Busy string
	// RefsVersion is bumped whenever HEAD or any ref moves, so views know
	// to reload the log.
	RefsVersion uint64
	// Loaded is set once the first refresh is in.
	Loaded bool

	refsKey        string
	refreshing     bool
	refreshPending bool
	statusPending  bool
	refreshed      time.Time
	activities     []activity
	graphUpdated   time.Time
	indexRefreshed time.Time
	listeners      map[int]func(Event)
	nextListener   int
	ctx            context.Context
	cancel         context.CancelFunc
	closeOnce      sync.Once
}

func New(repo *git.Repo) *Model {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{
		Repo:        repo,
		Changelists: changelists.Load(repo.GitDir()),
		listeners:   map[int]func(Event){},
		ctx:         ctx,
		cancel:      cancel,
	}
	m.Refresh()
	m.watch()
	m.maintain(true)
	go m.autoFetch()
	return m
}

// Close stops the model's background work, once its last view is gone.
func (m *Model) Close() { m.closeOnce.Do(m.cancel) }

// Subscribe calls fn with each event, on the main thread; off stops it.
func (m *Model) Subscribe(fn func(Event)) (off func()) {
	id := m.nextListener
	m.nextListener++
	m.listeners[id] = fn
	return func() { delete(m.listeners, id) }
}

func (m *Model) emit(e Event) {
	ids := make([]int, 0, len(m.listeners))
	for id := range m.listeners {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if fn, ok := m.listeners[id]; ok {
			fn(e)
		}
	}
}

// background runs work on a goroutine and then on the main thread, unless
// the model was closed meanwhile.
func background[T any](m *Model, work func() T, then func(T)) {
	go func() {
		v := work()
		Post(func() {
			if m.ctx.Err() == nil {
				then(v)
			}
		})
	}()
}

func (m *Model) Conflicts() []git.StatusEntry {
	var out []git.StatusEntry
	for _, e := range m.Status {
		if e.IsConflicted() {
			out = append(out, e)
		}
	}
	return out
}

func (m *Model) BranchesOf(kind git.RefKind) []git.Branch {
	var out []git.Branch
	for _, b := range m.Branches {
		if b.Kind == kind {
			out = append(out, b)
		}
	}
	return out
}

func (m *Model) IsFavorite(b *git.Branch) bool {
	return slices.Contains(m.Favorites, b.FullName())
}

func (m *Model) SetFavorite(b *git.Branch, favorite bool) {
	full := b.FullName()
	if err := m.Repo.SetFavoriteBranch(full, favorite); err != nil {
		m.emit(Event{Kind: OpFailed, Text: err.Error()})
		return
	}
	m.Favorites = slices.DeleteFunc(m.Favorites, func(f string) bool { return f == full })
	if favorite {
		m.Favorites = append(m.Favorites, full)
	}
}

// Activity is the background work to show, if any has gone on for a moment.
func (m *Model) Activity() (Activity, bool) {
	best, found := Activity(0), false
	for _, a := range m.activities {
		if time.Since(a.since) >= activityDelay && (!found || a.kind < best) {
			best, found = a.kind, true
		}
	}
	return best, found
}

func (m *Model) startActivity(kind Activity) {
	m.activities = append(m.activities, activity{kind, time.Now()})
	go func() {
		time.Sleep(activityDelay)
		Post(func() {})
	}()
}

func (m *Model) endActivity(kind Activity) {
	if ix := slices.IndexFunc(m.activities, func(a activity) bool { return a.kind == kind }); ix >= 0 {
		m.activities = slices.Delete(m.activities, ix, ix+1)
	}
}

// refreshIndex refreshes the file times cached in the index in the
// background, at most once a minute.
func (m *Model) refreshIndex() {
	if time.Since(m.indexRefreshed) < time.Minute {
		return
	}
	m.indexRefreshed = time.Now()
	repo := m.Repo
	go repo.RefreshIndex()
}

// maintain keeps git's own indexes helping on big repositories, off the
// main thread: the commit-graph (at most every 30 seconds), and on opening
// the file times cached in the index.
func (m *Model) maintain(opening bool) {
	if time.Since(m.graphUpdated) < 30*time.Second {
		return
	}
	// A first commit-graph of a big history is still being written.
	if slices.ContainsFunc(m.activities, func(a activity) bool { return a.kind != Refreshing }) {
		return
	}
	m.graphUpdated = time.Now()
	repo := m.Repo
	go func() {
		// Failing only means history stays slower; nothing to report.
		if repo.HasCommitGraph() {
			repo.UpdateCommitGraph()
		} else {
			Post(func() { m.startActivity(IndexingHistory) })
			written := repo.WriteQuickCommitGraph() == nil
			Post(func() {
				m.endActivity(IndexingHistory)
				if written && m.ctx.Err() == nil {
					// The log can be put in date order quickly now.
					m.RefsVersion++
					m.emit(Event{Kind: Changed})
					m.startActivity(IndexingPaths)
				}
			})
			if written {
				repo.AddChangedPaths()
				Post(func() { m.endActivity(IndexingPaths) })
			}
		}
		if opening {
			repo.RefreshIndex()
		}
	}()
}

// autoFetch fetches in the background as often as the settings ask.
func (m *Model) autoFetch() {
	last := time.Now()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
		minutes := settings.Read(func(s *settings.Settings) int { return s.AutoFetchMinutes })
		if minutes == 0 || time.Since(last) < time.Duration(minutes)*time.Minute {
			continue
		}
		// Leave the repository alone while an operation runs.
		idle := make(chan bool, 1)
		Post(func() { idle <- m.Busy == "" })
		if !<-idle {
			continue
		}
		last = time.Now()
		if m.Repo.FetchQuietly() == nil {
			Post(func() { m.Refresh() })
		}
	}
}

// RefreshIfStale refreshes when coming back to the repository, like
// JetBrains does on frame activation, unless it was read just now.
func (m *Model) RefreshIfStale() {
	if !m.refreshing && time.Since(m.refreshed) > 2*time.Second {
		m.Refresh()
	}
}

type snapshot struct {
	head           string
	branches       []git.Branch
	currentBranch  string
	state          git.RepoState
	operation      *git.Operation
	status         []git.StatusEntry
	stashes        []git.Stash
	shelves        []git.Shelf
	web            git.WebRemote
	hasWeb         bool
	favorites      []string
	recentBranches []string
	// slowStatus means reading the status took long enough that the
	// index's cached file times are worth refreshing.
	slowStatus bool
}

// Refresh reads the repository again. A refresh asked for while one runs
// waits for it, and several of them become one.
func (m *Model) Refresh() {
	if m.refreshing {
		m.refreshPending = true
		return
	}
	m.refreshing = true
	m.startActivity(Refreshing)
	repo := m.Repo
	background(m, func() snapshot {
		started := time.Now()
		status, _ := repo.Status()
		var s snapshot
		s.slowStatus = time.Since(started) > 100*time.Millisecond
		s.status = status
		s.head, _ = repo.Head()
		s.branches, _ = repo.Branches()
		s.currentBranch, _ = repo.CurrentBranch()
		s.state = repo.State()
		s.operation = repo.Operation()
		s.stashes, _ = repo.Stashes()
		s.shelves = repo.Shelves()
		s.web, s.hasWeb = repo.WebRemote()
		s.favorites = repo.FavoriteBranches()
		s.recentBranches = repo.RecentBranches(6)
		return s
	}, func(s snapshot) {
		key := s.head
		for _, b := range s.branches {
			key += fmt.Sprintf("\x00%d:%s=%s", b.Kind, b.Name, b.Target)
		}
		if key != m.refsKey {
			// New commits or refs: add them to the commit-graph.
			if m.refsKey != "" {
				m.maintain(false)
			}
			m.refsKey = key
			m.RefsVersion++
		}
		m.Branches = s.branches
		m.CurrentBranch = s.currentBranch
		m.State = s.state
		m.Operation = s.operation
		m.Status = s.status
		m.Stashes = s.stashes
		m.Shelves = s.shelves
		m.Web, m.HasWeb = s.web, s.hasWeb
		m.Favorites = s.favorites
		m.RecentBranches = s.recentBranches
		m.Loaded = true
		m.statusRead(s.slowStatus)
		m.refreshed = time.Now()
		m.statusPending = false
		m.refreshDone()
	})
}

// refreshStatus reads only the status again, for edits in the work tree:
// running the dozen other git commands of a refresh each time adds up.
func (m *Model) refreshStatus() {
	if m.refreshing {
		m.statusPending = true
		return
	}
	m.refreshing = true
	m.startActivity(Refreshing)
	repo := m.Repo
	type result struct {
		status []git.StatusEntry
		err    error
		slow   bool
	}
	background(m, func() result {
		started := time.Now()
		status, err := repo.Status()
		return result{status, err, time.Since(started) > 100*time.Millisecond}
	}, func(r result) {
		if r.err == nil {
			m.Status = r.status
			m.statusRead(r.slow)
		}
		m.refreshDone()
	})
}

func (m *Model) statusRead(slow bool) {
	if slow {
		m.refreshIndex()
	}
	var tracked []string
	for _, e := range m.Status {
		if !e.IsUntracked() {
			tracked = append(tracked, e.Path)
		}
	}
	if m.Changelists.Sync(tracked) {
		m.saveChangelists()
	}
	m.emit(Event{Kind: Changed})
}

// refreshDone runs the refresh asked for meanwhile, if any.
func (m *Model) refreshDone() {
	m.refreshing = false
	m.endActivity(Refreshing)
	switch {
	case m.refreshPending:
		m.refreshPending = false
		m.Refresh()
	case m.statusPending:
		m.statusPending = false
		m.refreshStatus()
	}
}

// Run runs a git operation off the main thread, reports the outcome and
// refreshes. The repository it gets records its commands in the Git
// console.
func Run[T any](m *Model, label string, op func(*git.Repo) (T, error)) {
	RunThen(m, label, op, nil, nil)
}

// RunThen is like Run, handing the operation's result to onOK, and the
// error to onError, which returns the event to report instead of the usual
// failure.
func RunThen[T any](m *Model, label string, op func(*git.Repo) (T, error), onOK func(T), onError func(error) Event) {
	if m.Busy != "" {
		m.emit(Event{Kind: OpFailed, Text: i18n.Tf("Another operation is running: {}", m.Busy)})
		return
	}
	m.Busy = label
	repo := m.Repo.Recorded()
	type result struct {
		value      T
		err        error
		conflicted bool
	}
	background(m, func() result {
		v, err := op(repo)
		conflicted := false
		if err != nil {
			status, _ := repo.Status()
			conflicted = slices.ContainsFunc(status, func(e git.StatusEntry) bool { return e.IsConflicted() })
		}
		return result{v, err, conflicted}
	}, func(r result) {
		m.Busy = ""
		switch {
		case r.err == nil:
			m.emit(Event{Kind: OpSucceeded, Text: i18n.Tf("{}: done", label)})
			if onOK != nil {
				onOK(r.value)
			}
		case r.conflicted:
			m.emit(Event{Kind: Conflicts, Text: label})
		case onError != nil:
			m.emit(onError(r.err))
		default:
			m.emit(Event{Kind: OpFailed, Text: i18n.Tf("{} failed", label) + "\n" + r.err.Error()})
		}
		m.Refresh()
	})
}

// Fail reports an error as a failed operation would.
func (m *Model) Fail(err error) { m.emit(Event{Kind: OpFailed, Text: err.Error()}) }

// ChangeLists changes the changelists and keeps them; a refused change is
// reported.
func (m *Model) ChangeLists(change func(*changelists.Changelists) error) {
	if err := change(m.Changelists); err != nil {
		m.Fail(err)
		return
	}
	m.saveChangelists()
	m.emit(Event{Kind: Changed})
}

func (m *Model) saveChangelists() { m.Changelists.Save(m.Repo.GitDir()) }

// Shelve shelves the changes of paths, remembering their changelist.
func (m *Model) Shelve(name string, paths []string) {
	list := m.Changelists.Active()
	if len(paths) > 0 {
		first := m.Changelists.ListOf(paths[0])
		if !slices.ContainsFunc(paths, func(p string) bool { return m.Changelists.ListOf(p) != first }) {
			list = first
		}
	}
	Run(m, i18n.Tf("Shelve {}", name), func(r *git.Repo) (*git.Shelf, error) { return r.Shelve(name, list, paths) })
}

// Unshelve unshelves all of shelf id, or paths of it, back into its
// changelist.
func (m *Model) Unshelve(id string, paths []string) {
	type result struct {
		shelf *git.Shelf
		files []string
	}
	RunThen(m, i18n.T("Unshelve"), func(r *git.Repo) (result, error) {
		shelf, files, err := r.Unshelve(id, paths)
		return result{shelf, files}, err
	}, func(r result) {
		m.ChangeLists(func(c *changelists.Changelists) error { return c.MoveTo(r.files, r.shelf.Changelist) })
	}, func(err error) Event {
		return Event{Kind: OpFailed, Text: i18n.T("Unshelve failed") + "\n" + err.Error()}
	})
}

// UndoCommit undoes the last commit, keeping its changes and offering its
// message again.
func (m *Model) UndoCommit() {
	RunThen(m, i18n.T("Undo commit"), (*git.Repo).UndoCommit, func(message string) {
		m.emit(Event{Kind: MessageRestored, Text: message})
	}, func(err error) Event {
		return Event{Kind: OpFailed, Text: i18n.T("Undo commit failed") + "\n" + err.Error()}
	})
}

// Checkout checks out a branch or revision, offering Smart Checkout when
// local changes are in the way.
func (m *Model) Checkout(target string, remote bool) {
	label := i18n.Tf("Checkout {}", ShortRef(target))
	RunThen(m, label, func(r *git.Repo) (string, error) { return r.CheckoutAny(target, remote) }, nil, func(err error) Event {
		if git.IsBlockedByLocalChanges(err) {
			return Event{Kind: CheckoutBlocked, Target: target, Remote: remote, Text: err.Error()}
		}
		return Event{Kind: OpFailed, Text: i18n.Tf("{} failed", label) + "\n" + err.Error()}
	})
}

// ShortRef is a ref or commit as menus name it: a commit id cut to eight
// characters, a branch as it is.
func ShortRef(target string) string {
	if len(target) == 40 {
		return git.Short(target)
	}
	return target
}
