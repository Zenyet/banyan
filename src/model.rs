//! Shared repository state. Every git operation goes through here so that all
//! views refresh together afterwards.

use std::sync::Arc;

use anyhow::Result;
use gpui_kit::*;

use crate::git::{Branch, RefKind, Repo, RepoState, StatusEntry, Stash};

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
    refresh_task: Option<Task<()>>,
}

impl EventEmitter<RepoEvent> for RepoModel {}

struct Snapshot {
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
            refresh_task: None,
        };
        this.refresh(cx);
        this
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
                        branches: repo.branches().unwrap_or_default(),
                        current_branch: repo.current_branch(),
                        state: repo.repo_state(),
                        status: repo.status().unwrap_or_default(),
                        stashes: repo.stashes().unwrap_or_default(),
                    }
                })
                .await;
            this.update(cx, |this, cx| {
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
                    Ok(_) => cx.emit(RepoEvent::OpSucceeded(label)),
                    Err(err) => cx.emit(RepoEvent::OpFailed(format!("{label} failed\n{err:#}").into())),
                }
                this.refresh(cx);
            })
            .ok();
        })
        .detach();
    }
}
