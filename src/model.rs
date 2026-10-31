//! Shared repository state. Every git operation goes through here so that all
//! views refresh together afterwards.

use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::Result;
use gpui_kit::*;

use crate::changelists::Changelists;
use crate::git::{Branch, Operation, RefKind, Repo, RepoState, Shelf, StatusEntry, Stash, WebRemote};
use crate::settings::Settings;
use crate::watcher::RepoWatcher;
use crate::i18n::tr;

pub enum RepoEvent {
    /// Refs, HEAD or the working tree may have changed.
    Changed,
    OpSucceeded(SharedString),
    OpFailed(SharedString),
    /// The operation with this label stopped with merge conflicts.
    Conflicts(SharedString),
    /// An undone commit's message, for the next commit.
    MessageRestored(String),
    /// A checkout was refused because it would overwrite local changes.
    CheckoutBlocked {
        target: String,
        remote: bool,
        message: SharedString,
    },
}

pub struct RepoModel {
    pub repo: Arc<Repo>,
    pub branches: Vec<Branch>,
    pub current_branch: Option<String>,
    pub state: RepoState,
    /// The stopped merge, rebase, cherry-pick or revert.
    pub operation: Option<Operation>,
    pub status: Vec<StatusEntry>,
    pub stashes: Vec<Stash>,
    /// Local changes sorted into named lists, kept in the git directory.
    pub changelists: Changelists,
    pub shelves: Vec<Shelf>,
    /// Where the repository's commits and files can be seen on the web.
    pub web: Option<WebRemote>,
    /// Full names of the branches marked as favorites.
    pub favorites: Vec<String>,
    /// Local branches checked out lately, most recent first.
    pub recent_branches: Vec<String>,
    /// Label of the operation that is running, if any.
    pub busy: Option<SharedString>,
    /// Bumped whenever HEAD or any ref moves, so views know to reload the log.
    pub refs_version: u64,
    refs_key: Option<RefsKey>,
    refresh_task: Option<Task<()>>,
    /// Another refresh was asked for while one ran; it runs once that ends.
    refresh_pending: bool,
    status_pending: bool,
    refreshed: Option<Instant>,
    activities: Vec<(Activity, Instant)>,
    /// When git's commit-graph was last brought up to date.
    graph_updated: Option<Instant>,
    index_refreshed: Option<Instant>,
    _maintenance: Option<Task<()>>,
    _watch_task: Option<Task<()>>,
    _auto_fetch_task: Task<()>,
}

/// HEAD and every ref with its target.
type RefsKey = (Option<String>, Vec<(String, String)>);

impl EventEmitter<RepoEvent> for RepoModel {}

/// Work going on in the background that can take a while on a big
/// repository, shown once it has.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Activity {
    Refreshing,
    IndexingHistory,
    IndexingPaths,
}

impl Activity {
    pub fn label(self) -> &'static str {
        match self {
            Activity::Refreshing => tr!("Checking for changes…"),
            Activity::IndexingHistory => tr!("Indexing history…"),
            Activity::IndexingPaths => tr!("Indexing file history…"),
        }
    }

    pub fn detail(self) -> &'static str {
        match self {
            Activity::Refreshing => tr!("Reading the status of the work tree and the branches."),
            Activity::IndexingHistory | Activity::IndexingPaths => {
                tr!("Writing git's commit-graph, once per repository, so the log and file history load quickly.")
            }
        }
    }
}

/// How long work goes on before it's shown, so quick work doesn't flicker.
const ACTIVITY_DELAY: Duration = Duration::from_millis(400);

struct Snapshot {
    head: Option<String>,
    branches: Vec<Branch>,
    current_branch: Option<String>,
    state: RepoState,
    operation: Option<Operation>,
    status: Vec<StatusEntry>,
    stashes: Vec<Stash>,
    shelves: Vec<Shelf>,
    web: Option<WebRemote>,
    favorites: Vec<String>,
    recent_branches: Vec<String>,
    /// Reading the status took long enough that the index's cached file
    /// times are worth refreshing.
    slow_status: bool,
}

impl RepoModel {
    pub fn new(repo: Repo, cx: &mut Context<Self>) -> Self {
        let changelists = repo.git_dir().map(|dir| Changelists::load(&dir)).unwrap_or_default();
        let mut this = Self {
            repo: Arc::new(repo),
            branches: Vec::new(),
            current_branch: None,
            state: RepoState::Clean,
            operation: None,
            status: Vec::new(),
            stashes: Vec::new(),
            changelists,
            shelves: Vec::new(),
            web: None,
            favorites: Vec::new(),
            recent_branches: Vec::new(),
            busy: None,
            refs_version: 0,
            refs_key: None,
            refresh_task: None,
            refresh_pending: false,
            status_pending: false,
            refreshed: None,
            activities: Vec::new(),
            graph_updated: None,
            index_refreshed: None,
            _maintenance: None,
            _watch_task: None,
            _auto_fetch_task: Self::auto_fetch(cx),
        };
        this.refresh(cx);
        this.watch(cx);
        this.maintain(true, cx);
        this
    }

    /// Refresh the file times cached in the index in the background, at most
    /// once a minute.
    fn refresh_index(&mut self, cx: &mut Context<Self>) {
        if self.index_refreshed.is_some_and(|at| at.elapsed() < Duration::from_secs(60)) {
            return;
        }
        self.index_refreshed = Some(Instant::now());
        let repo = self.repo.clone();
        cx.background_executor().spawn(async move { repo.refresh_index().ok() }).detach();
    }

    /// Keep git's own indexes helping on big repositories, off the main
    /// thread: the commit-graph (at most every 30 seconds), and on opening
    /// the file times cached in the index.
    fn maintain(&mut self, opening: bool, cx: &mut Context<Self>) {
        if self.graph_updated.is_some_and(|at| at.elapsed() < Duration::from_secs(30)) {
            return;
        }
        // A first commit-graph of a big history is still being written.
        if self.activities.iter().any(|(a, _)| *a != Activity::Refreshing) {
            return;
        }
        self.graph_updated = Some(Instant::now());
        let repo = self.repo.clone();
        self._maintenance = Some(cx.spawn(async move |this, cx| {
            let executor = cx.background_executor().clone();
            let run = |f: Box<dyn FnOnce(&Repo) -> bool + Send>| {
                let repo = repo.clone();
                executor.spawn(async move { f(&repo) })
            };
            // Failing only means history stays slower; nothing to report.
            if run(Box::new(|repo| repo.has_commit_graph())).await {
                run(Box::new(|repo| repo.update_commit_graph().is_ok())).await;
            } else {
                this.update(cx, |this, cx| this.start_activity(Activity::IndexingHistory, cx)).ok();
                let written = run(Box::new(|repo| repo.write_quick_commit_graph().is_ok())).await;
                this.update(cx, |this, cx| {
                    this.end_activity(Activity::IndexingHistory, cx);
                    if written {
                        // The log can be put in date order quickly now.
                        this.refs_version += 1;
                        cx.emit(RepoEvent::Changed);
                        this.start_activity(Activity::IndexingPaths, cx);
                    }
                })
                .ok();
                if written {
                    run(Box::new(|repo| repo.add_changed_paths().is_ok())).await;
                    this.update(cx, |this, cx| this.end_activity(Activity::IndexingPaths, cx)).ok();
                }
            }
            if opening {
                run(Box::new(|repo| repo.refresh_index().is_ok())).await;
            }
        }));
    }

    /// Fetch in the background as often as the settings ask.
    fn auto_fetch(cx: &mut Context<Self>) -> Task<()> {
        cx.spawn(async move |this, cx| {
            let mut last = Instant::now();
            loop {
                cx.background_executor().timer(Duration::from_secs(30)).await;
                let minutes = Settings::read(|s| s.auto_fetch_minutes);
                if minutes == 0 || last.elapsed() < Duration::from_secs(u64::from(minutes) * 60) {
                    continue;
                }
                // Leave the repository alone while an operation runs.
                let Ok(repo) = this.update(cx, |this, _| this.busy.is_none().then(|| this.repo.clone())) else {
                    return;
                };
                let Some(repo) = repo else { continue };
                last = Instant::now();
                let fetched = cx.background_executor().spawn(async move { repo.fetch_quietly() }).await;
                if fetched.is_ok() {
                    this.update(cx, |this, cx| this.refresh(cx)).ok();
                }
            }
        })
    }

    /// Refresh when files, the index or refs change outside the app.
    fn watch(&mut self, cx: &mut Context<Self>) {
        let repo = self.repo.clone();
        let Ok(watcher) = repo
            .git_dir()
            .map_err(|err| err.to_string())
            .and_then(|git_dir| RepoWatcher::new(repo.root(), &git_dir).map_err(|err| err.to_string()))
        else {
            return;
        };
        self._watch_task = Some(cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(Duration::from_millis(500)).await;
                let changes = watcher.take();
                if !changes.git && !changes.index && changes.worktree.is_empty() {
                    continue;
                }
                let repo = repo.clone();
                let relevant = changes.git
                    || changes.index
                    || cx
                        .background_executor()
                        .spawn(async move { repo.not_ignored(&changes.worktree) })
                        .await
                        .is_ok_and(|paths| !paths.is_empty());
                if relevant && this.update(cx, |this, cx| {
                    // An operation of our own refreshes when it finishes.
                    if this.busy.is_none() {
                        if changes.git { this.refresh(cx) } else { this.refresh_status(cx) }
                    }
                })
                .is_err()
                {
                    break;
                }
            }
        }));
    }

    pub fn conflicts(&self) -> impl Iterator<Item = &StatusEntry> {
        self.status.iter().filter(|e| e.is_conflicted())
    }

    pub fn branches_of(&self, kind: RefKind) -> impl Iterator<Item = &Branch> {
        self.branches.iter().filter(move |b| b.kind == kind)
    }

    pub fn is_favorite(&self, branch: &Branch) -> bool {
        self.favorites.contains(&branch.full_name())
    }

    pub fn set_favorite(&mut self, branch: &Branch, favorite: bool, cx: &mut Context<Self>) {
        let full_name = branch.full_name();
        match self.repo.set_favorite_branch(&full_name, favorite) {
            Ok(()) => {
                self.favorites.retain(|f| *f != full_name);
                if favorite {
                    self.favorites.push(full_name);
                }
                cx.notify();
            }
            Err(err) => cx.emit(RepoEvent::OpFailed(format!("{err:#}").into())),
        }
    }

    /// The background work to show, if any has gone on for a moment.
    pub fn activity(&self) -> Option<Activity> {
        self.activities.iter().filter(|(_, since)| since.elapsed() >= ACTIVITY_DELAY).map(|(a, _)| *a).min()
    }

    fn start_activity(&mut self, activity: Activity, cx: &mut Context<Self>) {
        self.activities.push((activity, Instant::now()));
        cx.spawn(async move |this, cx| {
            cx.background_executor().timer(ACTIVITY_DELAY).await;
            this.update(cx, |_, cx| cx.notify()).ok();
        })
        .detach();
    }

    fn end_activity(&mut self, activity: Activity, cx: &mut Context<Self>) {
        if let Some(ix) = self.activities.iter().position(|(a, _)| *a == activity) {
            let (_, since) = self.activities.remove(ix);
            if since.elapsed() >= ACTIVITY_DELAY {
                cx.notify();
            }
        }
    }

    /// Refresh when coming back to the repository, like JetBrains does on
    /// frame activation, unless it was read just now.
    pub fn refresh_if_stale(&mut self, cx: &mut Context<Self>) {
        let fresh = self.refreshed.is_some_and(|at| at.elapsed() < Duration::from_secs(2));
        if self.refresh_task.is_none() && !fresh {
            self.refresh(cx);
        }
    }

    /// Read the repository again. A refresh asked for while one runs waits
    /// for it, and several of them become one.
    pub fn refresh(&mut self, cx: &mut Context<Self>) {
        if self.refresh_task.is_some() {
            self.refresh_pending = true;
            return;
        }
        let repo = self.repo.clone();
        self.start_activity(Activity::Refreshing, cx);
        self.refresh_task = Some(cx.spawn(async move |this, cx| {
            let snapshot = cx
                .background_executor()
                .spawn(async move {
                    let started = Instant::now();
                    let status = repo.status();
                    let slow_status = started.elapsed() > Duration::from_millis(100);
                    Snapshot {
                        head: repo.head(),
                        branches: repo.branches().unwrap_or_default(),
                        current_branch: repo.current_branch(),
                        state: repo.repo_state(),
                        operation: repo.operation(),
                        status: status.unwrap_or_default(),
                        slow_status,
                        stashes: repo.stashes().unwrap_or_default(),
                        shelves: repo.shelves(),
                        web: repo.web_remote(),
                        favorites: repo.favorite_branches(),
                        recent_branches: repo.recent_branches(6),
                    }
                })
                .await;
            this.update(cx, |this, cx| {
                let refs_key: RefsKey = (
                    snapshot.head,
                    snapshot
                        .branches
                        .iter()
                        .map(|b| (format!("{:?}:{}", b.kind, b.name), b.target.clone()))
                        .collect(),
                );
                if this.refs_key.as_ref() != Some(&refs_key) {
                    // New commits or refs: add them to the commit-graph.
                    if this.refs_key.is_some() {
                        this.maintain(false, cx);
                    }
                    this.refs_key = Some(refs_key);
                    this.refs_version += 1;
                }
                this.branches = snapshot.branches;
                this.current_branch = snapshot.current_branch;
                this.state = snapshot.state;
                this.operation = snapshot.operation;
                this.status = snapshot.status;
                this.stashes = snapshot.stashes;
                this.shelves = snapshot.shelves;
                this.web = snapshot.web;
                this.favorites = snapshot.favorites;
                this.recent_branches = snapshot.recent_branches;
                this.status_read(snapshot.slow_status, cx);
                this.refreshed = Some(Instant::now());
                this.status_pending = false;
                this.refresh_done(cx);
            })
            .ok();
        }));
    }

    /// Read only the status again, for edits in the work tree: running the
    /// dozen other git commands of a refresh each time adds up.
    fn refresh_status(&mut self, cx: &mut Context<Self>) {
        if self.refresh_task.is_some() {
            self.status_pending = true;
            return;
        }
        let repo = self.repo.clone();
        self.start_activity(Activity::Refreshing, cx);
        self.refresh_task = Some(cx.spawn(async move |this, cx| {
            let (status, slow) = cx
                .background_executor()
                .spawn(async move {
                    let started = Instant::now();
                    let status = repo.status();
                    (status, started.elapsed() > Duration::from_millis(100))
                })
                .await;
            this.update(cx, |this, cx| {
                if let Ok(status) = status {
                    this.status = status;
                    this.status_read(slow, cx);
                }
                this.refresh_done(cx);
            })
            .ok();
        }));
    }

    fn status_read(&mut self, slow: bool, cx: &mut Context<Self>) {
        if slow {
            self.refresh_index(cx);
        }
        let tracked = self.status.iter().filter(|e| !e.is_untracked()).map(|e| e.path.as_str());
        if self.changelists.sync(tracked) {
            self.save_changelists();
        }
        cx.emit(RepoEvent::Changed);
        cx.notify();
    }

    /// Run the refresh asked for meanwhile, if any.
    fn refresh_done(&mut self, cx: &mut Context<Self>) {
        self.refresh_task = None;
        self.end_activity(Activity::Refreshing, cx);
        if std::mem::take(&mut self.refresh_pending) {
            self.refresh(cx);
        } else if std::mem::take(&mut self.status_pending) {
            self.refresh_status(cx);
        }
    }

    /// Run a git operation off the main thread, report the outcome and refresh.
    pub fn run<T: Send + 'static>(
        &mut self,
        label: impl Into<SharedString>,
        op: impl FnOnce(&Repo) -> Result<T> + Send + 'static,
        cx: &mut Context<Self>,
    ) {
        let label = label.into();
        let failed = label.clone();
        self.run_with(
            label,
            op,
            move |err| RepoEvent::OpFailed(format!("{}\n{err:#}", tr!("{} failed", failed)).into()),
            cx,
        );
    }

    /// Change the changelists and keep them; a refused change is reported.
    pub fn change_lists(&mut self, change: impl FnOnce(&mut Changelists) -> Result<()>, cx: &mut Context<Self>) {
        match change(&mut self.changelists) {
            Ok(()) => {
                self.save_changelists();
                cx.emit(RepoEvent::Changed);
                cx.notify();
            }
            Err(err) => cx.emit(RepoEvent::OpFailed(format!("{err:#}").into())),
        }
    }

    fn save_changelists(&self) {
        if let Ok(dir) = self.repo.git_dir() {
            self.changelists.save(&dir).ok();
        }
    }

    /// Shelve the changes of `paths`, remembering their changelist.
    pub fn shelve(&mut self, name: String, paths: Vec<String>, cx: &mut Context<Self>) {
        let changelist = match paths.first() {
            Some(first) if paths.iter().all(|p| self.changelists.list_of(p) == self.changelists.list_of(first)) => {
                self.changelists.list_of(first).to_string()
            }
            _ => self.changelists.active().to_string(),
        };
        self.run(tr!("Shelve {}", name), move |repo| repo.shelve(&name, &changelist, &paths), cx);
    }

    /// Unshelve all of shelf `id`, or `paths` of it, back into its changelist.
    pub fn unshelve(&mut self, id: String, paths: Option<Vec<String>>, cx: &mut Context<Self>) {
        self.run_then(
            tr!("Unshelve"),
            move |repo| repo.unshelve(&id, paths.as_deref()),
            |this, (shelf, files), cx| this.change_lists(|lists| lists.move_to(&files, &shelf.changelist), cx),
            |err| RepoEvent::OpFailed(format!("{}\n{err:#}", tr!("Unshelve failed")).into()),
            cx,
        );
    }

    /// Undo the last commit, keeping its changes and offering its message again.
    pub fn undo_commit(&mut self, cx: &mut Context<Self>) {
        self.run_then(
            tr!("Undo commit"),
            |repo| repo.undo_commit(),
            |_, message, cx| cx.emit(RepoEvent::MessageRestored(message)),
            |err| RepoEvent::OpFailed(format!("{}\n{err:#}", tr!("Undo commit failed")).into()),
            cx,
        );
    }

    /// Check out a branch or revision, offering Smart Checkout when local
    /// changes are in the way.
    pub fn checkout(&mut self, target: String, remote: bool, cx: &mut Context<Self>) {
        let label = tr!("Checkout {}", crate::actions::short(&target));
        let op_target = target.clone();
        let failed = label.clone();
        self.run_with(
            label,
            move |repo| repo.checkout_any(&op_target, remote),
            move |err| {
                if crate::git::is_blocked_by_local_changes(&err) {
                    RepoEvent::CheckoutBlocked {
                        target,
                        remote,
                        message: format!("{err:#}").into(),
                    }
                } else {
                    RepoEvent::OpFailed(format!("{}\n{err:#}", tr!("{} failed", failed)).into())
                }
            },
            cx,
        );
    }

    /// Like [`Self::run`], with a custom event for failures.
    pub fn run_with<T: Send + 'static>(
        &mut self,
        label: impl Into<SharedString>,
        op: impl FnOnce(&Repo) -> Result<T> + Send + 'static,
        on_error: impl FnOnce(anyhow::Error) -> RepoEvent + 'static,
        cx: &mut Context<Self>,
    ) {
        self.run_then(label, op, |_, _, _| {}, on_error, cx);
    }

    /// Like [`Self::run_with`], also handing the operation's result to `on_ok`.
    pub fn run_then<T: Send + 'static>(
        &mut self,
        label: impl Into<SharedString>,
        op: impl FnOnce(&Repo) -> Result<T> + Send + 'static,
        on_ok: impl FnOnce(&mut Self, T, &mut Context<Self>) + 'static,
        on_error: impl FnOnce(anyhow::Error) -> RepoEvent + 'static,
        cx: &mut Context<Self>,
    ) {
        let label = label.into();
        if let Some(busy) = &self.busy {
            cx.emit(RepoEvent::OpFailed(tr!("Another operation is running: {}", busy).into()));
            return;
        }
        self.busy = Some(label.clone());
        cx.notify();
        let repo = self.repo.clone();
        cx.spawn(async move |this, cx| {
            let (result, conflicted) = cx
                .background_executor()
                .spawn(async move {
                    let result = crate::console::recording(|| op(&repo));
                    let conflicted = result.is_err()
                        && repo.status().is_ok_and(|status| status.iter().any(|e| e.is_conflicted()));
                    (result, conflicted)
                })
                .await;
            this.update(cx, |this, cx| {
                this.busy = None;
                match result {
                    Ok(value) => {
                        cx.emit(RepoEvent::OpSucceeded(tr!("{}: done", label).into()));
                        on_ok(this, value, cx);
                    }
                    Err(_) if conflicted => cx.emit(RepoEvent::Conflicts(label)),
                    Err(err) => cx.emit(on_error(err)),
                }
                this.refresh(cx);
            })
            .ok();
        })
        .detach();
    }
}
