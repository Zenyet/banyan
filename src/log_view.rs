//! The Git "Log" tab: branches, commit graph, commit details and diff preview.

use std::collections::HashMap;
use std::rc::Rc;
use std::sync::Arc;

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, h_resizable,
    input::{Input, InputEvent, InputState},
    list::ListItem,
    menu::{ContextMenuExt as _, DropdownMenu as _, PopupMenuItem},
    resizable_panel,
    tree::{TreeItem, TreeState, tree},
    v_flex, v_resizable,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::actions::{branch_menu, commit_menu, short};
use crate::diff_pane::DiffPane;
use crate::git::{Branch, Commit, CommitDetails, FileChange, RefKind};
use crate::graph::{self, GraphRow, Half};
use crate::model::{RepoEvent, RepoModel};
use crate::ui::{selection_bg, FILE_PREFIX, file_tree, format_full_time, format_time, lane_color, path_tree, ref_label, status_color};

const ROW_HEIGHT: Pixels = px(24.);
const LANE_WIDTH: Pixels = px(14.);
const GRAPH_PADDING: Pixels = px(6.);
const LOG_LIMIT: usize = 5000;
const BRANCH_PREFIX: &str = "b:";

actions!(
    commit_list,
    [SelectPrevCommit, SelectNextCommit, SelectFirstCommit, SelectLastCommit, PageUpCommits, PageDownCommits]
);

pub fn init(cx: &mut App) {
    cx.bind_keys([
        KeyBinding::new("up", SelectPrevCommit, Some("CommitList")),
        KeyBinding::new("down", SelectNextCommit, Some("CommitList")),
        KeyBinding::new("home", SelectFirstCommit, Some("CommitList")),
        KeyBinding::new("end", SelectLastCommit, Some("CommitList")),
        KeyBinding::new("pageup", PageUpCommits, Some("CommitList")),
        KeyBinding::new("pagedown", PageDownCommits, Some("CommitList")),
    ]);
}

pub struct LogView {
    model: Entity<RepoModel>,
    commits: Arc<Vec<Commit>>,
    graph: Arc<Vec<GraphRow>>,
    /// Indices into `commits` that pass the text filter.
    rows: Rc<Vec<usize>>,
    selected: Option<String>,
    head: Option<String>,
    filter: Entity<InputState>,
    branch_filter: Option<String>,
    author_filter: Option<String>,
    branch_tree: Entity<TreeState>,
    branches: Rc<HashMap<SharedString, Branch>>,
    selected_branch: Option<SharedString>,
    details: Option<Arc<CommitDetails>>,
    file_tree: Entity<TreeState>,
    files: Rc<HashMap<SharedString, FileChange>>,
    selected_file: Option<SharedString>,
    diff: Entity<DiffPane>,
    scroll: UniformListScrollHandle,
    focus: FocusHandle,
    loading: bool,
    /// How many commits to load; grows with "Load more".
    limit: usize,
    /// The model's `refs_version` the log was last loaded for.
    refs_version: u64,
    load_more_scheduled: bool,
    log_task: Option<Task<()>>,
    details_task: Option<Task<()>>,
    diff_task: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

impl LogView {
    pub fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let filter = cx.new(|cx| {
            InputState::new(window, cx).placeholder("Text or hash")
        });
        let branch_tree = cx.new(|cx| TreeState::new(cx));
        let file_tree = cx.new(|cx| TreeState::new(cx));
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.clear(cx));

        let subscriptions = vec![
            cx.subscribe(&model, |this, _, event, cx| {
                if let RepoEvent::Changed = event {
                    this.rebuild_branches(cx);
                    this.reload_if_refs_moved(cx);
                }
            }),
            cx.subscribe(&filter, |this, _, event, cx| {
                if let InputEvent::Change = event {
                    this.apply_filter(cx);
                }
            }),
            cx.observe(&branch_tree, |this, tree, cx| {
                let id = tree.read(cx).selected_item().map(|item| item.id.clone());
                if id != this.selected_branch {
                    this.selected_branch = id.clone();
                    if let Some(branch) = id.and_then(|id| this.branches.get(&id).cloned()) {
                        this.reveal_commit(&branch.target, cx);
                    }
                }
            }),
            cx.observe(&file_tree, |this, tree, cx| {
                let id = tree.read(cx).selected_item().map(|item| item.id.clone());
                if id != this.selected_file {
                    this.selected_file = id;
                    this.load_file_diff(cx);
                }
            }),
        ];

        let mut this = Self {
            model,
            commits: Arc::default(),
            graph: Arc::default(),
            rows: Rc::default(),
            selected: None,
            head: None,
            filter,
            branch_filter: None,
            author_filter: None,
            branch_tree,
            branches: Rc::default(),
            selected_branch: None,
            details: None,
            file_tree,
            files: Rc::default(),
            selected_file: None,
            diff,
            scroll: UniformListScrollHandle::new(),
            focus: cx.focus_handle(),
            loading: true,
            limit: LOG_LIMIT,
            refs_version: 0,
            load_more_scheduled: false,
            log_task: None,
            details_task: None,
            diff_task: None,
            _subscriptions: subscriptions,
        };
        this.rebuild_branches(cx);
        this.reload_if_refs_moved(cx);
        this
    }

    /// Working tree edits don't change the log; only reload when refs moved.
    fn reload_if_refs_moved(&mut self, cx: &mut Context<Self>) {
        let version = self.model.read(cx).refs_version;
        if version != self.refs_version {
            self.refs_version = version;
            self.reload(cx);
        }
    }

    fn report(&self, err: anyhow::Error, cx: &mut Context<Self>) {
        let message: SharedString = format!("{err:#}").into();
        self.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(message)));
    }

    pub fn reload(&mut self, cx: &mut Context<Self>) {
        let repo = self.model.read(cx).repo.clone();
        let refs = self.branch_filter.clone();
        let limit = self.limit;
        self.loading = true;
        cx.notify();
        self.log_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    let commits = repo.log(refs.as_deref(), limit)?;
                    let graph = graph::layout(&commits);
                    let head = repo.git(&["rev-parse", "HEAD"]).ok().map(|s| s.trim().to_string());
                    anyhow::Ok((commits, graph, head))
                })
                .await;
            this.update(cx, |this, cx| {
                this.loading = false;
                match result {
                    Ok((commits, graph, head)) => {
                        this.commits = Arc::new(commits);
                        this.graph = Arc::new(graph);
                        this.head = head;
                    }
                    Err(err) => {
                        this.commits = Arc::default();
                        this.graph = Arc::default();
                        this.report(err, cx);
                    }
                }
                this.apply_filter(cx);
                let still_there = this
                    .selected
                    .as_ref()
                    .is_some_and(|id| this.commits.iter().any(|c| &c.id == id));
                if still_there {
                    // Branches containing the commit may have changed.
                    this.load_details(cx);
                } else {
                    let first = this.rows.first().map(|&ix| this.commits[ix].id.clone());
                    this.select(first, cx);
                }
            })
            .ok();
        }));
    }

    fn rebuild_branches(&mut self, cx: &mut Context<Self>) {
        let model = self.model.read(cx);
        let mut lookup = HashMap::new();
        let mut groups = Vec::new();
        if let Some(head) = model.branches_of(RefKind::Local).find(|b| b.is_head) {
            let id: SharedString = format!("{BRANCH_PREFIX}HEAD").into();
            lookup.insert(id.clone(), head.clone());
            groups.push(TreeItem::new(id, format!("HEAD ({})", head.name)));
        }
        for (label, kind) in [("Local", RefKind::Local), ("Remote", RefKind::Remote), ("Tags", RefKind::Tag)] {
            let branches: Vec<&Branch> = model.branches_of(kind.clone()).collect();
            if branches.is_empty() {
                continue;
            }
            let prefix = format!("{BRANCH_PREFIX}{label}:");
            for branch in &branches {
                lookup.insert(format!("{prefix}{}", branch.name).into(), (*branch).clone());
            }
            let children = path_tree(branches.iter().map(|b| b.name.as_str()), &prefix);
            groups.push(
                TreeItem::new(format!("group:{label}"), label)
                    .children(children)
                    .expanded(kind != RefKind::Tag),
            );
        }
        self.branches = Rc::new(lookup);
        self.branch_tree.update(cx, |tree, cx| tree.set_items(groups, cx));
    }

    fn apply_filter(&mut self, cx: &mut Context<Self>) {
        let query = self.filter.read(cx).value().trim().to_lowercase();
        let author = self.author_filter.as_deref();
        let rows: Vec<usize> = if query.is_empty() && author.is_none() {
            (0..self.commits.len()).collect()
        } else {
            self.commits
                .iter()
                .enumerate()
                .filter(|(_, c)| author.is_none_or(|author| c.author == author))
                .filter(|(_, c)| {
                    query.is_empty()
                        || c.id.starts_with(&query)
                        || c.subject.to_lowercase().contains(&query)
                        || c.author.to_lowercase().contains(&query)
                        || c.refs.iter().any(|r| r.name.to_lowercase().contains(&query))
                })
                .map(|(ix, _)| ix)
                .collect()
        };
        self.rows = Rc::new(rows);
        cx.notify();
    }

    fn is_filtered(&self, cx: &App) -> bool {
        self.author_filter.is_some() || !self.filter.read(cx).value().trim().is_empty()
    }

    fn set_author_filter(&mut self, author: Option<String>, cx: &mut Context<Self>) {
        self.author_filter = author;
        self.apply_filter(cx);
    }

    /// Authors of the loaded commits, most active first.
    fn authors(&self) -> Vec<String> {
        let mut counts: HashMap<&str, usize> = HashMap::new();
        for commit in self.commits.iter() {
            *counts.entry(commit.author.as_str()).or_default() += 1;
        }
        let mut authors: Vec<(&str, usize)> = counts.into_iter().collect();
        authors.sort_by(|a, b| b.1.cmp(&a.1).then(a.0.cmp(b.0)));
        authors.into_iter().map(|(name, _)| name.to_string()).collect()
    }

    fn set_branch_filter(&mut self, branch: Option<String>, cx: &mut Context<Self>) {
        self.branch_filter = branch;
        self.reload(cx);
    }

    fn reveal_commit(&mut self, id: &str, cx: &mut Context<Self>) {
        if let Some(row) = self.rows.iter().position(|&ix| self.commits[ix].id == id) {
            self.scroll.scroll_to_item(row, ScrollStrategy::Center);
            self.select(Some(id.to_string()), cx);
        }
    }

    fn select(&mut self, id: Option<String>, cx: &mut Context<Self>) {
        if self.selected == id && self.details.is_some() {
            return;
        }
        self.selected = id;
        self.load_details(cx);
        cx.notify();
    }

    fn select_offset(&mut self, delta: isize, cx: &mut Context<Self>) {
        if self.rows.is_empty() {
            return;
        }
        let current = self
            .selected
            .as_ref()
            .and_then(|id| self.rows.iter().position(|&ix| &self.commits[ix].id == id));
        let row = match current {
            Some(row) => (row as isize + delta).clamp(0, self.rows.len() as isize - 1) as usize,
            None => 0,
        };
        // At the end, also reveal the "Load more" row below the last commit.
        let has_more = self.commits.len() >= self.limit;
        let target = if has_more && row + 1 == self.rows.len() { row + 1 } else { row };
        self.scroll.scroll_to_item(target, ScrollStrategy::Nearest);
        let id = self.commits[self.rows[row]].id.clone();
        self.select(Some(id), cx);
    }

    fn load_details(&mut self, cx: &mut Context<Self>) {
        let Some(id) = self.selected.clone() else {
            self.details = None;
            self.files = Rc::default();
            self.file_tree.update(cx, |tree, cx| tree.set_items(Vec::<TreeItem>::new(), cx));
            self.diff.update(cx, |diff, cx| diff.clear(cx));
            return;
        };
        let repo = self.model.read(cx).repo.clone();
        self.details_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.commit_details(&id) })
                .await;
            this.update(cx, |this, cx| match result {
                Ok(details) => {
                    let same_commit = this.details.as_ref().is_some_and(|d| d.id == details.id);
                    let files: HashMap<SharedString, FileChange> = details
                        .files
                        .iter()
                        .map(|f| (format!("{FILE_PREFIX}{}", f.path).into(), f.clone()))
                        .collect();
                    let items = file_tree(details.files.iter().map(|f| f.path.as_str()));
                    this.files = Rc::new(files);
                    this.details = Some(Arc::new(details));
                    if !same_commit {
                        this.selected_file = None;
                        this.file_tree.update(cx, |tree, cx| {
                            tree.set_items(items, cx);
                            tree.set_selected_index(None, cx);
                        });
                        // Preview the first file, like JetBrains' diff preview.
                        let first = first_file_index(&this.files, &this.file_tree, cx);
                        this.file_tree.update(cx, |tree, cx| tree.set_selected_index(first, cx));
                        if first.is_none() {
                            this.diff.update(cx, |diff, cx| diff.show_message(None, "No changed files", cx));
                        }
                    }
                    cx.notify();
                }
                Err(err) => this.report(err, cx),
            })
            .ok();
        }));
    }

    fn load_file_diff(&mut self, cx: &mut Context<Self>) {
        let (Some(details), Some(file)) = (
            self.details.clone(),
            self.selected_file.as_ref().and_then(|id| self.files.get(id).cloned()),
        ) else {
            return;
        };
        let repo = self.model.read(cx).repo.clone();
        let title = match &file.orig_path {
            Some(orig) => format!("{orig} → {}  ({})", file.path, short(&details.id)),
            None => format!("{}  ({})", file.path, short(&details.id)),
        };
        self.diff_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.commit_file_diff(&details, &file) })
                .await;
            this.update(cx, |this, cx| {
                this.diff.update(cx, |diff, cx| match result {
                    Ok(patch) => diff.show_patch(title, &patch, cx),
                    Err(err) => diff.show_message(Some(title.into()), format!("{err:#}"), cx),
                })
            })
            .ok();
        }));
    }

    fn render_branches(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let branches = self.branches.clone();
        let menu_branches = self.branches.clone();
        let model = self.model.clone();
        v_flex()
            .size_full()
            .bg(cx.theme().sidebar)
            .child(panel_header("Branches", cx))
            .child(
                div().flex_1().min_h_0().child(
                    tree(&self.branch_tree, move |ix, entry, _selected, _, cx| {
                        let item = entry.item();
                        let branch = branches.get(&item.id);
                        let icon = if entry.is_folder() {
                            if entry.is_expanded() { IconName::ChevronDown } else { IconName::ChevronRight }
                        } else if branch.is_some_and(|b| b.kind == RefKind::Tag) {
                            IconName::Tag
                        } else {
                            IconName::GitBranch
                        };
                        let is_head = branch.is_some_and(|b| b.is_head);
                        ListItem::new(ix).py_0().child(
                            h_flex()
                                .gap_1()
                                .pl(px(entry.depth() as f32 * 12.))
                                .text_sm()
                                .child(Icon::new(icon).xsmall().text_color(if is_head {
                                    rgb(0xe0a33d).into()
                                } else {
                                    cx.theme().muted_foreground
                                }))
                                .child(div().truncate().child(item.label.clone()))
                                .when_some(branch.and_then(|b| b.track.clone()), |row, track| {
                                    row.child(
                                        div()
                                            .text_xs()
                                            .text_color(cx.theme().muted_foreground)
                                            .child(format_track(&track)),
                                    )
                                }),
                        )
                    })
                    .context_menu(move |_, entry, menu, _, cx| {
                        match menu_branches.get(&entry.item().id) {
                            Some(branch) => branch_menu(menu, &model, branch, cx),
                            None => menu,
                        }
                    }),
                ),
            )
    }

    fn render_toolbar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let model = self.model.read(cx);
        let mut names: Vec<String> = model
            .branches_of(RefKind::Local)
            .chain(model.branches_of(RefKind::Remote))
            .map(|b| b.name.clone())
            .collect();
        names.dedup();
        let view = cx.entity().downgrade();
        let label = match &self.branch_filter {
            Some(branch) => format!("Branch: {branch}"),
            None => "Branch: All".into(),
        };
        h_flex()
            .flex_none()
            .h(px(36.))
            .px_2()
            .gap_2()
            .border_b_1()
            .border_color(cx.theme().border)
            .child(
                div().w(px(260.)).child(
                    Input::new(&self.filter)
                        .xsmall()
                        .prefix(Icon::new(IconName::Search).xsmall())
                        .cleanable(true),
                ),
            )
            .child(
                Button::new("branch-filter")
                    .label(label)
                    .ghost()
                    .xsmall()
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, _| {
                        let view = view.clone();
                        menu = menu.item(PopupMenuItem::new("All").on_click({
                            let view = view.clone();
                            move |_, _, cx| {
                                view.update(cx, |this, cx| this.set_branch_filter(None, cx)).ok();
                            }
                        }));
                        menu = menu.separator();
                        for name in &names {
                            let view = view.clone();
                            let branch = name.clone();
                            menu = menu.item(PopupMenuItem::new(name.clone()).on_click(move |_, _, cx| {
                                let branch = branch.clone();
                                view.update(cx, |this, cx| this.set_branch_filter(Some(branch), cx)).ok();
                            }));
                        }
                        menu.max_h(px(480.)).scrollable(true)
                    }),
            )
            .child({
                let view = cx.entity().downgrade();
                let authors = self.authors();
                Button::new("author-filter")
                    .label(match &self.author_filter {
                        Some(author) => format!("User: {author}"),
                        None => "User: All".into(),
                    })
                    .ghost()
                    .xsmall()
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, _| {
                        let all = view.clone();
                        menu = menu
                            .item(PopupMenuItem::new("All").on_click(move |_, _, cx| {
                                all.update(cx, |this, cx| this.set_author_filter(None, cx)).ok();
                            }))
                            .separator();
                        for author in &authors {
                            let view = view.clone();
                            let name = author.clone();
                            menu = menu.item(PopupMenuItem::new(author.clone()).on_click(move |_, _, cx| {
                                let name = name.clone();
                                view.update(cx, |this, cx| this.set_author_filter(Some(name), cx)).ok();
                            }));
                        }
                        menu.max_h(px(480.)).scrollable(true)
                    })
            })
            .child(div().flex_1())
            .when(self.loading, |row| {
                row.child(div().text_xs().text_color(cx.theme().muted_foreground).child("Loading…"))
            })
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(format!("{} commits", self.rows.len())),
            )
            .child(
                Button::new("log-refresh")
                    .icon(IconName::RefreshCw)
                    .ghost()
                    .xsmall()
                    .tooltip("Refresh")
                    .on_click(cx.listener(|this, _, _, cx| {
                        this.model.update(cx, |model, cx| model.refresh(cx));
                        this.reload(cx);
                    })),
            )
    }

    fn render_commits(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let show_graph = !self.is_filtered(cx);
        let has_more = show_graph && self.commits.len() >= self.limit;
        v_flex()
            .size_full()
            .child(self.render_toolbar(cx))
            .child(
                div()
                    .id("commit-list")
                    .key_context("CommitList")
                    .track_focus(&self.focus)
                    .on_action(cx.listener(|this, _: &SelectPrevCommit, _, cx| this.select_offset(-1, cx)))
                    .on_action(cx.listener(|this, _: &SelectNextCommit, _, cx| this.select_offset(1, cx)))
                    .on_action(cx.listener(|this, _: &SelectFirstCommit, _, cx| this.select_offset(isize::MIN / 2, cx)))
                    .on_action(cx.listener(|this, _: &SelectLastCommit, _, cx| this.select_offset(isize::MAX / 2, cx)))
                    .on_action(cx.listener(|this, _: &PageUpCommits, _, cx| this.select_offset(-25, cx)))
                    .on_action(cx.listener(|this, _: &PageDownCommits, _, cx| this.select_offset(25, cx)))
                    .flex_1()
                    .min_h_0()
                    .child(
                        uniform_list(
                            "commits",
                            self.rows.len() + has_more as usize,
                            cx.processor(move |this, range: std::ops::Range<usize>, window, cx| {
                                range
                                    .map(|row| {
                                        if row < this.rows.len() {
                                            this.render_commit_row(row, show_graph, window, cx)
                                        } else {
                                            this.render_load_more(cx)
                                        }
                                    })
                                    .collect::<Vec<_>>()
                            }),
                        )
                        .size_full()
                        .track_scroll(&self.scroll),
                    ),
            )
    }

    fn render_load_more(&mut self, cx: &mut Context<Self>) -> AnyElement {
        // Scrolling the row into view loads the next page, like JetBrains.
        if !self.loading && !self.load_more_scheduled {
            self.load_more_scheduled = true;
            cx.spawn(async move |this, cx| {
                this.update(cx, |this, cx| this.load_more(cx)).ok();
            })
            .detach();
        }
        div()
            .id("load-more")
            .h(ROW_HEIGHT)
            .w_full()
            .flex()
            .items_center()
            .justify_center()
            .text_sm()
            .text_color(cx.theme().link)
            .hover(|style| style.bg(cx.theme().list_hover))
            .child(if self.loading {
                "Loading…".to_string()
            } else {
                format!("Showing the first {} commits. Load more…", self.limit)
            })
            .on_click(cx.listener(|this, _, _, cx| this.load_more(cx)))
            .into_any_element()
    }

    fn load_more(&mut self, cx: &mut Context<Self>) {
        self.load_more_scheduled = false;
        if !self.loading && self.commits.len() >= self.limit {
            self.limit += LOG_LIMIT;
            self.reload(cx);
        }
    }

    fn render_commit_row(
        &self,
        row: usize,
        show_graph: bool,
        _: &mut Window,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let ix = self.rows[row];
        let commit = &self.commits[ix];
        let selected = self.selected.as_deref() == Some(commit.id.as_str());
        let is_head = self.head.as_deref() == Some(commit.id.as_str());
        let graph_row = self.graph.get(ix).cloned().unwrap_or_default();
        let graph_width = if show_graph {
            GRAPH_PADDING * 2. + LANE_WIDTH * graph_row.width as f32
        } else {
            px(8.)
        };
        let id = commit.id.clone();
        let subject = commit.subject.clone();
        let model = self.model.clone();
        let muted = cx.theme().muted_foreground;
        let fg = cx.theme().foreground;

        div()
            .id(SharedString::from(commit.id.clone()))
            .h(ROW_HEIGHT)
            .w_full()
            .flex()
            .items_center()
            .pr_2()
            .text_sm()
            .text_color(fg)
            .when(selected, |row| row.bg(selection_bg()))
            .when(!selected, |row| row.hover(|style| style.bg(cx.theme().list_hover)))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, _, window, cx| {
                    this.focus.focus(window, cx);
                    this.select(Some(this.commits[ix].id.clone()), cx);
                }),
            )
            .on_mouse_down(
                MouseButton::Right,
                cx.listener(move |this, _, _, cx| this.select(Some(this.commits[ix].id.clone()), cx)),
            )
            .child(
                div()
                    .flex_none()
                    .w(graph_width)
                    .h_full()
                    .when(show_graph, |cell| {
                        cell.child(
                            canvas(
                                |_, _, _| {},
                                move |bounds, _, window, cx| paint_graph(&graph_row, is_head, bounds, window, cx),
                            )
                            .size_full(),
                        )
                    }),
            )
            .child(
                h_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_1()
                    .overflow_hidden()
                    .children(commit.refs.iter().map(|r| ref_label(&r.name, &r.kind, cx)))
                    .child(div().min_w_0().truncate().child(commit.subject.clone())),
            )
            .child(
                div()
                    .flex_none()
                    .w(px(140.))
                    .pl_2()
                    .truncate()
                    .text_color(if selected { fg } else { muted })
                    .child(commit.author.clone()),
            )
            .child(
                div()
                    .flex_none()
                    .w(px(130.))
                    .pl_2()
                    .truncate()
                    .text_color(if selected { fg } else { muted })
                    .child(format_time(commit.time)),
            )
            .context_menu(move |menu, _, _| commit_menu(menu, &model, id.clone(), subject.clone()))
            .into_any_element()
    }

    fn render_details(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let files = self.files.clone();
        let menu_files = self.files.clone();
        let model = self.model.clone();
        let rev = self.details.as_ref().map(|d| d.id.clone());
        let changed = self.details.as_ref().map(|d| d.files.len()).unwrap_or(0);
        let files_panel = v_flex()
            .size_full()
            .child(panel_header(format!("Changed Files ({changed})"), cx))
            .child(div().flex_1().min_h_0().child(tree(
                &self.file_tree,
                move |ix, entry, _, _, cx| {
                    let item = entry.item();
                    let change = files.get(&item.id);
                    let (icon, color) = match change {
                        Some(change) => (IconName::File, status_color(change.status, cx)),
                        None if entry.is_expanded() => (IconName::FolderOpen, cx.theme().muted_foreground),
                        None => (IconName::Folder, cx.theme().muted_foreground),
                    };
                    ListItem::new(ix).py_0().child(
                        h_flex()
                            .gap_1()
                            .pl(px(entry.depth() as f32 * 12.))
                            .text_sm()
                            .child(Icon::new(icon).xsmall().text_color(color))
                            .child(div().truncate().text_color(color).child(item.label.clone()))
                            .when_some(change.and_then(|c| c.orig_path.clone()), |row, orig| {
                                row.child(
                                    div()
                                        .text_xs()
                                        .truncate()
                                        .text_color(cx.theme().muted_foreground)
                                        .child(format!("from {orig}")),
                                )
                            }),
                    )
                },
            )
            .context_menu(move |_, entry, menu, _, _| {
                let (Some(change), Some(rev)) = (menu_files.get(&entry.item().id), rev.clone()) else {
                    return menu;
                };
                let path = change.path.clone();
                let (m, p) = (model.clone(), path.clone());
                let menu = menu.item(PopupMenuItem::new("Show History").on_click(move |_, window, cx| {
                    crate::history::show_history(m.clone(), p.clone(), window, cx)
                }));
                if change.status == 'D' {
                    return menu;
                }
                let m = model.clone();
                menu.item(PopupMenuItem::new("Annotate at This Revision").on_click(move |_, window, cx| {
                    crate::history::annotate(m.clone(), rev.clone(), path.clone(), window, cx)
                }))
            })));

        let info = match &self.details {
            None => div()
                .p_3()
                .text_sm()
                .text_color(cx.theme().muted_foreground)
                .child("No commit selected")
                .into_any_element(),
            Some(details) => {
                let muted = cx.theme().muted_foreground;
                let branches = if details.branches.len() > 6 {
                    format!(
                        "In {} branches: {}, …",
                        details.branches.len(),
                        details.branches[..6].join(", ")
                    )
                } else {
                    format!("In {} branches: {}", details.branches.len(), details.branches.join(", "))
                };
                div()
                    .id("commit-info")
                    .size_full()
                    .overflow_y_scroll()
                    .p_3()
                    .child(
                        v_flex()
                            .gap_2()
                            .text_sm()
                            .child(
                                div()
                                    .whitespace_normal()
                                    .font_semibold()
                                    .child(details.message.clone()),
                            )
                            .child(
                                div()
                                    .text_color(muted)
                                    .child(format!("{} {}", short(&details.id), details.author))
                                    .child(div().text_xs().child(format!(
                                        "<{}> on {}",
                                        details.email,
                                        format_full_time(details.author_time)
                                    ))),
                            )
                            .when(details.committer != details.author || details.commit_time != details.author_time, |col| {
                                col.child(div().text_xs().text_color(muted).child(format!(
                                    "Committed by {} on {}",
                                    details.committer,
                                    format_full_time(details.commit_time)
                                )))
                            })
                            .when(details.parents.len() > 1, |col| {
                                col.child(div().text_xs().text_color(muted).child(format!(
                                    "Merge of {}",
                                    details.parents.iter().map(|p| short(p)).collect::<Vec<_>>().join(", ")
                                )))
                            })
                            .child(div().text_xs().text_color(muted).whitespace_normal().child(branches)),
                    )
                    .into_any_element()
            }
        };

        v_resizable("log-details")
            .child(resizable_panel().child(files_panel))
            .child(resizable_panel().size(px(220.)).child(info))
    }
}

impl Render for LogView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        v_resizable("log-main")
            .child(
                resizable_panel().child(
                    h_resizable("log-columns")
                        .child(resizable_panel().size(px(230.)).size_range(px(140.)..px(500.)).child(self.render_branches(cx)))
                        .child(resizable_panel().child(self.render_commits(cx)))
                        .child(resizable_panel().size(px(340.)).size_range(px(200.)..px(800.)).child(self.render_details(cx))),
                ),
            )
            .child(resizable_panel().size(px(300.)).child(self.diff.clone()))
    }
}

fn first_file_index(
    files: &HashMap<SharedString, FileChange>,
    tree: &Entity<TreeState>,
    cx: &App,
) -> Option<usize> {
    let tree = tree.read(cx);
    (0..)
        .map_while(|ix| tree.entry(ix))
        .position(|entry| files.contains_key(&entry.item().id))
}

fn format_track(track: &str) -> String {
    track
        .split(", ")
        .map(|part| {
            if let Some(n) = part.strip_prefix("ahead ") {
                format!("↑{n}")
            } else if let Some(n) = part.strip_prefix("behind ") {
                format!("↓{n}")
            } else {
                part.to_string()
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

pub fn panel_header(title: impl Into<SharedString>, cx: &App) -> impl IntoElement {
    div()
        .flex_none()
        .h(px(28.))
        .px_2()
        .flex()
        .items_center()
        .text_xs()
        .font_semibold()
        .text_color(cx.theme().muted_foreground)
        .border_b_1()
        .border_color(cx.theme().border)
        .child(title.into())
}

fn paint_graph(row: &GraphRow, is_head: bool, bounds: Bounds<Pixels>, window: &mut Window, cx: &App) {
    let x = |lane: usize| bounds.origin.x + GRAPH_PADDING + LANE_WIDTH * (lane as f32 + 0.5);
    let top = bounds.origin.y;
    let mid = top + bounds.size.height / 2.;
    let bottom = top + bounds.size.height;
    for edge in &row.edges {
        let (y0, y1) = match edge.half {
            Half::Top => (top, mid),
            Half::Bottom => (mid, bottom),
        };
        let mut path = PathBuilder::stroke(px(1.5));
        path.move_to(point(x(edge.from), y0));
        path.line_to(point(x(edge.to), y1));
        if let Ok(path) = path.build() {
            window.paint_path(path, lane_color(edge.color));
        }
    }
    let radius = if is_head { px(4.5) } else { px(3.5) };
    let center = point(x(row.node), mid);
    let color = lane_color(row.color);
    let node = Bounds::new(point(center.x - radius, center.y - radius), size(radius * 2., radius * 2.));
    if is_head {
        window.paint_quad(
            quad(node, radius, cx.theme().background, px(2.), color, BorderStyle::default()),
        );
    } else {
        window.paint_quad(fill(node, color).corner_radii(radius));
    }
}
