//! The Git "Log" tab: branches, commit graph, commit details and diff preview.

use std::collections::{HashMap, HashSet};
use std::rc::Rc;
use std::sync::Arc;

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, h_resizable,
    input::{Input, InputEvent, InputState},
    list::ListItem,
    menu::{ContextMenuExt as _, DropdownMenu as _, PopupMenu, PopupMenuItem},
    resizable_panel,
    tree::{TreeItem, TreeState, tree},
    v_flex, v_resizable,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::actions::{branch_menu, commit_menu, commits_menu, short};
use crate::diff_pane::DiffPane;
use crate::git::{Branch, Commit, CommitDetails, FileChange, LogFilter, RefKind};
use crate::graph::{self, GraphRow, Half};
use crate::model::{RepoEvent, RepoModel};
use crate::settings::Settings;
use crate::ui::{cached, SubView, selection_bg, FILE_PREFIX, file_count, file_tree, format_full_time, format_time, lane_color, path_tree, ref_colors, status_color};
use crate::i18n::tr;

const ROW_HEIGHT: Pixels = px(24.);
const LANE_WIDTH: Pixels = px(14.);
const GRAPH_PADDING: Pixels = px(6.);
const BRANCH_PREFIX: &str = "b:";

actions!(
    commit_list,
    [SelectPrevCommit, SelectNextCommit, SelectFirstCommit, SelectLastCommit, PageUpCommits, PageDownCommits]
);

/// `git log --since` values of the Date filter, and their labels.
const DATE_FILTERS: [&str; 4] = ["24.hours.ago", "7.days.ago", "30.days.ago", "1.year.ago"];

fn date_filter_label(ix: usize) -> &'static str {
    match ix {
        0 => tr!("Last 24 Hours"),
        1 => tr!("Last 7 Days"),
        2 => tr!("Last 30 Days"),
        _ => tr!("Last Year"),
    }
}

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
    /// Commits selected along with `selected` by Cmd- or Shift-click.
    marked: HashSet<String>,
    /// The commit whose context menu is opening.
    menu_target: Option<String>,
    /// Where a Shift-click range starts.
    anchor: Option<String>,
    head: Option<String>,
    filter: Entity<InputState>,
    branch_filter: Option<String>,
    /// Authors to show, any of them; git does the filtering.
    author_filter: Vec<String>,
    /// Authors seen in the log while it wasn't filtered by author, most
    /// active first, to choose from.
    known_authors: Vec<String>,
    /// `user.name`, for "Me".
    me: Option<String>,
    /// Index into `DATE_FILTERS`.
    date_filter: Option<usize>,
    /// Files or folders relative to the repository root, any of them.
    path_filter: Vec<String>,
    /// Paths filtered by before, newest first, to pick again.
    recent_paths: Vec<String>,
    branch_tree: Entity<TreeState>,
    branches: Rc<HashMap<SharedString, Branch>>,
    selected_branch: Option<SharedString>,
    details: Option<Arc<CommitDetails>>,
    /// Which parent of a merge commit its changes are shown against.
    base: usize,
    file_tree: Entity<TreeState>,
    files: Rc<HashMap<SharedString, FileChange>>,
    selected_file: Option<SharedString>,
    diff: Entity<DiffPane>,
    panels: Option<Panels>,
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
    _subscriptions: Vec<Subscription>,
}

impl LogView {
    pub fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let filter = cx.new(|cx| {
            InputState::new(window, cx).placeholder(tr!("Text or hash"))
        });
        let me = model.read(cx).repo.user_name();
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
            cx.subscribe_in(&filter, window, |this, _, event, window, cx| match event {
                InputEvent::Change => this.apply_filter(cx),
                // Enter goes to the matching commits, for the arrow keys.
                InputEvent::PressEnter { .. } => this.focus.focus(window, cx),
                _ => {}
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
            marked: HashSet::new(),
            menu_target: None,
            anchor: None,
            head: None,
            filter,
            branch_filter: None,
            date_filter: None,
            path_filter: Vec::new(),
            recent_paths: Vec::new(),
            author_filter: Vec::new(),
            known_authors: Vec::new(),
            me,
            branch_tree,
            branches: Rc::default(),
            selected_branch: None,
            base: 0,
            details: None,
            file_tree,
            files: Rc::default(),
            selected_file: None,
            diff,
            panels: None,
            scroll: UniformListScrollHandle::new(),
            focus: cx.focus_handle(),
            loading: true,
            limit: Settings::read(|s| s.log_page_size),
            refs_version: 0,
            load_more_scheduled: false,
            log_task: None,
            details_task: None,
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

    /// The search box, which ⌘F / Ctrl+F focuses.
    pub fn search_focus(&self, cx: &App) -> FocusHandle {
        self.filter.focus_handle(cx)
    }

    pub fn reload(&mut self, cx: &mut Context<Self>) {
        let repo = self.model.read(cx).repo.clone();
        let filter = LogFilter {
            refs: self.branch_filter.clone(),
            since: self.date_filter.map(|ix| DATE_FILTERS[ix].to_string()),
            authors: self.author_filter.clone(),
            paths: self.path_filter.clone(),
        };
        let limit = self.limit;
        self.loading = true;
        cx.notify();
        self.log_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    let commits = repo.log(&filter, limit)?;
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
                        if this.author_filter.is_empty() {
                            this.known_authors = this.authors();
                        }
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
        for (label, kind) in [(tr!("Local"), RefKind::Local), (tr!("Remote"), RefKind::Remote), (tr!("Tags"), RefKind::Tag)] {
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
        let rows: Vec<usize> = if query.is_empty() {
            (0..self.commits.len()).collect()
        } else {
            self.commits
                .iter()
                .enumerate()
                .filter(|(_, c)| {
                    c.id.starts_with(&query)
                        || c.subject.to_lowercase().contains(&query)
                        || c.author.to_lowercase().contains(&query)
                        || c.refs.iter().any(|r| r.name.to_lowercase().contains(&query))
                })
                .map(|(ix, _)| ix)
                .collect()
        };
        self.rows = Rc::new(rows);
        let visible: HashSet<&str> = self.rows.iter().map(|&ix| self.commits[ix].id.as_str()).collect();
        self.marked.retain(|id| visible.contains(id.as_str()));
        if self.marked.len() < 2 {
            self.marked.clear();
        }
        cx.notify();
    }

    fn is_filtered(&self, cx: &App) -> bool {
        !self.filter.read(cx).value().trim().is_empty()
    }

    /// Filters git applies, which leave gaps in the history.
    fn is_history_filtered(&self) -> bool {
        self.date_filter.is_some() || !self.path_filter.is_empty() || !self.author_filter.is_empty()
    }

    fn set_date_filter(&mut self, filter: Option<usize>, cx: &mut Context<Self>) {
        self.date_filter = filter;
        self.reload(cx);
    }

    fn clear_path_filter(&mut self, cx: &mut Context<Self>) {
        self.path_filter.clear();
        self.reload(cx);
    }

    /// Add `path` to the paths shown, or take it out if it's there.
    fn toggle_path_filter(&mut self, path: String, cx: &mut Context<Self>) {
        if let Some(ix) = self.path_filter.iter().position(|p| *p == path) {
            self.path_filter.remove(ix);
        } else {
            self.recent_paths.retain(|p| *p != path);
            self.recent_paths.insert(0, path.clone());
            self.recent_paths.truncate(8);
            self.path_filter.push(path);
        }
        self.reload(cx);
    }

    fn choose_path_filter(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let root = self.model.read(cx).repo.root().to_path_buf();
        let paths = cx.prompt_for_paths(PathPromptOptions {
            files: true,
            directories: true,
            multiple: false,
            prompt: Some(tr!("Filter").into()),
        });
        cx.spawn_in(window, async move |this, cx| {
            let Ok(Ok(Some(paths))) = paths.await else { return };
            let Some(path) = paths.into_iter().next() else { return };
            let root = std::fs::canonicalize(&root).unwrap_or(root);
            let path = std::fs::canonicalize(&path).unwrap_or(path);
            this.update(cx, |this, cx| match path.strip_prefix(&root) {
                Ok(relative) if relative.as_os_str().is_empty() => this.clear_path_filter(cx),
                Ok(relative) => {
                    let path = relative.to_string_lossy().replace('\\', "/");
                    if !this.path_filter.contains(&path) {
                        this.toggle_path_filter(path, cx);
                    }
                }
                Err(_) => this.report(anyhow::anyhow!("{}", tr!("{} is not inside the repository", path.display())), cx),
            })
            .ok();
        })
        .detach();
    }

    fn clear_author_filter(&mut self, cx: &mut Context<Self>) {
        self.author_filter.clear();
        self.reload(cx);
    }

    /// Add `author` to the authors shown, or take them out if they're there.
    fn toggle_author_filter(&mut self, author: String, cx: &mut Context<Self>) {
        if let Some(ix) = self.author_filter.iter().position(|a| *a == author) {
            self.author_filter.remove(ix);
        } else {
            self.author_filter.push(author);
        }
        self.reload(cx);
    }

    /// The authors to choose from: those seen, plus any chosen that weren't.
    fn author_choices(&self) -> Vec<String> {
        let mut authors = self.known_authors.clone();
        for author in &self.author_filter {
            if !authors.contains(author) {
                authors.push(author.clone());
            }
        }
        authors
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
        self.marked.clear();
        self.anchor = id.clone();
        if self.selected == id && self.details.is_some() {
            cx.notify();
            return;
        }
        self.selected = id;
        self.base = 0;
        self.load_details(cx);
        cx.notify();
    }

    fn is_selected(&self, id: &str) -> bool {
        self.selected.as_deref() == Some(id) || self.marked.contains(id)
    }

    /// Cmd-click toggles a commit in the selection; Shift-click selects the
    /// range from the last plain click.
    fn click_commit(&mut self, ix: usize, modifiers: Modifiers, cx: &mut Context<Self>) {
        let id = self.commits[ix].id.clone();
        let row_of = |id: &str| self.rows.iter().position(|&ix| self.commits[ix].id == id);
        if modifiers.shift
            && let Some((from, to)) = self.anchor.as_deref().and_then(row_of).zip(row_of(&id))
        {
            let anchor = self.anchor.clone();
            let range = from.min(to)..=from.max(to);
            let marked = self.rows[range].iter().map(|&ix| self.commits[ix].id.clone()).collect();
            self.select(Some(id), cx);
            self.marked = marked;
            self.anchor = anchor;
        } else if modifiers.secondary(cx) {
            let mut marked = self.marked.clone();
            marked.extend(self.selected.clone());
            let primary = if marked.remove(&id) {
                // Show the details of another selected commit.
                if marked.is_empty() {
                    return;
                }
                self.rows
                    .iter()
                    .map(|&ix| &self.commits[ix].id)
                    .find(|id| marked.contains(*id))
                    .cloned()
            } else {
                marked.insert(id.clone());
                Some(id)
            };
            self.select(primary, cx);
            if marked.len() > 1 {
                self.marked = marked;
            }
        } else {
            self.select(Some(id), cx);
        }
        cx.notify();
    }

    /// The selected commits, newest first, when the menu opens on one of
    /// several of them.
    fn selection_for_menu(&self, id: &str) -> Vec<String> {
        if self.marked.len() < 2 || !self.marked.contains(id) {
            return Vec::new();
        }
        self.rows
            .iter()
            .map(|&ix| &self.commits[ix].id)
            .filter(|id| self.marked.contains(*id))
            .cloned()
            .collect()
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
        let base = self.base;
        self.details_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.commit_details_against(&id, base) })
                .await;
            this.update(cx, |this, cx| match result {
                Ok(details) => {
                    let same_commit = this.details.as_ref().is_some_and(|d| d.id == details.id && d.base == details.base);
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
                            this.diff.update(cx, |diff, cx| diff.show_message(None, tr!("No changed files"), cx));
                        }
                    }
                    cx.notify();
                }
                Err(err) => this.report(err, cx),
            })
            .ok();
        }));
    }

    fn set_base(&mut self, base: usize, cx: &mut Context<Self>) {
        if self.base != base {
            self.base = base;
            self.load_details(cx);
        }
    }

    /// For a merge commit: which parent its changes are shown against.
    fn render_parent_choice(&self, details: &CommitDetails, cx: &mut Context<Self>) -> impl IntoElement {
        h_flex()
            .flex_none()
            .px_2()
            .py_1()
            .gap_1()
            .flex_wrap()
            .border_b_1()
            .border_color(cx.theme().border)
            .child(div().text_xs().text_color(cx.theme().muted_foreground).child(tr!("Compare with")))
            .children(details.parents.iter().enumerate().map(|(ix, parent)| {
                let button = Button::new(("merge-parent", ix))
                    .label(tr!("Parent {} ({})", ix + 1, short(parent)))
                    .xsmall()
                    .on_click(cx.listener(move |this, _, _, cx| this.set_base(ix, cx)));
                if ix == details.base { button.primary() } else { button.ghost() }
            }))
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
        self.diff
            .update(cx, |diff, cx| diff.load(title, move || repo.commit_file_diff(&details, &file), cx));
    }

    fn render_branches(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let branches = self.branches.clone();
        let menu_branches = self.branches.clone();
        let model = self.model.clone();
        v_flex()
            .size_full()
            .bg(cx.theme().sidebar)
            .child(panel_header(tr!("Branches"), cx))
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

    fn branch_names(&self, cx: &App) -> Vec<String> {
        let model = self.model.read(cx);
        let mut names: Vec<String> = model
            .branches_of(RefKind::Local)
            .chain(model.branches_of(RefKind::Remote))
            .map(|b| b.name.clone())
            .collect();
        names.dedup();
        names
    }

    // The menus below gather their items when opened, not on every render.
    fn render_toolbar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let view = cx.entity().downgrade();
        let label = match &self.branch_filter {
            Some(branch) => tr!("Branch: {}", branch),
            None => tr!("Branch: All").into(),
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
                    .dropdown_menu(move |mut menu, _, cx| {
                        let names = view.upgrade().map(|view| view.read(cx).branch_names(cx)).unwrap_or_default();
                        let view = view.clone();
                        menu = menu.item(PopupMenuItem::new(tr!("All")).on_click({
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
                Button::new("author-filter")
                    .label(match self.author_filter.as_slice() {
                        [] => tr!("User: All").into(),
                        [author] if Some(author) == self.me.as_ref() => tr!("User: Me").into(),
                        [author] => tr!("User: {}", author),
                        [author, rest @ ..] => tr!("User: {} +{}", author, rest.len()),
                    })
                    .ghost()
                    .xsmall()
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, cx| {
                        let Some(this) = view.upgrade() else { return menu };
                        let (authors, chosen, me) = {
                            let this = this.read(cx);
                            (this.author_choices(), this.author_filter.clone(), this.me.clone())
                        };
                        let all = view.clone();
                        menu = menu.item(
                            PopupMenuItem::new(tr!("All")).checked(chosen.is_empty()).on_click(move |_, _, cx| {
                                all.update(cx, |this, cx| this.clear_author_filter(cx)).ok();
                            }),
                        );
                        if let Some(me) = me.clone() {
                            let view = view.clone();
                            menu = menu.item(
                                PopupMenuItem::new(tr!("Me ({})", me))
                                    .checked(chosen.contains(&me))
                                    .on_click(move |_, _, cx| {
                                        let me = me.clone();
                                        view.update(cx, |this, cx| this.toggle_author_filter(me, cx)).ok();
                                    }),
                            );
                        }
                        menu = menu.separator();
                        for author in authors.into_iter().filter(|author| Some(author) != me.as_ref()) {
                            let view = view.clone();
                            menu = menu.item(
                                PopupMenuItem::new(author.clone())
                                    .checked(chosen.contains(&author))
                                    .on_click(move |_, _, cx| {
                                        let author = author.clone();
                                        view.update(cx, |this, cx| this.toggle_author_filter(author, cx)).ok();
                                    }),
                            );
                        }
                        menu.max_h(px(480.)).scrollable(true)
                    })
            })
            .child({
                let view = cx.entity().downgrade();
                Button::new("date-filter")
                    .label(match self.date_filter {
                        Some(ix) => tr!("Date: {}", date_filter_label(ix)),
                        None => tr!("Date: All").into(),
                    })
                    .ghost()
                    .xsmall()
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, _| {
                        let all = view.clone();
                        menu = menu
                            .item(PopupMenuItem::new(tr!("All")).on_click(move |_, _, cx| {
                                all.update(cx, |this, cx| this.set_date_filter(None, cx)).ok();
                            }))
                            .separator();
                        for ix in 0..DATE_FILTERS.len() {
                            let view = view.clone();
                            menu = menu.item(PopupMenuItem::new(date_filter_label(ix)).on_click(move |_, _, cx| {
                                view.update(cx, |this, cx| this.set_date_filter(Some(ix), cx)).ok();
                            }));
                        }
                        menu
                    })
            })
            .child({
                let view = cx.entity().downgrade();
                Button::new("path-filter")
                    .label(match self.path_filter.as_slice() {
                        [] => tr!("Paths: All").into(),
                        [path] => tr!("Paths: {}", path),
                        [path, rest @ ..] => tr!("Paths: {} +{}", path, rest.len()),
                    })
                    .ghost()
                    .xsmall()
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, cx| {
                        let Some(this) = view.upgrade() else { return menu };
                        let (recent, chosen) = {
                            let this = this.read(cx);
                            let mut paths = this.path_filter.clone();
                            paths.extend(this.recent_paths.iter().filter(|p| !this.path_filter.contains(p)).cloned());
                            (paths, this.path_filter.clone())
                        };
                        let all = view.clone();
                        menu = menu.item(
                            PopupMenuItem::new(tr!("All")).checked(chosen.is_empty()).on_click(move |_, _, cx| {
                                all.update(cx, |this, cx| this.clear_path_filter(cx)).ok();
                            }),
                        );
                        if !recent.is_empty() {
                            menu = menu.separator();
                        }
                        for path in recent {
                            let view = view.clone();
                            menu = menu.item(
                                PopupMenuItem::new(path.clone()).checked(chosen.contains(&path)).on_click(move |_, _, cx| {
                                    let path = path.clone();
                                    view.update(cx, |this, cx| this.toggle_path_filter(path, cx)).ok();
                                }),
                            );
                        }
                        let choose = view.clone();
                        menu.separator()
                            .item(PopupMenuItem::new(tr!("Select File or Folder…")).on_click(move |_, window, cx| {
                                choose.update(cx, |this, cx| this.choose_path_filter(window, cx)).ok();
                            }))
                    })
            })
            .child(div().flex_1())
            .when(self.loading, |row| {
                row.child(div().text_xs().text_color(cx.theme().muted_foreground).child(tr!("Loading…")))
            })
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("{} commits", self.rows.len())),
            )
            .child(
                Button::new("log-refresh")
                    .icon(IconName::RefreshCw)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Refresh"))
                    .on_click(cx.listener(|this, _, _, cx| {
                        this.model.update(cx, |model, cx| model.refresh(cx));
                        this.reload(cx);
                    })),
            )
    }

    fn render_commits(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let show_graph = !self.is_filtered(cx) && !self.is_history_filtered();
        let has_more = !self.is_filtered(cx) && self.commits.len() >= self.limit;
        v_flex()
            .size_full()
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
                    .context_menu({
                        let view = cx.entity().downgrade();
                        move |menu, _, cx| {
                            let Some(target) = view.update(cx, |this, _| this.menu_target.take()).ok().flatten() else {
                                return menu;
                            };
                            match view.upgrade() {
                                Some(view) => view.read(cx).commit_list_menu(menu, target, cx),
                                None => menu,
                            }
                        }
                    })
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
                tr!("Loading…").to_string()
            } else {
                tr!("Showing the first {} commits. Load more…", self.limit)
            })
            .on_click(cx.listener(|this, _, _, cx| this.load_more(cx)))
            .into_any_element()
    }

    fn load_more(&mut self, cx: &mut Context<Self>) {
        self.load_more_scheduled = false;
        if !self.loading && self.commits.len() >= self.limit {
            self.limit += Settings::read(|s| s.log_page_size);
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
        let selected = self.is_selected(&commit.id);
        let is_head = self.head.as_deref() == Some(commit.id.as_str());
        let graph_row = self.graph.get(ix).cloned().unwrap_or_default();
        let graph_width = if show_graph {
            GRAPH_PADDING * 2. + LANE_WIDTH * graph_row.width as f32
        } else {
            px(8.)
        };
        let muted = cx.theme().muted_foreground;
        let fg = cx.theme().foreground;
        // One canvas paints the whole row, as laying out a dozen flex children
        // per row on every scroll step made scrolling costly.
        let paint = RowPaint {
            graph: show_graph.then_some(graph_row),
            graph_width,
            is_head,
            refs: commit
                .refs
                .iter()
                .map(|r| {
                    let (bg, fg) = ref_colors(&r.kind, cx);
                    (SharedString::from(r.name.clone()), bg, fg)
                })
                .collect(),
            subject: commit.subject.clone().into(),
            author: commit.author.clone().into(),
            time: format_time(commit.time).into(),
            fg,
            secondary: if selected { fg } else { muted },
        };

        div()
            .id(row)
            .h(ROW_HEIGHT)
            .w_full()
            .text_sm()
            .when(selected, |row| row.bg(selection_bg()))
            .when(!selected, |row| row.hover(|style| style.bg(cx.theme().list_hover)))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                    this.focus.focus(window, cx);
                    this.click_commit(ix, event.modifiers, cx);
                }),
            )
            .on_mouse_down(
                MouseButton::Right,
                cx.listener(move |this, _, _, cx| {
                    let id = this.commits[ix].id.clone();
                    if !this.is_selected(&id) {
                        this.select(Some(id.clone()), cx)
                    }
                    this.menu_target = Some(id);
                }),
            )
            .child(canvas(|_, _, _| {}, move |bounds, _, window, cx| paint.paint(bounds, window, cx)).size_full())
            .into_any_element()
    }

    /// The menu for the commit last right-clicked, or for the selection it
    /// belongs to. One menu serves the whole list.
    fn commit_list_menu(&self, menu: PopupMenu, id: String, cx: &App) -> PopupMenu {
        let selection = self.selection_for_menu(&id);
        if !selection.is_empty() {
            return commits_menu(menu, &self.model, selection);
        }
        let commit = self.commits.iter().find(|c| c.id == id);
        let subject = commit.map(|c| c.subject.clone()).unwrap_or_default();
        let is_head = commit.is_some_and(|c| c.refs.iter().any(|r| r.kind == crate::git::RefKind::Head));
        let web = self.model.read(cx).web.clone();
        commit_menu(menu, &self.model, id, subject, is_head, web)
    }

    fn render_details(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let files = self.files.clone();
        let menu_files = self.files.clone();
        let web = self.model.read(cx).web.clone();
        let view = cx.entity().downgrade();
        let model = self.model.clone();
        let rev = self.details.as_ref().map(|d| d.id.clone());
        let base = self.details.as_ref().and_then(|d| d.base_parent().map(str::to_string));
        let changed = self.details.as_ref().map(|d| d.files.len()).unwrap_or(0);
        let files_panel = v_flex()
            .size_full()
            .child(panel_header(tr!("Changed Files ({})", changed), cx))
            .when_some(self.details.as_ref().filter(|d| d.parents.len() > 1), |col, details| {
                col.child(self.render_parent_choice(details, cx))
            })
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
                let Some(rev) = rev.clone() else { return menu };
                let id = entry.item().id.as_ref();
                // The file, or every file under the folder, with where renamed ones came from.
                let chosen: Vec<&FileChange> = match id.strip_prefix(&format!("d:{FILE_PREFIX}")) {
                    Some(dir) => {
                        let dir = format!("{dir}/");
                        menu_files.values().filter(|f| f.path.starts_with(&dir)).collect()
                    }
                    None => menu_files.get(id).into_iter().collect(),
                };
                let count = chosen.len();
                let mut paths: Vec<String> = chosen.iter().map(|f| f.path.clone()).collect();
                paths.extend(chosen.iter().filter_map(|f| f.orig_path.clone()));
                let mut menu = menu;
                for (label, revert) in [(tr!("Cherry-Pick Selected Changes"), false), (tr!("Revert Selected Changes"), true)] {
                    if paths.is_empty() {
                        break;
                    }
                    let (m, r, b, paths) = (model.clone(), rev.clone(), base.clone(), paths.clone());
                    menu = menu.item(PopupMenuItem::new(label).on_click(move |_, _, cx| {
                        let (rev, paths) = (r.clone(), paths.clone());
                        let label = if revert {
                            tr!("Revert {} from {}", file_count(count), crate::actions::short(&rev))
                        } else {
                            tr!("Cherry-pick {} from {}", file_count(count), crate::actions::short(&rev))
                        };
                        let base = b.clone();
                        m.update(cx, |m, cx| {
                            m.run(label, move |repo| repo.apply_commit_changes(&rev, base.as_deref(), &paths, revert), cx)
                        });
                    }));
                }
                let Some(change) = menu_files.get(id) else { return menu };
                let menu = menu.separator();
                let path = change.path.clone();
                let (m, p) = (model.clone(), path.clone());
                let menu = menu.item(PopupMenuItem::new(tr!("Show History")).on_click(move |_, window, cx| {
                    crate::history::show_history(m.clone(), p.clone(), window, cx)
                }));
                let (v, p) = (view.clone(), path.clone());
                let menu = menu.item(PopupMenuItem::new(tr!("Filter the Log by This File")).on_click(move |_, _, cx| {
                    let path = p.clone();
                    v.update(cx, |this, cx| {
                        this.path_filter.clear();
                        this.toggle_path_filter(path, cx);
                    })
                    .ok();
                }));
                let (m, p, r) = (model.clone(), path.clone(), rev.clone());
                let menu = menu.item(PopupMenuItem::new(tr!("Compare with Local")).on_click(move |_, window, cx| {
                    crate::worktree_diff::open_between(m.clone(), r.clone(), None, Some(p.clone()), window, cx)
                }));
                let (m, p, r) = (model.clone(), path.clone(), rev.clone());
                let menu = menu.item(PopupMenuItem::new(tr!("Get from Revision")).on_click(move |_, window, cx| {
                    let (model, path, rev) = (m.clone(), p.clone(), r.clone());
                    let run = move |_: &mut Window, cx: &mut App| {
                        let (path, rev) = (path.clone(), rev.clone());
                        let label = tr!("Get {} from {}", path, crate::actions::short(&rev));
                        model.update(cx, |m, cx| m.run(label, move |repo| repo.get_from_revision(&rev, &path), cx));
                    };
                    if m.read(cx).status.iter().any(|e| e.path == p) {
                        crate::ui::confirm(
                            tr!("Get from Revision"),
                            tr!("{} has local changes. Replace it with its version in {}?", p, crate::actions::short(&r)),
                            tr!("Replace"),
                            run,
                            window,
                            cx,
                        );
                    } else {
                        run(window, cx);
                    }
                }));
                if change.status == 'D' {
                    return menu;
                }
                let menu = match &web {
                    Some(web) => {
                        let url = web.file_url(&rev, &path, None);
                        let open = url.clone();
                        menu.item(PopupMenuItem::new(tr!("Open on {}", web.name())).on_click(move |_, _, cx| cx.open_url(&open)))
                            .item(PopupMenuItem::new(tr!("Copy Link to File")).on_click(move |_, _, cx| {
                                cx.write_to_clipboard(ClipboardItem::new_string(url.clone()))
                            }))
                    }
                    None => menu,
                };
                let m = model.clone();
                menu.item(PopupMenuItem::new(tr!("Annotate at This Revision")).on_click(move |_, window, cx| {
                    crate::history::annotate(m.clone(), rev.clone(), path.clone(), window, cx)
                }))
            })));

        let info = match &self.details {
            None => div()
                .p_3()
                .text_sm()
                .text_color(cx.theme().muted_foreground)
                .child(tr!("No commit selected"))
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
                    tr!("In {} branches: {}", details.branches.len(), details.branches.join(", "))
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
                                col.child(div().text_xs().text_color(muted).child(tr!(
                                    "Committed by {} on {}",
                                    details.committer,
                                    format_full_time(details.commit_time)
                                )))
                            })
                            .when(details.parents.len() > 1, |col| {
                                col.child(div().text_xs().text_color(muted).child(tr!(
                                    "Merge of {}",
                                    details.parents.iter().map(|p| short(p)).collect::<Vec<_>>().join(", ")
                                )))
                            })
                            .child(div().text_xs().text_color(muted).whitespace_normal().child(branches))
                            .when_some(signature_line(&details.signature, cx), |col, line| col.child(line)),
                    )
                    .into_any_element()
            }
        };

        v_resizable("log-details")
            .child(resizable_panel().child(files_panel))
            .child(resizable_panel().size(px(220.)).child(info))
    }
}

/// The log's columns as separate cached views: scrolling the commits then
/// leaves the branches, details and diff as they were instead of laying
/// them out and painting them again on every step.
#[derive(Clone)]
struct Panels {
    branches: Entity<SubView<LogView>>,
    toolbar: Entity<SubView<LogView>>,
    commits: Entity<SubView<LogView>>,
    details: Entity<SubView<LogView>>,
}

impl Render for LogView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let log = cx.entity();
        let panels = self
            .panels
            .get_or_insert_with(|| {
                let panels = Panels {
                    branches: SubView::new(&log, |log, cx| log.render_branches(cx).into_any_element(), cx),
                    toolbar: SubView::new(&log, |log, cx| log.render_toolbar(cx).into_any_element(), cx),
                    commits: SubView::new(&log, |log, cx| log.render_commits(cx).into_any_element(), cx),
                    details: SubView::new(&log, |log, cx| log.render_details(cx).into_any_element(), cx),
                };
                // Expanding a folder changes only the tree's own state.
                SubView::observe(&panels.branches, &self.branch_tree, cx);
                SubView::observe(&panels.details, &self.file_tree, cx);
                panels
            })
            .clone();
        v_resizable("log-main")
            .child(
                resizable_panel().child(
                    h_resizable("log-columns")
                        .child(resizable_panel().size(px(230.)).size_range(px(140.)..px(500.)).child(cached(panels.branches)))
                        .child(
                            resizable_panel().child(
                                v_flex()
                                    .size_full()
                                    .child(div().flex_none().h(px(36.)).child(cached(panels.toolbar)))
                                    .child(div().flex_1().min_h_0().child(cached(panels.commits))),
                            ),
                        )
                        .child(resizable_panel().size(px(340.)).size_range(px(200.)..px(800.)).child(cached(panels.details))),
                ),
            )
            .child(resizable_panel().size(px(300.)).child(cached(self.diff.clone())))
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

/// Everything a commit row draws, captured for its canvas.
struct RowPaint {
    graph: Option<GraphRow>,
    graph_width: Pixels,
    is_head: bool,
    /// Label, background and text color of each ref.
    refs: Vec<(SharedString, Hsla, Hsla)>,
    subject: SharedString,
    author: SharedString,
    time: SharedString,
    fg: Hsla,
    /// Author and date.
    secondary: Hsla,
}

const AUTHOR_WIDTH: Pixels = px(140.);
const TIME_WIDTH: Pixels = px(130.);
const CELL_PADDING: Pixels = px(8.);

impl RowPaint {
    fn paint(&self, bounds: Bounds<Pixels>, window: &mut Window, cx: &mut App) {
        if let Some(graph) = &self.graph {
            let graph_bounds = Bounds::new(bounds.origin, size(self.graph_width, bounds.size.height));
            paint_graph(graph, self.is_head, graph_bounds, window, cx);
        }
        let style = window.text_style();
        let font = style.font();
        let font_size = style.font_size.to_pixels(window.rem_size());
        let line_height = style.line_height_in_pixels(window.rem_size());
        let text_top = bounds.origin.y + (bounds.size.height - line_height) / 2.;

        let right = bounds.right() - CELL_PADDING;
        let time_left = right - TIME_WIDTH;
        let author_left = time_left - AUTHOR_WIDTH;
        let mut x = bounds.origin.x + self.graph_width;

        // Ref labels, then the subject in what is left of the column.
        let label_size = font_size * 0.86;
        let label_height = px(16.);
        for (name, bg, fg) in &self.refs {
            let line = shape(name.clone(), &font, label_size, *fg, window);
            let width = line.width + px(8.);
            if x + width > author_left - CELL_PADDING {
                break;
            }
            let label = Bounds::new(
                point(x, bounds.origin.y + (bounds.size.height - label_height) / 2.),
                size(width, label_height),
            );
            window.paint_quad(fill(label, *bg).corner_radii(px(3.)));
            let label_line_height = label_size * 1.3;
            line.paint(
                point(x + px(4.), label.origin.y + (label_height - label_line_height) / 2.),
                label_line_height,
                TextAlign::Left,
                None,
                window,
                cx,
            )
            .ok();
            x += width + px(4.);
        }
        let cells = [
            (&self.subject, self.fg, x, author_left - CELL_PADDING),
            (&self.author, self.secondary, author_left + CELL_PADDING, time_left),
            (&self.time, self.secondary, time_left + CELL_PADDING, right),
        ];
        for (text, color, left, right) in cells {
            let width = right - left;
            if width <= px(0.) || text.is_empty() {
                continue;
            }
            let run = [TextRun { len: text.len(), font: font.clone(), color, background_color: None, underline: None, strikethrough: None }];
            let (text, runs) = window
                .text_system()
                .line_wrapper(font.clone(), font_size)
                .truncate_line((*text).clone(), width, "…", &run, TruncateFrom::End);
            let line = window.text_system().shape_line(text, font_size, &runs, None);
            line.paint(point(left, text_top), line_height, TextAlign::Left, None, window, cx).ok();
        }
    }
}

fn shape(text: SharedString, font: &Font, font_size: Pixels, color: Hsla, window: &Window) -> ShapedLine {
    let run = TextRun { len: text.len(), font: font.clone(), color, background_color: None, underline: None, strikethrough: None };
    window.text_system().shape_line(text, font_size, &[run], None)
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

/// How a commit's signature checks out, in a line for its details.
fn signature_line(signature: &crate::git::Signature, cx: &App) -> Option<AnyElement> {
    use crate::git::Signature;
    let theme = cx.theme();
    let (text, color) = match signature {
        Signature::None => return None,
        Signature::Good(signer) => (tr!("Signed by {} (verified)", signer), theme.success),
        Signature::Untrusted(signer) => (tr!("Signed by {} (key not trusted)", signer), theme.warning),
        Signature::Bad => (tr!("Bad signature").to_string(), theme.danger),
        Signature::Expired => (tr!("Signature or key expired or revoked").to_string(), theme.warning),
        Signature::Unchecked => (tr!("Signed, but the signature can't be checked").to_string(), theme.muted_foreground),
    };
    Some(div().text_xs().text_color(color).child(text).into_any_element())
}
