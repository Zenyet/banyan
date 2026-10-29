//! "Manage Worktrees…": the repository's working trees, opening one in a
//! window of its own, removing one, and adding a working tree for a branch.

use std::path::{Path, PathBuf};

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{Input, InputEvent, InputState},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::{Repo, Worktree};
use crate::i18n::tr;
use crate::model::{RepoEvent, RepoModel};
use crate::ui::confirm;

/// Open the dialog, with the form filled in for `branch` when given.
pub fn open(model: Entity<RepoModel>, branch: Option<String>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| WorktreesDialog::new(model, branch, window, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Worktrees")).w(px(760.)).child(view.clone()));
}

struct WorktreesDialog {
    model: Entity<RepoModel>,
    trees: Vec<Worktree>,
    branch: Entity<InputState>,
    path: Entity<InputState>,
    /// The location was typed rather than derived from the branch.
    path_edited: bool,
    load_task: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

/// `~/x/repo` and branch `feature/login` → `~/x/repo-feature-login`.
fn suggested_path(root: &Path, branch: &str) -> PathBuf {
    let name = root.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let branch: String = branch.chars().map(|c| if c.is_alphanumeric() || c == '-' || c == '_' { c } else { '-' }).collect();
    root.with_file_name(format!("{name}-{branch}"))
}

impl WorktreesDialog {
    fn new(model: Entity<RepoModel>, branch: Option<String>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let root = model.read(cx).repo.root().to_path_buf();
        let initial = branch.unwrap_or_default();
        let branch = cx.new(|cx| InputState::new(window, cx).placeholder(tr!("Branch")).default_value(initial.clone()));
        let path = cx.new(|cx| {
            let state = InputState::new(window, cx).placeholder(tr!("Location"));
            if initial.is_empty() { state } else { state.default_value(suggested_path(&root, &initial).display().to_string()) }
        });
        let subscriptions = vec![
            cx.subscribe(&model, |this, _, event, cx| {
                if let RepoEvent::Changed = event {
                    this.load(cx);
                }
            }),
            // The location follows the branch until it is typed in.
            cx.subscribe_in(&branch, window, move |this, input, event, window, cx| {
                if let InputEvent::Change = event
                    && !this.path_edited
                {
                    let branch = input.read(cx).value().trim().to_string();
                    let path = if branch.is_empty() { String::new() } else { suggested_path(&root, &branch).display().to_string() };
                    this.path.update(cx, |input, cx| input.set_value(path, window, cx));
                }
            }),
            cx.subscribe(&path, |this, _, event, _| {
                if let InputEvent::Change = event {
                    this.path_edited = true;
                }
            }),
        ];
        let mut this = Self { model, trees: Vec::new(), branch, path, path_edited: false, load_task: None, _subscriptions: subscriptions };
        this.load(cx);
        this
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        let repo = self.model.read(cx).repo.clone();
        self.load_task = Some(cx.spawn(async move |this, cx| {
            let trees = cx.background_executor().spawn(async move { repo.worktrees().unwrap_or_default() }).await;
            this.update(cx, |this, cx| {
                this.trees = trees;
                cx.notify();
            })
            .ok();
        }));
    }

    fn add(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let branch = self.branch.read(cx).value().trim().to_string();
        let path = PathBuf::from(self.path.read(cx).value().trim().to_string());
        if branch.is_empty() || path.as_os_str().is_empty() {
            self.model
                .update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(tr!("Enter a branch and a location").into())));
            return;
        }
        self.branch.update(cx, |input, cx| input.set_value("", window, cx));
        self.path_edited = false;
        self.model.update(cx, |m, cx| {
            m.run_then(
                tr!("Add worktree {}", branch),
                move |repo| repo.add_worktree(&path, &branch),
                |_, repo, cx| crate::open_repo_window(repo, cx),
                |err| RepoEvent::OpFailed(format!("{}\n{err:#}", tr!("Adding the worktree failed")).into()),
                cx,
            )
        });
    }

    fn remove(&self, tree: &Worktree, window: &mut Window, cx: &mut Context<Self>) {
        let path = tree.path.clone();
        let dirty = Repo::worktree_is_dirty(&path);
        let model = self.model.clone();
        let shown = crate::welcome::tilde(&path);
        let message = if dirty {
            tr!("{} has local changes. Remove it anyway? Its changes are lost.", shown)
        } else {
            tr!("Remove the worktree at {}? Its branch stays.", shown)
        };
        confirm(
            tr!("Remove Worktree"),
            message,
            tr!("Remove"),
            move |_, cx| {
                let path = path.clone();
                model.update(cx, |m, cx| {
                    m.run(tr!("Remove worktree"), move |repo| repo.remove_worktree(&path, dirty), cx)
                });
            },
            window,
            cx,
        );
    }

    fn render_tree(&self, ix: usize, tree: &Worktree, cx: &mut Context<Self>) -> AnyElement {
        let badge = |text: &'static str, cx: &App| {
            div()
                .px_1()
                .rounded(px(3.))
                .text_xs()
                .bg(cx.theme().secondary)
                .text_color(cx.theme().secondary_foreground)
                .child(text)
        };
        let head = match &tree.branch {
            Some(branch) => branch.clone(),
            None => tr!("detached at {}", &tree.head[..tree.head.len().min(8)]),
        };
        let open_path = tree.path.clone();
        let removable = !tree.is_main && !tree.is_current;
        let row_tree = tree.clone();
        h_flex()
            .id(("worktree", ix))
            .px_2()
            .py_1p5()
            .gap_2()
            .rounded(cx.theme().radius)
            .hover(|s| s.bg(cx.theme().list_hover))
            .child(Icon::new(IconName::GitBranch).small().text_color(cx.theme().muted_foreground))
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(
                        h_flex()
                            .gap_1()
                            .child(div().text_sm().font_semibold().truncate().child(head))
                            .when(tree.is_current, |row| row.child(badge(tr!("current"), cx)))
                            .when(tree.is_main, |row| row.child(badge(tr!("main"), cx)))
                            .when(tree.locked, |row| row.child(badge(tr!("locked"), cx)))
                            .when(tree.prunable, |row| row.child(badge(tr!("folder missing"), cx))),
                    )
                    .child(
                        div()
                            .text_xs()
                            .truncate()
                            .text_color(cx.theme().muted_foreground)
                            .child(crate::welcome::tilde(&tree.path)),
                    ),
            )
            .when(!tree.is_current && !tree.prunable, |row| {
                row.child(
                    Button::new(("open-worktree", ix))
                        .label(tr!("Open"))
                        .small()
                        .on_click(move |_, _, cx| crate::open_path(&open_path, None, cx)),
                )
            })
            .when(removable && !tree.prunable, |row| {
                row.child(
                    Button::new(("remove-worktree", ix))
                        .icon(IconName::Close)
                        .ghost()
                        .small()
                        .tooltip(tr!("Remove Worktree"))
                        .on_click(cx.listener(move |this, _, window, cx| this.remove(&row_tree, window, cx))),
                )
            })
            .into_any_element()
    }
}

impl Render for WorktreesDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let trees = self.trees.clone();
        let rows: Vec<AnyElement> = trees.iter().enumerate().map(|(ix, tree)| self.render_tree(ix, tree, cx)).collect();
        let prunable = trees.iter().any(|t| t.prunable);
        let model = self.model.clone();
        v_flex()
            .gap_3()
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("Each worktree is a folder with its own branch checked out, sharing this repository's history.")),
            )
            .child(
                v_flex()
                    .id("worktrees")
                    .max_h(px(300.))
                    .overflow_y_scroll()
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .p_1()
                    .children(rows),
            )
            .when(prunable, |col| {
                col.child(
                    h_flex()
                        .gap_2()
                        .text_xs()
                        .text_color(cx.theme().muted_foreground)
                        .child(div().flex_1().child(tr!("Some worktree folders are gone.")))
                        .child(Button::new("prune-worktrees").label(tr!("Forget Missing")).small().on_click(
                            move |_, _, cx| {
                                model.update(cx, |m, cx| m.run(tr!("Prune worktrees"), |repo| repo.prune_worktrees(), cx))
                            },
                        )),
                )
            })
            .child(div().text_sm().font_semibold().child(tr!("New Worktree")))
            .child(
                h_flex()
                    .gap_2()
                    .child(div().w(px(220.)).child(Input::new(&self.branch)))
                    .child(div().flex_1().child(Input::new(&self.path)))
                    .child(
                        Button::new("add-worktree")
                            .label(tr!("Add"))
                            .primary()
                            .on_click(cx.listener(|this, _, window, cx| this.add(window, cx))),
                    ),
            )
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("An existing branch is checked out there; a new name creates a branch from the current commit.")),
            )
    }
}
