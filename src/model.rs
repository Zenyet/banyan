//! Shared repository state. Every git operation goes through here so that all
//! views refresh together afterwards.

use std::sync::Arc;
use std::time::Duration;

use anyhow::Result;
use gpui_kit::*;

use crate::git::{Branch, RefKind, Repo, RepoState, StatusEntry, Stash};
use crate::watcher::RepoWatcher;

pub enum RepoEvent {
    /// Refs, HEAD or the working tree may have changed.
    Changed,
    OpSucceeded(SharedString),
    OpFailed(SharedString),
}

pub struct RepoModel {
    pub repo: Arc<Repo>,
    pub branches: Vec<Branch>,
    pub current_branch: Option<String>,
    pub state: RepoState,
    pub status: Vec<StatusEntry>,
    pub stashes: Vec<Stash>,
    /// Label of the operation that is running, if any.
    pub busy: Option<SharedString>,
    /// Bumped whenever HEAD or any ref moves, so views know to reload the log.
    pub refs_version: u64,
    refs_key: Option<RefsKey>,
    refresh_task: Option<Task<()>>,
    _watch_task: Option<Task<()>>,
}

/// HEAD and every ref with its target.
type RefsKey = (Option<String>, Vec<(String, String)>);

impl EventEmitter<RepoEvent> for RepoModel {}

struct Snapshot {
    head: Option<String>,
    branches: Vec<Branch>,
    current_branch: Option<String>,
    state: RepoState,
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
            status: Vec::new(),
            stashes: Vec::new(),
            busy: None,
            refs_version: 0,
            refs_key: None,
            refresh_task: None,
            _watch_task: None,
        };
        this.refresh(cx);
        this.watch(cx);
        this
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
        if let Some(busy) = &self.busy {
            cx.emit(RepoEvent::OpFailed(format!("Another operation is running: {busy}").into()));
            return;
        }
        self.busy = Some(label.clone());
        cx.notify();
        let repo = self.repo.clone();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { op(&repo) }).await;
            this.update(cx, |this, cx| {
                this.busy = None;
                match result {
                    Ok(_) => cx.emit(RepoEvent::OpSucceeded(format!("{label}: done").into())),
                    Err(err) => cx.emit(RepoEvent::OpFailed(format!("{label} failed\n{err:#}").into())),
                }
                this.refresh(cx);
            })
            .ok();
        })
        .detach();
    }
}
