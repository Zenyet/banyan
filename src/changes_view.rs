//! The "Commit" tab: local changes with checkboxes, commit message and diff preview.

use std::collections::{BTreeSet, HashSet};

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex, h_resizable,
    input::{Textarea, TextareaState},
    menu::{ContextMenuExt as _, PopupMenuItem},
    resizable_panel, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::diff_pane::DiffPane;
use crate::git::{RepoState, StatusEntry};
use crate::log_view::panel_header;
use crate::model::{RepoEvent, RepoModel};
use crate::ui::{selection_bg, confirm, prompt_text, status_color};

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum Group {
    Conflicts,
    Changes,
    Unversioned,
}

impl Group {
    fn of(entry: &StatusEntry) -> Self {
        if entry.is_conflicted() {
            Group::Conflicts
        } else if entry.is_untracked() {
            Group::Unversioned
        } else {
            Group::Changes
        }
    }

    fn title(self) -> &'static str {
        match self {
            Group::Conflicts => "Merge Conflicts",
            Group::Changes => "Changes",
            Group::Unversioned => "Unversioned Files",
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
    prefilled_merge_message: bool,
    collapsed: BTreeSet<Group>,
    selected: Option<String>,
    message: Entity<TextareaState>,
    amend: bool,
    diff: Entity<DiffPane>,
    diff_task: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

impl ChangesView {
    pub fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let message = cx.new(|cx| {
            TextareaState::new(window, cx)
                .placeholder("Commit Message")
                .auto_grow(4, 12)
        });
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.clear(cx));
        let subscriptions = vec![cx.subscribe(&model, |this, _, event, cx| {
            if let RepoEvent::Changed = event {
                this.sync_status(cx);
            }
        })];
        let mut this = Self {
            model,
            included: HashSet::new(),
            known: HashSet::new(),
            conflicted: HashSet::new(),
            pending_message: None,
            prefilled_merge_message: false,
            collapsed: BTreeSet::new(),
            selected: None,
            message,
            amend: false,
            diff,
            diff_task: None,
            _subscriptions: subscriptions,
        };
        this.sync_status(cx);
        this
    }

    pub fn change_count(&self, cx: &App) -> usize {
        self.model.read(cx).status.len()
    }

    /// New tracked changes are included by default, unversioned files are not.
    fn sync_status(&mut self, cx: &mut Context<Self>) {
        let status = self.model.read(cx).status.clone();
        let paths: HashSet<String> = status.iter().map(|e| e.path.clone()).collect();
        for entry in &status {
            let is_new = self.known.insert(entry.path.clone());
            if entry.is_conflicted() {
                self.conflicted.insert(entry.path.clone());
            } else if self.conflicted.remove(&entry.path) || (is_new && !entry.is_untracked()) {
                // Freshly resolved conflicts are ready to commit.
                self.included.insert(entry.path.clone());
            }
        }
        self.known.retain(|p| paths.contains(p));
        self.conflicted.retain(|p| paths.contains(p));

        let model = self.model.read(cx);
        if model.state == RepoState::Clean {
            self.prefilled_merge_message = false;
        } else if !self.prefilled_merge_message && self.message.read(cx).value().trim().is_empty() {
            self.prefilled_merge_message = true;
            self.pending_message = model.repo.merge_message();
        }
        self.included.retain(|p| paths.contains(p));
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
            self.diff.update(cx, |diff, cx| diff.show_message(None, "No local changes", cx));
            return;
        };
        let repo = self.model.read(cx).repo.clone();
        let title: SharedString = entry.path.clone().into();
        self.diff_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.worktree_diff(&entry) })
                .await;
            this.update(cx, |this, cx| {
                this.diff.update(cx, |diff, cx| match result {
                    Ok(patch) => diff.show_patch(title, &patch, cx),
                    Err(err) => diff.show_message(Some(title), format!("{err:#}"), cx),
                })
            })
            .ok();
        }));
    }

    fn toggle(&mut self, path: &str, cx: &mut Context<Self>) {
        if !self.included.remove(path) {
            self.included.insert(path.to_string());
        }
        cx.notify();
    }

    fn toggle_group(&mut self, group: Group, include: bool, cx: &mut Context<Self>) {
        for entry in self.entries(cx).iter().filter(|e| Group::of(e) == group) {
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
            self.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed("Commit message is empty".into())));
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
            self.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed("No changes selected to commit".into())));
            return;
        }
        let amend = self.amend;
        let label = if push { "Commit and Push" } else { "Commit" };
        self.model.update(cx, |model, cx| {
            model.run(
                label,
                move |repo| {
                    repo.commit(&message, &paths, amend)?;
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
            "Rollback Changes",
            format!("Discard local changes in {count} file(s)? This cannot be undone."),
            "Rollback",
            move |_, cx| {
                let entries = entries.clone();
                model.update(cx, |m, cx| m.run("Rollback", move |repo| repo.rollback(&entries), cx));
            },
            window,
            cx,
        );
    }

    fn render_toolbar(&self, cx: &mut Context<Self>) -> AnyElement {
        let model = self.model.clone();
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
                    .tooltip("Refresh")
                    .on_click(cx.listener(|this, _, _, cx| this.model.update(cx, |m, cx| m.refresh(cx)))),
            )
            .child(
                Button::new("changes-rollback")
                    .icon(IconName::Undo2)
                    .ghost()
                    .xsmall()
                    .tooltip("Rollback Selected Changes")
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
                    .tooltip("Stash Changes...")
                    .on_click(move |_, window, cx| {
                        let model = model.clone();
                        prompt_text(
                            "Stash Changes",
                            "Create Stash",
                            "WIP",
                            move |message, _, cx| {
                                model.update(cx, |m, cx| m.run("Stash", move |repo| repo.stash(&message), cx));
                            },
                            window,
                            cx,
                        );
                    }),
            )
            .child(div().flex_1())
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(format!("{} of {} selected", self.included.len(), self.change_count(cx))),
            )
        .into_any_element()
    }

    fn render_group(&self, group: Group, entries: &[StatusEntry], cx: &mut Context<Self>) -> AnyElement {
        let collapsed = self.collapsed.contains(&group);
        let all = entries.iter().all(|e| self.included.contains(&e.path));
        let mut rows = Vec::new();
        if !collapsed {
            for entry in entries {
                rows.push(self.render_entry(entry, cx));
            }
        }
        v_flex()
            .child(
                h_flex()
                    .id(group.title())
                    .h(px(24.))
                    .px_2()
                    .gap_1()
                    .text_sm()
                    .font_semibold()
                    .child(
                        Icon::new(if collapsed { IconName::ChevronRight } else { IconName::ChevronDown })
                            .xsmall(),
                    )
                    .child(
                        Checkbox::new(SharedString::from(format!("group-{}", group.title())))
                            .checked(all)
                            .on_click(cx.listener(move |this, checked: &bool, _, cx| {
                                this.toggle_group(group, *checked, cx)
                            })),
                    )
                    .child(group.title())
                    .child(
                        div()
                            .text_xs()
                            .font_normal()
                            .text_color(cx.theme().muted_foreground)
                            .child(format!("{} files", entries.len())),
                    )
                    .on_click(cx.listener(move |this, _, _, cx| {
                        if !this.collapsed.remove(&group) {
                            this.collapsed.insert(group);
                        }
                        cx.notify();
                    })),
            )
            .children(rows)
        .into_any_element()
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
            .on_click(cx.listener({
                let path = path.clone();
                move |this, event: &ClickEvent, window, cx| {
                    this.select(path.clone(), cx);
                    if conflicted && event.click_count() >= 2 {
                        crate::merge_tool::open(this.model.clone(), path.clone(), window, cx);
                    }
                }
            }))
            .context_menu(move |mut menu, _, _| {
                if conflicted {
                    let merge_model = model.clone();
                    let merge_path = path.clone();
                    menu = menu
                        .item(PopupMenuItem::new("Merge...").on_click(move |_, window, cx| {
                            crate::merge_tool::open(merge_model.clone(), merge_path.clone(), window, cx)
                        }))
                        .separator();
                    for (label, ours) in [("Accept Yours", Some(true)), ("Accept Theirs", Some(false)), ("Mark as Resolved", None)] {
                        let model = model.clone();
                        let path = path.clone();
                        menu = menu.item(PopupMenuItem::new(label).on_click(move |_, _, cx| {
                            let path = path.clone();
                            model.update(cx, |m, cx| {
                                m.run(format!("Resolve {path}"), move |repo| repo.resolve_conflict(&path, ours), cx)
                            });
                        }));
                    }
                } else if !untracked {
                    for (label, annotate) in [("Show History", false), ("Annotate", true)] {
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
                    menu = menu.item(PopupMenuItem::new("Rollback...").on_click(move |_, window, cx| {
                        let path = path.clone();
                        view.update(cx, |this, cx| this.rollback(vec![path], window, cx)).ok();
                    }));
                } else {
                    let model = model.clone();
                    let path = path.clone();
                    menu = menu.item(PopupMenuItem::new("Add to VCS").on_click(move |_, _, cx| {
                        let path = path.clone();
                        model.update(cx, |m, cx| m.run(format!("Add {path}"), move |repo| repo.git(&["add", "--", &path]), cx));
                    }));
                }
                menu
            })
        .into_any_element()
    }

    fn render_commit_box(&self, cx: &mut Context<Self>) -> AnyElement {
        let state = self.model.read(cx).state;
        v_flex()
            .flex_none()
            .p_2()
            .gap_2()
            .border_t_1()
            .border_color(cx.theme().border)
            .when(state != RepoState::Clean, |col| {
                let label = match state {
                    RepoState::Merging => "Merging",
                    RepoState::Rebasing => "Rebasing",
                    RepoState::CherryPicking => "Cherry-picking",
                    RepoState::Reverting => "Reverting",
                    RepoState::Clean => "",
                };
                col.child(
                    h_flex()
                        .gap_2()
                        .text_sm()
                        .child(Icon::new(IconName::GitMerge).small().text_color(rgb(0xe0a33d)))
                        .child(div().flex_1().child(format!("{label} in progress")))
                        .child(
                            Button::new("op-continue")
                                .label("Continue")
                                .xsmall()
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.model.update(cx, |m, cx| m.run("Continue", move |repo| repo.continue_op(state), cx))
                                })),
                        )
                        .child(
                            Button::new("op-abort")
                                .label("Abort")
                                .xsmall()
                                .danger()
                                .on_click(cx.listener(move |this, _, _, cx| {
                                    this.model.update(cx, |m, cx| m.run("Abort", move |repo| repo.abort(state), cx))
                                })),
                        ),
                )
            })
            .child(Textarea::new(&self.message))
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Checkbox::new("amend")
                            .label("Amend")
                            .checked(self.amend)
                            .on_click(cx.listener(|this, checked: &bool, window, cx| this.set_amend(*checked, window, cx))),
                    )
                    .child(div().flex_1())
                    .child(
                        Button::new("commit-push")
                            .label("Commit and Push")
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.commit(true, window, cx))),
                    )
                    .child(
                        Button::new("commit")
                            .label("Commit")
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
        for group in [Group::Conflicts, Group::Changes, Group::Unversioned] {
            let members: Vec<StatusEntry> = entries.iter().filter(|e| Group::of(e) == group).cloned().collect();
            if !members.is_empty() {
                groups.push((group, members));
            }
        }
        let mut rendered = Vec::new();
        for (group, members) in &groups {
            rendered.push(self.render_group(*group, members, cx));
        }
        let list = if groups.is_empty() {
            div()
                .flex_1()
                .flex()
                .items_center()
                .justify_center()
                .text_sm()
                .text_color(cx.theme().muted_foreground)
                .child("No local changes")
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
                        .child(panel_header("Local Changes", cx))
                        .child(self.render_toolbar(cx))
                        .child(list)
                        .child(self.render_commit_box(cx)),
                ),
            )
            .child(resizable_panel().child(self.diff.clone()))
    }
}
