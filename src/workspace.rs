//! Main window: toolbar with the branch popup and VCS operations, and the
//! Log / Commit tabs.

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _, Theme, ThemeMode, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    menu::{DropdownMenu as _, PopupMenuItem},
    notification::Notification,
    tab::{Tab, TabBar},
    v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::actions::{branch_menu, new_branch};
use crate::changes_view::ChangesView;
use crate::console::ConsoleView;
use crate::git::{RefKind, RepoState};
use crate::log_view::LogView;
use crate::model::{RepoEvent, RepoModel};
use crate::settings::Settings;
use crate::ui::{confirm, prompt_text};

actions!(
    workspace,
    [CommitChanges, PushCommits, UpdateProject, ShowLog, ShowCommitTab, ShowConsole, RefreshRepository]
);

/// JetBrains' macOS keymap where it has an equivalent.
pub fn init(cx: &mut App) {
    cx.bind_keys([
        KeyBinding::new("cmd-k", CommitChanges, Some("Workspace")),
        KeyBinding::new("cmd-shift-k", PushCommits, Some("Workspace")),
        KeyBinding::new("cmd-t", UpdateProject, Some("Workspace")),
        KeyBinding::new("cmd-9", ShowLog, Some("Workspace")),
        KeyBinding::new("cmd-0", ShowCommitTab, Some("Workspace")),
        KeyBinding::new("cmd-r", RefreshRepository, Some("Workspace")),
    ]);
}

pub struct Workspace {
    model: Entity<RepoModel>,
    focus: FocusHandle,
    log: Entity<LogView>,
    changes: Entity<ChangesView>,
    console: Entity<ConsoleView>,
    tab: usize,
    _subscriptions: Vec<Subscription>,
}

impl Workspace {
    pub fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let log = cx.new(|cx| LogView::new(model.clone(), window, cx));
        let changes = cx.new(|cx| ChangesView::new(model.clone(), window, cx));
        let console = cx.new(|cx| ConsoleView::new(model.clone(), cx));
        let subscriptions = vec![
            cx.subscribe_in(&model, window, |_, _, event, window, cx| match event {
                RepoEvent::OpFailed(message) => {
                    window.push_notification(Notification::error(message.clone()), cx)
                }
                RepoEvent::OpSucceeded(label) => {
                    window.push_notification(Notification::success(label.clone()), cx)
                }
                RepoEvent::Changed => cx.notify(),
            }),
            cx.observe(&model, |_, _, cx| cx.notify()),
            // Pick up changes made outside the app, like JetBrains does on frame activation.
            cx.observe_window_activation(window, |this, window, cx| {
                if window.is_window_active() {
                    this.model.update(cx, |model, cx| model.refresh(cx));
                }
            }),
        ];
        let name = model.read(cx).repo.name();
        window.set_window_title(&format!("{name} – GitBrains"));
        let focus = cx.focus_handle();
        focus.focus(window, cx);
        Self {
            model,
            focus,
            log,
            changes,
            console,
            tab: 0,
            _subscriptions: subscriptions,
        }
    }

    fn run(
        &self,
        label: &'static str,
        op: impl FnOnce(&crate::git::Repo) -> anyhow::Result<String> + Send + 'static,
        cx: &mut Context<Self>,
    ) {
        self.model.update(cx, |model, cx| model.run(label, op, cx));
    }

    fn set_tab(&mut self, tab: usize, cx: &mut Context<Self>) {
        self.tab = tab;
        cx.notify();
    }

    fn render_branch_button(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let model = self.model.clone();
        let state = self.model.read(cx);
        let label = match (&state.current_branch, state.state) {
            (Some(branch), RepoState::Clean) => branch.clone(),
            (Some(branch), RepoState::Merging) => format!("{branch} (Merging)"),
            (Some(branch), RepoState::Rebasing) => format!("{branch} (Rebasing)"),
            (Some(branch), _) => branch.clone(),
            (None, RepoState::Rebasing) => "Rebasing".into(),
            (None, _) => "Detached HEAD".into(),
        };
        Button::new("branch-popup")
            .icon(IconName::GitBranch)
            .label(label)
            .ghost()
            .small()
            .dropdown_caret(true)
            .dropdown_menu(move |mut menu, window, cx| {
                let m = model.clone();
                menu = menu.item(PopupMenuItem::new("New Branch...").icon(IconName::Plus).on_click(
                    move |_, window, cx| new_branch(&m, "HEAD".into(), window, cx),
                ));
                let m = model.clone();
                menu = menu.item(PopupMenuItem::new("Checkout Tag or Revision...").on_click(
                    move |_, window, cx| {
                        let m = m.clone();
                        prompt_text(
                            "Checkout Tag or Revision",
                            "Checkout",
                            "",
                            move |rev, _, cx| {
                                m.update(cx, |m, cx| {
                                    m.run(format!("Checkout {rev}"), move |repo| repo.checkout(&rev), cx)
                                });
                            },
                            window,
                            cx,
                        );
                    },
                ));
                let branches = model.read(cx).branches.clone();
                for (title, kind) in [("Local", RefKind::Local), ("Remote", RefKind::Remote)] {
                    let group: Vec<_> = branches.iter().filter(|b| b.kind == kind).cloned().collect();
                    if group.is_empty() {
                        continue;
                    }
                    menu = menu.separator().label(title);
                    for branch in group {
                        let m = model.clone();
                        let label = match (&branch.track, branch.is_head) {
                            (_, true) => format!("★ {}", branch.name),
                            (Some(track), _) => format!("{}  ({track})", branch.name),
                            _ => branch.name.clone(),
                        };
                        menu = menu.submenu(label, window, cx, move |menu, _, cx| {
                            branch_menu(menu, &m, &branch, cx)
                        });
                    }
                }
                menu
            })
    }

    fn render_stash_button(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let model = self.model.clone();
        let count = self.model.read(cx).stashes.len();
        Button::new("stashes")
            .icon(IconName::Archive)
            .label(format!("Stashes ({count})"))
            .ghost()
            .small()
            .dropdown_caret(true)
            .dropdown_menu(move |mut menu, window, cx| {
                let stashes = model.read(cx).stashes.clone();
                if stashes.is_empty() {
                    return menu.label("No stashes");
                }
                let m = model.clone();
                menu = menu
                    .item(PopupMenuItem::new("Show Stashes…").on_click(move |_, window, cx| {
                        crate::stash_viewer::open(m.clone(), window, cx)
                    }))
                    .separator();
                for stash in stashes {
                    let m = model.clone();
                    menu = menu.submenu(
                        format!("{}: {}", stash.name, stash.message),
                        window,
                        cx,
                        move |menu, _, _| {
                            let mut menu = menu;
                            for (label, kind) in [("Pop", 0), ("Apply", 1), ("Drop", 2)] {
                                let m = m.clone();
                                let name = stash.name.clone();
                                menu = menu.item(PopupMenuItem::new(label).on_click(move |_, window, cx| {
                                    let m = m.clone();
                                    let name = name.clone();
                                    let run = move |_: &mut Window, cx: &mut App| {
                                        let name = name.clone();
                                        m.update(cx, |m, cx| {
                                            m.run(
                                                format!("{label} {name}"),
                                                move |repo| match kind {
                                                    0 => repo.stash_pop(&name),
                                                    1 => repo.stash_apply(&name),
                                                    _ => repo.stash_drop(&name),
                                                },
                                                cx,
                                            )
                                        });
                                    };
                                    if kind == 2 {
                                        confirm("Drop Stash", "Drop this stash? This cannot be undone.", "Drop", run, window, cx);
                                    } else {
                                        run(window, cx);
                                    }
                                }));
                            }
                            menu
                        },
                    );
                }
                menu
            })
    }

    fn render_toolbar(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let repo_model = self.model.clone();
        let model = self.model.read(cx);
        let busy = model.busy.clone();
        let name = model.repo.name();
        let root = model.repo.root().display().to_string();
        let is_dark = cx.theme().mode.is_dark();
        h_flex()
            .flex_none()
            .h(px(40.))
            .px_2()
            .gap_1()
            .bg(cx.theme().title_bar)
            .border_b_1()
            .border_color(cx.theme().border)
            .child(
                Button::new("repo")
                    .icon(IconName::FolderGit2)
                    .label(name)
                    .ghost()
                    .small()
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, _| {
                        menu = menu.item(
                            PopupMenuItem::new("Open Repository…")
                                .icon(IconName::FolderOpen)
                                .on_click(|_, _, cx| crate::prompt_open_repo(None, cx)),
                        );
                        menu = menu.item(
                            PopupMenuItem::new("Clone Repository…")
                                .icon(IconName::Download)
                                .on_click(|_, window, cx| crate::clone_dialog::open(None, window, cx)),
                        );
                        let m = repo_model.clone();
                        menu = menu.item(
                            PopupMenuItem::new("Manage Remotes…")
                                .icon(IconName::Globe)
                                .on_click(move |_, window, cx| crate::remotes_dialog::open(m.clone(), window, cx)),
                        );
                        let recent: Vec<_> = Settings::load()
                            .recent
                            .into_iter()
                            .filter(|path| path.display().to_string() != root)
                            .collect();
                        if !recent.is_empty() {
                            menu = menu.separator().label("Recent Repositories");
                            for path in recent {
                                let label = recent_label(&path);
                                menu = menu.item(PopupMenuItem::new(label).on_click(move |_, _, cx| {
                                    crate::open_path(&path, None, cx)
                                }));
                            }
                        }
                        menu
                    }),
            )
            .child(self.render_branch_button(cx))
            .child(div().w(px(1.)).h(px(18.)).mx_1().bg(cx.theme().border))
            .child(
                Button::new("fetch")
                    .icon(IconName::RefreshCcw)
                    .label("Fetch")
                    .ghost()
                    .small()
                    .on_click(cx.listener(|this, _, _, cx| this.run("Fetch", |repo| repo.fetch(), cx))),
            )
            .child(
                Button::new("pull")
                    .icon(IconName::ArrowDownToLine)
                    .label("Pull")
                    .ghost()
                    .small()
                    .on_click(cx.listener(|this, _, _, cx| this.run("Pull", |repo| repo.pull(), cx))),
            )
            .child(
                Button::new("push")
                    .icon(IconName::ArrowUpFromLine)
                    .label("Push")
                    .ghost()
                    .small()
                    .on_click(cx.listener(|this, _, window, cx| {
                        crate::push_dialog::open(this.model.clone(), window, cx)
                    })),
            )
            .child(self.render_stash_button(cx))
            .child(div().flex_1())
            .when_some(busy, |row, busy| {
                row.child(
                    h_flex()
                        .gap_1()
                        .text_xs()
                        .text_color(cx.theme().muted_foreground)
                        .child(Icon::new(IconName::LoaderCircle).xsmall())
                        .child(format!("{busy}…")),
                )
            })
            .child(
                Button::new("theme")
                    .icon(if is_dark { IconName::Sun } else { IconName::Moon })
                    .ghost()
                    .small()
                    .tooltip("Toggle Theme")
                    .on_click(move |_, window, cx| {
                        let mode = if is_dark { ThemeMode::Light } else { ThemeMode::Dark };
                        Theme::change(mode, Some(window), cx);
                        Settings::update(|settings| settings.dark = !is_dark);
                    }),
            )
    }
}

impl Render for Workspace {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let changes = self.changes.read(cx).change_count(cx);
        let content = match self.tab {
            0 => self.log.clone().into_any_element(),
            1 => self.changes.clone().into_any_element(),
            _ => self.console.clone().into_any_element(),
        };
        v_flex()
            .key_context("Workspace")
            .track_focus(&self.focus)
            .on_action(cx.listener(|this, _: &CommitChanges, window, cx| {
                this.set_tab(1, cx);
                let message = this.changes.read(cx).message_focus(cx);
                message.focus(window, cx);
            }))
            .on_action(cx.listener(|this, _: &PushCommits, window, cx| {
                crate::push_dialog::open(this.model.clone(), window, cx)
            }))
            .on_action(cx.listener(|this, _: &UpdateProject, _, cx| {
                this.run("Update Project", |repo| repo.pull(), cx)
            }))
            .on_action(cx.listener(|this, _: &ShowLog, _, cx| this.set_tab(0, cx)))
            .on_action(cx.listener(|this, _: &ShowCommitTab, _, cx| this.set_tab(1, cx)))
            .on_action(cx.listener(|this, _: &ShowConsole, _, cx| this.set_tab(2, cx)))
            .on_action(cx.listener(|this, _: &RefreshRepository, _, cx| {
                this.model.update(cx, |model, cx| model.refresh(cx));
                this.log.update(cx, |log, cx| log.reload(cx));
            }))
            .size_full()
            .bg(cx.theme().background)
            .text_color(cx.theme().foreground)
            .child(self.render_toolbar(cx))
            .child(
                TabBar::new("main-tabs")
                    .underline()
                    .small()
                    .selected_index(self.tab)
                    .child(Tab::new().label("Log").prefix(Icon::new(IconName::GitGraph).small()))
                    .child(
                        Tab::new()
                            .label(format!("Commit ({changes})"))
                            .prefix(Icon::new(IconName::GitCommitHorizontal).small()),
                    )
                    .child(Tab::new().label("Console").prefix(Icon::new(IconName::SquareTerminal).small()))
                    .on_click(cx.listener(|this, ix: &usize, _, cx| {
                        this.set_tab(*ix, cx);
                    })),
            )
            .child(div().flex_1().min_h_0().child(content))
            .child(
                h_flex()
                    .flex_none()
                    .h(px(24.))
                    .px_2()
                    .gap_3()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .border_t_1()
                    .border_color(cx.theme().border)
                    .child(self.model.read(cx).repo.root().display().to_string())
                    .child(div().flex_1())
                    .child(
                        self.model
                            .read(cx)
                            .current_branch
                            .clone()
                            .unwrap_or_else(|| "Detached HEAD".into()),
                    )
                    .font_normal(),
            )
    }
}

/// "name — ~/dir/…/parent" for the recent repositories menu.
fn recent_label(path: &std::path::Path) -> String {
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let mut dir = path.parent().map(|p| p.display().to_string()).unwrap_or_default();
    if let Some(home) = std::env::var_os("HOME").map(|h| h.to_string_lossy().into_owned())
        && let Some(rest) = dir.strip_prefix(&home)
    {
        dir = format!("~{rest}");
    }
    let chars: Vec<char> = dir.chars().collect();
    if chars.len() > 48 {
        let head: String = chars[..16].iter().collect();
        let tail: String = chars[chars.len() - 28..].iter().collect();
        dir = format!("{head}…{tail}");
    }
    format!("{name} — {dir}")
}
