//! The "Commit" tab: local changes with checkboxes, commit message and diff preview.

use std::collections::{BTreeSet, HashMap, HashSet};

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex, h_resizable,
    input::{InputEvent, Textarea, TextareaState},
    menu::{ContextMenuExt as _, DropdownMenu as _, PopupMenuItem},
    resizable_panel, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::changelists::Changelists;
use crate::diff_pane::DiffPane;
use crate::git::{CommitOptions, RepoState, StatusEntry};
use crate::patch::{self, ChangedLine, Hunk};
use crate::log_view::panel_header;
use crate::model::{RepoEvent, RepoModel};
use crate::settings::Settings;
use crate::ui::{selection_bg, confirm, prompt_text, status_color};
use crate::i18n::tr;

#[derive(Clone, PartialEq, Eq, PartialOrd, Ord)]
enum Group {
    Conflicts,
    /// A changelist, by name.
    List(String),
    Unversioned,
}

impl Group {
    fn of(entry: &StatusEntry, lists: &Changelists) -> Self {
        if entry.is_conflicted() {
            Group::Conflicts
        } else if entry.is_untracked() {
            Group::Unversioned
        } else {
            Group::List(lists.list_of(&entry.path).to_string())
        }
    }

    fn title(&self) -> String {
        match self {
            Group::Conflicts => tr!("Merge Conflicts").into(),
            // The default list's name is shown in the interface language.
            Group::List(name) if name == crate::changelists::DEFAULT => tr!("Changes").to_string(),
            Group::List(name) => name.clone(),
            Group::Unversioned => tr!("Unversioned Files").into(),
        }
    }
}

pub struct ChangesView {
    model: Entity<RepoModel>,
    included: HashSet<String>,
    known: HashSet<String>,
    conflicted: HashSet<String>,
    /// Merge message to put into the editor on the next render.
    pending_message: Option<String>,
    /// The merge message put into the editor, cleared again when the merge
    /// ends if it was left as it was.
    prefilled_merge_message: Option<String>,
    collapsed: BTreeSet<Group>,
    selected: Option<String>,
    message: Entity<TextareaState>,
    amend: bool,
    /// Author, sign-off, signing and hooks for the next commit.
    options: CommitOptions,
    diff: Entity<DiffPane>,
    diff_task: Option<Task<()>>,
    /// Hunks of the selected file, when it can be committed partially.
    hunks: Vec<Hunk>,
    /// Changed lines left out of the commit, per file.
    excluded: HashMap<String, HashSet<ChangedLine>>,
    /// Hunks of the selected file showing their lines.
    expanded: HashSet<String>,
    _subscriptions: Vec<Subscription>,
}

impl ChangesView {
    pub fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let message = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder(tr!("Commit Message"))
                .auto_grow(4, 12)
        });
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.clear(cx));
        let subscriptions = vec![
            cx.subscribe(&model, |this, _, event, cx| match event {
                RepoEvent::Changed => this.sync_status(cx),
                // An undone commit's message, unless a new one is being written.
                RepoEvent::MessageRestored(message) if this.message.read(cx).value().trim().is_empty() => {
                    this.pending_message = Some(message.clone());
                    cx.notify();
                }
                _ => {}
            }),
            // Cmd-Enter in the message commits, as in JetBrains.
            cx.subscribe_in(&message, window, |this, _, event, window, cx| {
                match event {
                    InputEvent::PressEnter { secondary: true, .. } => this.commit(false, window, cx),
                    // The message checks below the box follow the text.
                    InputEvent::Change => cx.notify(),
                    _ => {}
                }
            }),
        ];
        let mut this = Self {
            model,
            included: HashSet::new(),
            known: HashSet::new(),
            conflicted: HashSet::new(),
            pending_message: None,
            prefilled_merge_message: None,
            collapsed: BTreeSet::new(),
            selected: None,
            message,
            amend: false,
            options: CommitOptions::default(),
            diff,
            diff_task: None,
            hunks: Vec::new(),
            excluded: HashMap::new(),
            expanded: HashSet::new(),
            _subscriptions: subscriptions,
        };
        this.sync_status(cx);
        this
    }

    pub fn message_focus(&self, cx: &App) -> FocusHandle {
        self.message.focus_handle(cx)
    }

    pub fn change_count(&self, cx: &App) -> usize {
        self.model.read(cx).status.len()
    }

    /// New tracked changes are included by default, unversioned files are not.
    fn sync_status(&mut self, cx: &mut Context<Self>) {
        let status = self.model.read(cx).status.clone();
        let lists = self.model.read(cx).changelists.clone();
        let paths: HashSet<String> = status.iter().map(|e| e.path.clone()).collect();
        for entry in &status {
            let is_new = self.known.insert(entry.path.clone());
            if entry.is_conflicted() {
                self.conflicted.insert(entry.path.clone());
            } else if self.conflicted.remove(&entry.path)
                || (is_new && !entry.is_untracked() && lists.list_of(&entry.path) == lists.active())
            {
                // Freshly resolved conflicts are ready to commit.
                self.included.insert(entry.path.clone());
            }
        }
        self.known.retain(|p| paths.contains(p));
        self.conflicted.retain(|p| paths.contains(p));

        let model = self.model.read(cx);
        let current = self.message.read(cx).value();
        if model.state == RepoState::Clean {
            if self.prefilled_merge_message.take().is_some_and(|message| message == current.trim()) {
                self.pending_message = Some(String::new());
            }
        } else if self.prefilled_merge_message.is_none() && current.trim().is_empty() {
            let message = model.repo.merge_message();
            self.prefilled_merge_message = Some(message.clone().unwrap_or_default());
            self.pending_message = message;
        }
        self.included.retain(|p| paths.contains(p));
        self.excluded.retain(|p, _| paths.contains(p));
        if self.selected.as_ref().is_some_and(|p| !paths.contains(p)) {
            self.selected = None;
        }
        if self.selected.is_none() {
            self.selected = status.first().map(|e| e.path.clone());
        }
        self.load_diff(cx);
        cx.notify();
    }

    fn entries(&self, cx: &App) -> Vec<StatusEntry> {
        self.model.read(cx).status.clone()
    }

    fn select(&mut self, path: String, cx: &mut Context<Self>) {
        if self.selected.as_ref() != Some(&path) {
            self.expanded.clear();
        }
        self.selected = Some(path);
        self.load_diff(cx);
        cx.notify();
    }

    fn load_diff(&mut self, cx: &mut Context<Self>) {
        let Some(entry) = self
            .selected
            .as_ref()
            .and_then(|p| self.model.read(cx).status.iter().find(|e| &e.path == p).cloned())
        else {
            self.diff.update(cx, |diff, cx| diff.show_message(None, tr!("No local changes"), cx));
            return;
        };
        let repo = self.model.read(cx).repo.clone();
        let title: SharedString = entry.path.clone().into();
        let splittable = can_commit_partially(&entry);
        // The diff shown follows the whitespace option; the hunks to commit
        // always come from the full patch, as the commit applies it.
        let shown = (repo.clone(), entry.clone());
        self.diff.update(cx, |diff, cx| {
            diff.load(title, move || shown.0.worktree_diff(&shown.1, Settings::read(|s| s.ignore_whitespace)), cx)
        });
        self.diff_task = Some(cx.spawn(async move |this, cx| {
            let path = entry.path.clone();
            let result = cx
                .background_executor()
                .spawn(async move { if splittable { repo.worktree_diff(&entry, false) } else { Ok(String::new()) } })
                .await;
            this.update(cx, |this, cx| {
                this.hunks = match (&result, splittable) {
                    (Ok(patch), true) => patch::parse(patch).hunks,
                    _ => Vec::new(),
                };
                // Forget exclusions of lines that no longer exist.
                let lines = changed_lines(&this.hunks);
                this.expanded.retain(|header| this.hunks.iter().any(|h| &h.header == header));
                if let Some(excluded) = this.excluded.get_mut(&path) {
                    excluded.retain(|line| lines.contains(line));
                    if excluded.is_empty() {
                        this.excluded.remove(&path);
                    }
                }
                cx.notify();
            })
            .ok();
        }));
    }

    fn toggle(&mut self, path: &str, cx: &mut Context<Self>) {
        self.excluded.remove(path);
        if !self.included.remove(path) {
            self.included.insert(path.to_string());
        }
        cx.notify();
    }

    fn is_partial(&self, path: &str) -> bool {
        self.included.contains(path) && self.excluded.get(path).is_some_and(|e| !e.is_empty())
    }

    fn is_line_included(&self, path: &str, line: &ChangedLine) -> bool {
        self.included.contains(path) && !self.excluded.get(path).is_some_and(|e| e.contains(line))
    }

    /// How many of the hunk's changed lines are included, out of how many.
    fn hunk_inclusion(&self, path: &str, hunk: &Hunk) -> (usize, usize) {
        let lines: Vec<ChangedLine> = hunk.changes().map(|ix| (hunk.header.clone(), ix)).collect();
        let included = lines.iter().filter(|line| self.is_line_included(path, line)).count();
        (included, lines.len())
    }

    /// Include or leave out some changed lines of the selected file.
    /// Including lines of an unchecked file includes just those lines.
    fn set_lines_included(&mut self, lines: Vec<ChangedLine>, include: bool, cx: &mut Context<Self>) {
        let Some(path) = self.selected.clone() else { return };
        let all = changed_lines(&self.hunks);
        if self.included.insert(path.clone()) {
            self.excluded.insert(path.clone(), all.clone());
        }
        let excluded = self.excluded.entry(path.clone()).or_default();
        for line in lines {
            if include {
                excluded.remove(&line);
            } else {
                excluded.insert(line);
            }
        }
        if *excluded == all {
            self.excluded.remove(&path);
            self.included.remove(&path);
        } else if excluded.is_empty() {
            self.excluded.remove(&path);
        }
        cx.notify();
    }

    /// A partly included hunk becomes fully included.
    fn toggle_hunk(&mut self, hunk: &Hunk, cx: &mut Context<Self>) {
        let Some(path) = self.selected.clone() else { return };
        let (included, total) = self.hunk_inclusion(&path, hunk);
        let lines = hunk.changes().map(|ix| (hunk.header.clone(), ix)).collect();
        self.set_lines_included(lines, included < total, cx);
    }

    fn toggle_line(&mut self, line: ChangedLine, cx: &mut Context<Self>) {
        let Some(path) = self.selected.clone() else { return };
        let include = !self.is_line_included(&path, &line);
        self.set_lines_included(vec![line], include, cx);
    }

    fn toggle_expanded(&mut self, header: String, cx: &mut Context<Self>) {
        if !self.expanded.remove(&header) {
            self.expanded.insert(header);
        }
        cx.notify();
    }

    fn toggle_group(&mut self, group: Group, include: bool, cx: &mut Context<Self>) {
        let lists = self.model.read(cx).changelists.clone();
        for entry in self.entries(cx).iter().filter(|e| Group::of(e, &lists) == group) {
            if include {
                self.included.insert(entry.path.clone());
            } else {
                self.included.remove(&entry.path);
            }
        }
        cx.notify();
    }

    fn set_amend(&mut self, amend: bool, window: &mut Window, cx: &mut Context<Self>) {
        self.amend = amend;
        if amend && self.message.read(cx).value().trim().is_empty() {
            let message = self.model.read(cx).repo.last_commit_message().unwrap_or_default();
            self.message.update(cx, |state, cx| state.set_value(message, window, cx));
        }
        cx.notify();
    }

    fn commit(&mut self, push: bool, window: &mut Window, cx: &mut Context<Self>) {
        let message = self.message.read(cx).value().trim().to_string();
        if message.is_empty() {
            self.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(tr!("Commit message is empty").into())));
            return;
        }
        let model = self.model.read(cx);
        let mut paths: Vec<String> = Vec::new();
        for entry in model.status.iter().filter(|e| self.included.contains(&e.path)) {
            paths.push(entry.path.clone());
            if let Some(orig) = &entry.orig_path {
                paths.push(orig.clone());
            }
        }
        let merging = model.state != RepoState::Clean;
        if paths.is_empty() && !self.amend && !merging {
            self.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(tr!("No changes selected to commit").into())));
            return;
        }
        let partial: Vec<(String, Vec<ChangedLine>)> = self
            .excluded
            .iter()
            .filter(|(path, excluded)| self.included.contains(*path) && !excluded.is_empty())
            .map(|(path, excluded)| (path.clone(), excluded.iter().cloned().collect()))
            .collect();
        let full: Vec<String> = paths
            .into_iter()
            .filter(|p| !partial.iter().any(|(path, _)| path == p))
            .collect();
        let amend = self.amend;
        let options = self.options.clone();
        // Like JetBrains, the author applies to one commit only.
        self.options.author = None;
        let label = if push { tr!("Commit and Push") } else { tr!("Commit") };
        Settings::add_message(message.clone());
        self.excluded.clear();
        self.model.update(cx, |model, cx| {
            model.run(
                label,
                move |repo| {
                    if partial.is_empty() {
                        repo.commit(&message, &full, amend, &options)?;
                    } else {
                        repo.commit_partial(&message, &full, &partial, amend, &options)?;
                    }
                    if push {
                        repo.push()?;
                    }
                    Ok(())
                },
                cx,
            )
        });
        self.message.update(cx, |state, cx| state.set_value("", window, cx));
        self.amend = false;
    }

    fn rollback(&mut self, paths: Vec<String>, window: &mut Window, cx: &mut Context<Self>) {
        let entries: Vec<StatusEntry> = self
            .entries(cx)
            .into_iter()
            .filter(|e| paths.contains(&e.path) && !e.is_untracked())
            .collect();
        if entries.is_empty() {
            return;
        }
        let model = self.model.clone();
        let count = entries.len();
        confirm(
            tr!("Rollback Changes"),
            tr!("Discard local changes in {} file(s)? This cannot be undone.", count),
            tr!("Rollback"),
            move |_, cx| {
                let entries = entries.clone();
                model.update(cx, |m, cx| m.run(tr!("Rollback"), move |repo| repo.rollback(&entries), cx));
            },
            window,
            cx,
        );
    }

    fn render_toolbar(&self, cx: &mut Context<Self>) -> AnyElement {
        h_flex()
            .flex_none()
            .h(px(36.))
            .px_2()
            .gap_1()
            .border_b_1()
            .border_color(cx.theme().border)
            .child(
                Button::new("changes-refresh")
                    .icon(IconName::RefreshCw)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Refresh"))
                    .on_click(cx.listener(|this, _, _, cx| this.model.update(cx, |m, cx| m.refresh(cx)))),
            )
            .child(
                Button::new("changes-rollback")
                    .icon(IconName::Undo2)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Rollback Selected Changes"))
                    .on_click(cx.listener(|this, _, window, cx| {
                        let paths: Vec<String> = this.included.iter().cloned().collect();
                        this.rollback(paths, window, cx)
                    })),
            )
            .child(
                Button::new("changes-stash")
                    .icon(IconName::Archive)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Stash Changes..."))
                    .on_click(cx.listener(|this, _, window, cx| {
                        let mut checked: Vec<String> = this.included.iter().cloned().collect();
                        checked.sort();
                        crate::stash_dialog::open(this.model.clone(), checked, window, cx)
                    })),
            )
            .child(
                Button::new("changes-apply-patch")
                    .icon(IconName::FileText)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Apply Patch..."))
                    .on_click(cx.listener(|this, _, _, cx| crate::actions::apply_patch(&this.model, cx))),
            )
            .child(
                Button::new("changes-shelve")
                    .icon(IconName::Inbox)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Shelve Selected Changes..."))
                    .on_click(cx.listener(|this, _, window, cx| {
                        let paths: Vec<String> = this.included.iter().cloned().collect();
                        if paths.is_empty() {
                            this.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(tr!("No changes selected to shelve").into())));
                        } else {
                            prompt_shelve(&this.model, paths, window, cx);
                        }
                    })),
            )
            .child(div().flex_1())
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("{} of {} selected", self.included.len(), self.change_count(cx))),
            )
        .into_any_element()
    }

    fn render_group(&self, group: Group, entries: &[StatusEntry], cx: &mut Context<Self>) -> AnyElement {
        let collapsed = self.collapsed.contains(&group);
        let all = !entries.is_empty() && entries.iter().all(|e| self.included.contains(&e.path));
        let title = group.title();
        let active = match &group {
            Group::List(name) => self.model.read(cx).changelists.active() == name,
            _ => false,
        };
        let mut rows = Vec::new();
        if !collapsed {
            for entry in entries {
                rows.push(self.render_entry(entry, cx));
            }
        }
        let paths: Vec<String> = entries.iter().map(|e| e.path.clone()).collect();
        let (toggle, check, menu_group) = (group.clone(), group.clone(), group.clone());
        let model = self.model.clone();
        let header = h_flex()
            .id(SharedString::from(format!("group-{title}")))
            .h(px(24.))
            .px_2()
            .gap_1()
            .text_sm()
            .font_semibold()
            .child(Icon::new(if collapsed { IconName::ChevronRight } else { IconName::ChevronDown }).xsmall())
            .child(
                Checkbox::new(SharedString::from(format!("group-check-{title}")))
                    .checked(all)
                    .on_click(cx.listener(move |this, checked: &bool, _, cx| {
                        this.toggle_group(check.clone(), *checked, cx)
                    })),
            )
            .child(title.clone())
            .when(active, |row| {
                row.child(
                    div()
                        .px_1()
                        .rounded(px(3.))
                        .text_xs()
                        .font_normal()
                        .bg(cx.theme().secondary)
                        .text_color(cx.theme().secondary_foreground)
                        .child(tr!("active")),
                )
            })
            .child(
                div()
                    .text_xs()
                    .font_normal()
                    .text_color(cx.theme().muted_foreground)
                    .child(crate::ui::file_count(entries.len())),
            )
            .on_click(cx.listener(move |this, _, _, cx| {
                if !this.collapsed.remove(&toggle) {
                    this.collapsed.insert(toggle.clone());
                }
                cx.notify();
            }));
        let header = match menu_group {
            Group::List(name) => header
                .context_menu(move |menu, _, _| changelist_menu(menu, &model, &name, active, paths.clone()))
                .into_any_element(),
            _ => header.into_any_element(),
        };
        v_flex().child(header).children(rows).into_any_element()
    }

    fn render_entry(&self, entry: &StatusEntry, cx: &mut Context<Self>) -> AnyElement {
        let path = entry.path.clone();
        let selected = self.selected.as_deref() == Some(path.as_str());
        let (dir, name) = match path.rsplit_once('/') {
            Some((dir, name)) => (dir.to_string(), name.to_string()),
            None => (String::new(), path.clone()),
        };
        let color = status_color(entry.display_status(), cx);
        let conflicted = entry.is_conflicted();
        let untracked = entry.is_untracked();
        let model = self.model.clone();
        let root = self.model.read(cx).repo.root().to_path_buf();
        let view = cx.entity().downgrade();

        h_flex()
            .id(SharedString::from(format!("change-{path}")))
            .h(px(24.))
            .pl(px(28.))
            .pr_2()
            .gap_1()
            .text_sm()
            .when(selected, |row| row.bg(selection_bg()))
            .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
            .child(
                Checkbox::new(SharedString::from(format!("include-{path}")))
                    .checked(self.included.contains(&path))
                    .on_click(cx.listener({
                        let path = path.clone();
                        move |this, _: &bool, _, cx| this.toggle(&path, cx)
                    })),
            )
            .child(Icon::new(IconName::File).xsmall().text_color(color))
            .child(div().flex_none().text_color(color).child(name))
            .child(
                div()
                    .min_w_0()
                    .truncate()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(dir),
            )
            .when(self.is_partial(&path), |row| {
                row.child(
                    div()
                        .flex_none()
                        .px_1()
                        .rounded(px(3.))
                        .text_xs()
                        .bg(cx.theme().secondary)
                        .text_color(cx.theme().secondary_foreground)
                        .child("partial"),
                )
            })
            .on_click(cx.listener({
                let path = path.clone();
                move |this, event: &ClickEvent, window, cx| {
                    this.select(path.clone(), cx);
                    if conflicted && event.click_count() >= 2 {
                        crate::merge_tool::open(this.model.clone(), path.clone(), window, cx);
                    }
                }
            }))
            .context_menu(move |mut menu, window, cx| {
                let full_path = root.join(&path);
                if !conflicted {
                    let lists = model.read(cx).changelists.clone();
                    if !untracked {
                        let (model, path) = (model.clone(), path.clone());
                        menu = menu.submenu(tr!("Move to Another Changelist"), window, cx, move |mut sub, _, _| {
                            let current = lists.list_of(&path).to_string();
                            for name in lists.names().iter().filter(|n| **n != current).cloned() {
                                let (model, path) = (model.clone(), path.clone());
                                sub = sub.item(PopupMenuItem::new(name.clone()).on_click(move |_, _, cx| {
                                    model.update(cx, |m, cx| m.change_lists(|l| l.move_to(&[path.clone()], &name), cx))
                                }));
                            }
                            let (model, path) = (model.clone(), path.clone());
                            sub.separator().item(PopupMenuItem::new(tr!("New Changelist...")).on_click(move |_, window, cx| {
                                let (model, path) = (model.clone(), path.clone());
                                prompt_text(tr!("New Changelist"), tr!("Create"), "", move |name, _, cx| {
                                    model.update(cx, |m, cx| m.change_lists(|l| l.move_to(&[path.clone()], &name), cx))
                                }, window, cx);
                            }))
                        });
                    }
                    let (shelve_model, shelve_path) = (model.clone(), path.clone());
                    menu = menu
                        .item(PopupMenuItem::new(tr!("Shelve...")).on_click(move |_, window, cx| {
                            prompt_shelve(&shelve_model, vec![shelve_path.clone()], window, cx)
                        }))
                        .separator();
                }
                if conflicted {
                    let merge_model = model.clone();
                    let merge_path = path.clone();
                    menu = menu
                        .item(PopupMenuItem::new(tr!("Merge...")).on_click(move |_, window, cx| {
                            crate::merge_tool::open(merge_model.clone(), merge_path.clone(), window, cx)
                        }))
                        .separator();
                    for (label, ours) in [(tr!("Accept Yours"), Some(true)), (tr!("Accept Theirs"), Some(false)), (tr!("Mark as Resolved"), None)] {
                        let model = model.clone();
                        let path = path.clone();
                        menu = menu.item(PopupMenuItem::new(label).on_click(move |_, _, cx| {
                            let path = path.clone();
                            model.update(cx, |m, cx| {
                                m.run(tr!("Resolve {}", path), move |repo| repo.resolve_conflict(&path, ours), cx)
                            });
                        }));
                    }
                } else if !untracked {
                    for (label, annotate) in [(tr!("Show History"), false), (tr!("Annotate"), true)] {
                        let model = model.clone();
                        let path = path.clone();
                        menu = menu.item(PopupMenuItem::new(label).on_click(move |_, window, cx| {
                            if annotate {
                                crate::history::annotate(model.clone(), "HEAD".into(), path.clone(), window, cx)
                            } else {
                                crate::history::show_history(model.clone(), path.clone(), window, cx)
                            }
                        }));
                    }
                    menu = menu.separator();
                    let view = view.clone();
                    let path = path.clone();
                    menu = menu.item(PopupMenuItem::new(tr!("Rollback...")).on_click(move |_, window, cx| {
                        let path = path.clone();
                        view.update(cx, |this, cx| this.rollback(vec![path], window, cx)).ok();
                    }));
                } else {
                    let model = model.clone();
                    let path = path.clone();
                    let ignore_model = model.clone();
                    let ignore_path = path.clone();
                    menu = menu.item(PopupMenuItem::new(tr!("Add to VCS")).on_click(move |_, _, cx| {
                        let path = path.clone();
                        model.update(cx, |m, cx| m.run(tr!("Add {}", path), move |repo| repo.git(&["add", "--", &path]), cx));
                    }));
                    menu = menu.item(PopupMenuItem::new(tr!("Add to .gitignore")).on_click(move |_, _, cx| {
                        let path = ignore_path.clone();
                        ignore_model.update(cx, |m, cx| {
                            m.run(tr!("Ignore {}", path), move |repo| repo.add_to_gitignore(&path), cx)
                        });
                    }));
                }
                let copy_path = full_path.clone();
                let reveal_path = full_path.clone();
                menu.separator()
                    .item(PopupMenuItem::new(tr!("Copy Path")).on_click(move |_, _, cx| {
                        cx.write_to_clipboard(ClipboardItem::new_string(copy_path.to_string_lossy().into_owned()))
                    }))
                    .item(PopupMenuItem::new(reveal_label()).on_click(move |_, _, cx| {
                        cx.reveal_path(&reveal_path)
                    }))
            })
        .into_any_element()
    }

    /// Checkboxes for the hunks of the selected file and, once expanded,
    /// for their lines, like the gutter checkboxes of JetBrains' commit diff.
    fn render_hunks(&self, cx: &mut Context<Self>) -> AnyElement {
        let Some(path) = self.selected.clone() else {
            return div().into_any_element();
        };
        let muted = cx.theme().muted_foreground;
        let mono = cx.theme().mono_font_family.clone();
        let mut rows: Vec<AnyElement> = Vec::new();
        for (ix, hunk) in self.hunks.iter().enumerate() {
            let line = hunk.first_changed_line();
            let context = hunk.context();
            let (included, total) = self.hunk_inclusion(&path, hunk);
            let expanded = self.expanded.contains(&hunk.header);
            let toggle = hunk.clone();
            let header = hunk.header.clone();
            rows.push(
                h_flex()
                    .id(("hunk", ix))
                    .h(px(24.))
                    .px_2()
                    .gap_2()
                    .text_sm()
                    .hover(|s| s.bg(cx.theme().list_hover))
                    .child(
                        Button::new(("hunk-expand", ix))
                            .icon(if expanded { IconName::ChevronDown } else { IconName::ChevronRight })
                            .ghost()
                            .xsmall()
                            .tooltip(if expanded { tr!("Hide lines") } else { tr!("Choose lines") })
                            .on_click(cx.listener(move |this, _, _, cx| {
                                cx.stop_propagation();
                                this.toggle_expanded(header.clone(), cx)
                            })),
                    )
                    .child(
                        Checkbox::new(("hunk-include", ix))
                            .checked(included > 0)
                            .on_click(cx.listener(move |this, _: &bool, _, cx| this.toggle_hunk(&toggle, cx))),
                    )
                    .child(div().flex_none().text_color(muted).child(tr!("Line {}", line)))
                    .child(div().flex_1().min_w_0().truncate().child(if context.is_empty() {
                        tr!("Change {}", ix + 1)
                    } else {
                        context.to_string()
                    }))
                    .when(included > 0 && included < total, |row| {
                        row.child(div().flex_none().text_xs().text_color(muted).child(format!("{included}/{total} lines")))
                    })
                    .child(div().flex_none().text_xs().text_color(cx.theme().success).child(format!("+{}", hunk.additions())))
                    .child(div().flex_none().text_xs().text_color(cx.theme().danger).child(format!("−{}", hunk.deletions())))
                    .on_click(cx.listener(move |this, _, _, cx| {
                        this.diff.update(cx, |diff, cx| diff.scroll_to_line(line, cx))
                    }))
                    .into_any_element(),
            );
            if !expanded {
                continue;
            }
            for line_ix in hunk.changes() {
                let text = &hunk.lines[line_ix];
                let key: ChangedLine = (hunk.header.clone(), line_ix);
                let checked = self.is_line_included(&path, &key);
                let added = text.starts_with('+');
                let new_line = hunk.new_line_at(line_ix);
                rows.push(
                    h_flex()
                        .id(SharedString::from(format!("hunk-line-{ix}-{line_ix}")))
                        .h(px(22.))
                        .pl(px(44.))
                        .pr_2()
                        .gap_2()
                        .text_xs()
                        .font_family(mono.clone())
                        .bg(if added { cx.theme().success.opacity(0.08) } else { cx.theme().danger.opacity(0.08) })
                        .hover(|s| s.bg(cx.theme().list_hover))
                        .child(
                            Checkbox::new(SharedString::from(format!("hunk-line-include-{ix}-{line_ix}")))
                                .checked(checked)
                                .on_click(cx.listener(move |this, _: &bool, _, cx| this.toggle_line(key.clone(), cx))),
                        )
                        .child(div().flex_none().w(px(36.)).text_color(muted).child(new_line.to_string()))
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .truncate()
                                .text_color(if added { cx.theme().success } else { cx.theme().danger })
                                .when(!checked, |text| text.line_through().opacity(0.6))
                                .child(text.clone()),
                        )
                        .on_click(cx.listener(move |this, _, _, cx| {
                            this.diff.update(cx, |diff, cx| diff.scroll_to_line(new_line, cx))
                        }))
                        .into_any_element(),
                );
            }
        }
        v_flex()
            .flex_none()
            .border_b_1()
            .border_color(cx.theme().border)
            .child(
                div()
                    .px_2()
                    .pt_1()
                    .text_xs()
                    .text_color(muted)
                    .child(tr!("Changes to commit in this file")),
            )
            .child(div().id("hunks").max_h(px(240.)).overflow_y_scroll().py_1().children(rows))
            .into_any_element()
    }

    /// Recent commit messages, like JetBrains' Commit Message History.
    fn render_message_history(&self) -> AnyElement {
        let input = self.message.clone();
        Button::new("message-history")
            .icon(IconName::Clock)
            .ghost()
            .xsmall()
            .tooltip(tr!("Commit Message History"))
            .dropdown_menu(move |mut menu, _, _| {
                let messages = Settings::get().messages;
                if messages.is_empty() {
                    return menu.item(PopupMenuItem::new(tr!("No recent messages")).disabled(true));
                }
                for message in messages {
                    let subject = message.lines().next().unwrap_or_default();
                    let mut label: String = subject.chars().take(70).collect();
                    if label.len() < subject.len() || message.contains('\n') {
                        label.push('…');
                    }
                    let input = input.clone();
                    menu = menu.item(PopupMenuItem::new(label).on_click(move |_, window, cx| {
                        input.update(cx, |state, cx| state.set_value(message.clone(), window, cx));
                    }));
                }
                menu.max_h(px(400.)).scrollable(true)
            })
            .into_any_element()
    }

    fn selected_conflict(&self, cx: &App) -> Option<StatusEntry> {
        let path = self.selected.as_ref()?;
        self.model.read(cx).conflicts().find(|e| &e.path == path).cloned()
    }

    /// What can be done with the selected conflicted file, above its diff.
    fn render_conflict_card(&self, entry: &StatusEntry, cx: &mut Context<Self>) -> AnyElement {
        let path = entry.path.clone();
        let (ours, theirs) = entry.conflict_sides().map(|(o, t)| (o.label(), t.label())).unwrap_or(("", ""));
        let (ours_name, theirs_name) = self
            .model
            .read(cx)
            .operation
            .as_ref()
            .map(|op| (op.ours.clone(), op.theirs.clone()))
            .unwrap_or_else(|| ("yours".into(), "theirs".into()));
        let resolve = |ours: bool, model: Entity<RepoModel>, path: String| {
            move |_: &ClickEvent, _: &mut Window, cx: &mut App| {
                let path = path.clone();
                let label = if ours { tr!("Accept yours for {}", path) } else { tr!("Accept theirs for {}", path) };
                model.update(cx, |m, cx| m.run(label, move |repo| repo.resolve_conflict(&path, Some(ours)), cx));
            }
        };
        let merge_model = self.model.clone();
        let merge_path = path.clone();
        h_flex()
            .flex_none()
            .p_3()
            .gap_3()
            .border_b_1()
            .border_color(cx.theme().border)
            .bg(cx.theme().warning.opacity(0.08))
            .child(Icon::new(IconName::TriangleAlert).text_color(cx.theme().warning))
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_0p5()
                    .child(div().text_sm().font_semibold().child(tr!("{} has merge conflicts", path)))
                    .child(
                        div()
                            .text_xs()
                            .truncate()
                            .text_color(cx.theme().muted_foreground)
                            .child(tr!("Yours ({}): {}  ·  Theirs ({}): {}", ours_name, ours, theirs_name, theirs)),
                    ),
            )
            .child(
                Button::new("card-yours")
                    .label(tr!("Accept Yours"))
                    .small()
                    .on_click(resolve(true, self.model.clone(), path.clone())),
            )
            .child(
                Button::new("card-theirs")
                    .label(tr!("Accept Theirs"))
                    .small()
                    .on_click(resolve(false, self.model.clone(), path.clone())),
            )
            .when(entry.can_merge_text(), |row| {
                row.child(Button::new("card-merge").label(tr!("Merge…")).primary().small().on_click(
                    move |_, window, cx| crate::merge_tool::open(merge_model.clone(), merge_path.clone(), window, cx),
                ))
            })
            .into_any_element()
    }

    /// What the next commit does beyond the defaults, e.g. "Author: Ada · Sign-off".
    fn options_summary(&self) -> Option<String> {
        let o = &self.options;
        let mut parts: Vec<String> = Vec::new();
        if let Some(author) = &o.author {
            parts.push(tr!("Author: {}", author.split('<').next().unwrap_or(author).trim()));
        }
        if o.sign_off {
            parts.push(tr!("Sign-off").into());
        }
        match o.sign {
            Some(true) => parts.push(tr!("Signed").into()),
            Some(false) => parts.push(tr!("Not signed").into()),
            None => {}
        }
        if o.skip_hooks {
            parts.push(tr!("Hooks skipped").into());
        }
        (!parts.is_empty()).then(|| parts.join(" · "))
    }

    fn render_commit_box(&self, cx: &mut Context<Self>) -> AnyElement {
        v_flex()
            .flex_none()
            .p_2()
            .gap_2()
            .border_t_1()
            .border_color(cx.theme().border)
            .child(Textarea::new(&self.message))
            .children(message_problems(&self.message.read(cx).value()).into_iter().map(|problem| {
                div().text_xs().text_color(cx.theme().muted_foreground).child(problem)
            }))
            .when_some(self.options_summary(), |col, summary| {
                col.child(div().text_xs().text_color(cx.theme().warning).truncate().child(summary))
            })
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Checkbox::new("amend")
                            .label(tr!("Amend"))
                            .checked(self.amend)
                            .on_click(cx.listener(|this, checked: &bool, window, cx| this.set_amend(*checked, window, cx))),
                    )
                    .child(self.render_message_history())
                    .child(
                        Button::new("commit-options")
                            .icon(IconName::Settings2)
                            .ghost()
                            .xsmall()
                            .tooltip(tr!("Commit Options…"))
                            .on_click(cx.listener(|this, _, window, cx| {
                                let view = cx.entity().downgrade();
                                crate::commit_options::open(
                                    this.model.clone(),
                                    this.options.clone(),
                                    move |options, cx| {
                                        view.update(cx, |this, cx| {
                                            this.options = options;
                                            cx.notify();
                                        })
                                        .ok();
                                    },
                                    window,
                                    cx,
                                );
                            })),
                    )
                    .child(div().flex_1())
                    .child(
                        Button::new("commit-push")
                            .label(tr!("Commit and Push"))
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.commit(true, window, cx))),
                    )
                    .child(
                        Button::new("commit")
                            .label(tr!("Commit"))
                            .primary()
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.commit(false, window, cx))),
                    ),
            )
        .into_any_element()
    }
}

impl Render for ChangesView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        if let Some(message) = self.pending_message.take() {
            self.message.update(cx, |state, cx| state.set_value(message, window, cx));
        }
        let entries = self.entries(cx);
        let mut groups: Vec<(Group, Vec<StatusEntry>)> = Vec::new();
        let lists = self.model.read(cx).changelists.clone();
        let mut order = vec![Group::Conflicts];
        order.extend(lists.names().iter().map(|name| Group::List(name.clone())));
        order.push(Group::Unversioned);
        // Every changelist shows, even empty, once there is more than one.
        let show_empty_lists = lists.names().len() > 1;
        for group in order {
            let members: Vec<StatusEntry> = entries.iter().filter(|e| Group::of(e, &lists) == group).cloned().collect();
            if !members.is_empty() || (show_empty_lists && matches!(group, Group::List(_))) {
                groups.push((group, members));
            }
        }
        let mut rendered = Vec::new();
        for (group, members) in &groups {
            rendered.push(self.render_group(group.clone(), members, cx));
        }
        let list = if groups.is_empty() {
            div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_sm()
                .text_color(cx.theme().muted_foreground)
                .child(tr!("No local changes"))
                .into_any_element()
        } else {
            div()
                .id("changes-list")
                .flex_1()
                .min_h_0()
                .overflow_y_scroll()
                .py_1()
                .children(rendered)
                .into_any_element()
        };

        h_resizable("changes")
            .child(
                resizable_panel().size(px(420.)).size_range(px(280.)..px(900.)).child(
                    v_flex()
                        .size_full()
                        .bg(cx.theme().sidebar)
                        .child(panel_header(tr!("Local Changes"), cx))
                        .child(self.render_toolbar(cx))
                        .child(list)
                        .child(self.render_commit_box(cx)),
                ),
            )
            .child(
                resizable_panel().child(
                    v_flex()
                        .size_full()
                        .when_some(self.selected_conflict(cx), |col, entry| col.child(self.render_conflict_card(&entry, cx)))
                        .when(!self.hunks.is_empty(), |col| col.child(self.render_hunks(cx)))
                        .child(div().flex_1().min_h_0().child(crate::ui::cached(self.diff.clone()))),
                ),
            )
    }
}

/// The file manager's name for showing a file in its folder.
pub fn reveal_label() -> &'static str {
    if cfg!(target_os = "macos") {
        tr!("Reveal in Finder")
    } else if cfg!(windows) {
        tr!("Show in Explorer")
    } else {
        tr!("Open Containing Folder")
    }
}

/// Every changed line of `hunks`.
fn changed_lines(hunks: &[Hunk]) -> HashSet<ChangedLine> {
    hunks
        .iter()
        .flat_map(|hunk| hunk.changes().map(|ix| (hunk.header.clone(), ix)))
        .collect()
}

/// Only plain modifications of tracked files can be split into hunks.
fn can_commit_partially(entry: &StatusEntry) -> bool {
    entry.display_status() == 'M' && entry.orig_path.is_none()
}

/// Ask for a name, then shelve `paths`.
fn prompt_shelve(model: &Entity<RepoModel>, paths: Vec<String>, window: &mut Window, cx: &mut App) {
    let default = {
        let lists = &model.read(cx).changelists;
        paths.first().map(|p| lists.list_of(p).to_string()).unwrap_or_default()
    };
    let model = model.clone();
    prompt_text(
        if paths.len() == 1 { tr!("Shelve 1 File").to_string() } else { tr!("Shelve {} Files", paths.len()) },
        tr!("Shelve"),
        default,
        move |name, _, cx| model.update(cx, |m, cx| m.shelve(name, paths.clone(), cx)),
        window,
        cx,
    );
}

/// The menu of a changelist's header.
fn changelist_menu(
    mut menu: gpui_kit::component::menu::PopupMenu,
    model: &Entity<RepoModel>,
    name: &str,
    active: bool,
    paths: Vec<String>,
) -> gpui_kit::component::menu::PopupMenu {
    let item = |label: &str, f: Box<dyn Fn(&Entity<RepoModel>, &mut Window, &mut App)>| {
        let model = model.clone();
        PopupMenuItem::new(label.to_string()).on_click(move |_, window, cx| f(&model, window, cx))
    };
    if !active {
        let name = name.to_string();
        menu = menu.item(item(
            tr!("Set as Active Changelist"),
            Box::new(move |model, _, cx| model.update(cx, |m, cx| m.change_lists(|l| l.set_active(&name), cx))),
        ));
    }
    menu = menu.item(item(
        tr!("New Changelist..."),
        Box::new(|model, window, cx| {
            let model = model.clone();
            prompt_text(tr!("New Changelist"), tr!("Create"), "", move |new, _, cx| {
                model.update(cx, |m, cx| m.change_lists(|l| l.create(&new), cx))
            }, window, cx);
        }),
    ));
    let old = name.to_string();
    menu = menu.item(item(
        tr!("Rename Changelist..."),
        Box::new(move |model, window, cx| {
            let (model, old) = (model.clone(), old.clone());
            prompt_text(tr!("Rename Changelist"), tr!("Rename"), old.clone(), move |new, _, cx| {
                model.update(cx, |m, cx| m.change_lists(|l| l.rename(&old, &new), cx))
            }, window, cx);
        }),
    ));
    let gone = name.to_string();
    menu = menu.item(item(
        tr!("Delete Changelist"),
        Box::new(move |model, _, cx| model.update(cx, |m, cx| m.change_lists(|l| l.delete(&gone), cx))),
    ));
    if !paths.is_empty() {
        menu = menu.separator().item(item(
            tr!("Shelve Changes..."),
            Box::new(move |model, window, cx| prompt_shelve(model, paths.clone(), window, cx)),
        ));
    }
    menu
}

/// Where a commit message strays from the usual form: a subject of at most
/// 72 characters, then a blank line before the body. Shown, not enforced.
fn message_problems(message: &str) -> Vec<String> {
    const SUBJECT_LIMIT: usize = 72;
    let mut lines = message.lines();
    let mut problems = Vec::new();
    let subject_length = lines.next().map(|subject| subject.trim_end().chars().count()).unwrap_or(0);
    if subject_length > SUBJECT_LIMIT {
        problems.push(tr!("The subject is {} characters; {} or fewer reads best in logs", subject_length, SUBJECT_LIMIT));
    }
    if lines.next().is_some_and(|second| !second.trim().is_empty()) {
        problems.push(tr!("Leave a blank line between the subject and the body").into());
    }
    problems
}

#[cfg(test)]
mod tests {
    use super::message_problems;

    #[test]
    fn checks_commit_messages() {
        assert!(message_problems("").is_empty());
        assert!(message_problems("Fix the thing\n\nBecause it broke.").is_empty());
        assert_eq!(message_problems(&"x".repeat(73)).len(), 1);
        assert!(message_problems(&"字".repeat(72)).is_empty());
        assert_eq!(message_problems("Subject\nBody right away").len(), 1);
        assert_eq!(message_problems(&format!("{}\nbody", "y".repeat(80))).len(), 2);
    }
}
