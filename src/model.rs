//! Shared repository state. Every git operation goes through here so that all
//! views refresh together afterwards.

use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::Result;
use gpui_kit::*;

use crate::git::{Branch, Operation, RefKind, Repo, RepoState, StatusEntry, Stash};
use crate::settings::Settings;
use crate::watcher::RepoWatcher;

pub enum RepoEvent {
    /// Refs, HEAD or the working tree may have changed.
    Changed,
    OpSucceeded(SharedString),
    OpFailed(SharedString),
    /// The operation with this label stopped with merge conflicts.
    Conflicts(SharedString),
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
    /// Label of the operation that is running, if any.
    pub busy: Option<SharedString>,
    /// Bumped whenever HEAD or any ref moves, so views know to reload the log.
    pub refs_version: u64,
    refs_key: Option<RefsKey>,
    refresh_task: Option<Task<()>>,
    _watch_task: Option<Task<()>>,
    _auto_fetch_task: Task<()>,
}

/// HEAD and every ref with its target.
type RefsKey = (Option<String>, Vec<(String, String)>);

impl EventEmitter<RepoEvent> for RepoModel {}

struct Snapshot {
    head: Option<String>,
    branches: Vec<Branch>,
    current_branch: Option<String>,
    state: RepoState,
    operation: Option<Operation>,
    status: Vec<StatusEntry>,
    stashes: Vec<Stash>,
}

impl RepoModel {
    pub fn new(repo: Repo, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            repo: Arc::new(repo),
            branches: Vec::new(),
            current_branch: None,
            state: RepoState::Clean,
            operation: None,
            status: Vec::new(),
            stashes: Vec::new(),
            busy: None,
            refs_version: 0,
            refs_key: None,
            refresh_task: None,
            _watch_task: None,
            _auto_fetch_task: Self::auto_fetch(cx),
        };
        this.refresh(cx);
        this.watch(cx);
        this
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
                if !changes.git && changes.worktree.is_empty() {
                    continue;
                }
                let repo = repo.clone();
                let relevant = changes.git
                    || cx
                        .background_executor()
                        .spawn(async move { repo.not_ignored(&changes.worktree) })
                        .await
                        .is_ok_and(|paths| !paths.is_empty());
                if relevant && this.update(cx, |this, cx| {
                    // An operation of our own refreshes when it finishes.
                    if this.busy.is_none() {
                        this.refresh(cx)
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

    pub fn refresh(&mut self, cx: &mut Context<Self>) {
        let repo = self.repo.clone();
        self.refresh_task = Some(cx.spawn(async move |this, cx| {
            let snapshot = cx
                .background_executor()
                .spawn(async move {
                    Snapshot {
                        head: repo.head(),
                        branches: repo.branches().unwrap_or_default(),
                        current_branch: repo.current_branch(),
                        state: repo.repo_state(),
                        operation: repo.operation(),
                        status: repo.status().unwrap_or_default(),
                        stashes: repo.stashes().unwrap_or_default(),
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
                    this.refs_key = Some(refs_key);
                    this.refs_version += 1;
                }
                this.branches = snapshot.branches;
                this.current_branch = snapshot.current_branch;
                this.state = snapshot.state;
                this.operation = snapshot.operation;
                this.status = snapshot.status;
                this.stashes = snapshot.stashes;
                cx.emit(RepoEvent::Changed);
                cx.notify();
            })
            .ok();
        }));
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
            move |err| RepoEvent::OpFailed(format!("{failed} failed\n{err:#}").into()),
            cx,
        );
    }

    /// Check out a branch or revision, offering Smart Checkout when local
    /// changes are in the way.
    pub fn checkout(&mut self, target: String, remote: bool, cx: &mut Context<Self>) {
        let label = format!("Checkout {}", crate::actions::short(&target));
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
                    RepoEvent::OpFailed(format!("{failed} failed\n{err:#}").into())
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
        let label = label.into();
        if let Some(busy) = &self.busy {
            cx.emit(RepoEvent::OpFailed(format!("Another operation is running: {busy}").into()));
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
                    Ok(_) => cx.emit(RepoEvent::OpSucceeded(format!("{label}: done").into())),
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
