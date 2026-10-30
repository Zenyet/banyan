//! Main window: toolbar with the branch popup and VCS operations, and the
//! Log / Commit tabs.

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _, WindowExt as _,
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
use crate::shelf_view::ShelfView;
use crate::git::{Branch, RefKind, RepoState};
use crate::log_view::LogView;
use crate::model::{RepoEvent, RepoModel};
use crate::settings::{Settings, ThemeChoice};
use crate::ui::{SubView, cached, confirm, prompt_text};
use crate::i18n::tr;

actions!(
    workspace,
    [CommitChanges, PushCommits, UpdateProject, ApplyPatch, ManageWorktrees, ShowSubmodules, FindInLog, GoToRevision, ShowLog, ShowCommitTab, ShowConsole, RefreshRepository]
);

/// JetBrains' macOS keymap where it has an equivalent.
pub fn init(cx: &mut App) {
    cx.bind_keys([
        KeyBinding::new(secondary!("k"), CommitChanges, Some("Workspace")),
        KeyBinding::new(secondary!("shift-k"), PushCommits, Some("Workspace")),
        KeyBinding::new(secondary!("t"), UpdateProject, Some("Workspace")),
        KeyBinding::new(secondary!("9"), ShowLog, Some("Workspace")),
        KeyBinding::new(secondary!("0"), ShowCommitTab, Some("Workspace")),
        KeyBinding::new(secondary!("r"), RefreshRepository, Some("Workspace")),
        KeyBinding::new(secondary!("f"), FindInLog, Some("Workspace")),
        KeyBinding::new(secondary!("g"), GoToRevision, Some("Workspace")),
    ]);
}

/// The height of a small underlined `TabBar`.
const TAB_BAR_HEIGHT: Pixels = px(30.);

/// The parts around the tab content, as cached views: the whole workspace
/// repaints with every scroll step of a list inside it.
#[derive(Clone)]
struct Chrome {
    toolbar: Entity<SubView<Workspace>>,
    tabs: Entity<SubView<Workspace>>,
    status_bar: Entity<SubView<Workspace>>,
}

pub struct Workspace {
    model: Entity<RepoModel>,
    focus: FocusHandle,
    log: Entity<LogView>,
    changes: Entity<ChangesView>,
    shelf: Entity<ShelfView>,
    console: Entity<ConsoleView>,
    tab: usize,
    chrome: Option<Chrome>,
    _subscriptions: Vec<Subscription>,
}

impl Workspace {
    pub fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let log = cx.new(|cx| LogView::new(model.clone(), window, cx));
        let changes = cx.new(|cx| ChangesView::new(model.clone(), window, cx));
        let shelf = cx.new(|cx| ShelfView::new(model.clone(), cx));
        let console = cx.new(|cx| ConsoleView::new(model.clone(), cx));
        let model_for_events = model.clone();
        let subscriptions = vec![
            cx.subscribe_in(&model, window, move |_, _, event, window, cx| match event {
                RepoEvent::OpFailed(message) => {
                    window.push_notification(Notification::error(message.clone()), cx)
                }
                RepoEvent::Conflicts(label) => {
                    window.push_notification(
                        Notification::warning(tr!("{} stopped: resolve the conflicts to continue", label)),
                        cx,
                    );
                    if !window.has_active_dialog(cx) {
                        crate::conflicts_dialog::open(model_for_events.clone(), window, cx);
                    }
                }
                RepoEvent::OpSucceeded(label) => {
                    window.push_notification(Notification::success(label.clone()), cx)
                }
                RepoEvent::CheckoutBlocked { target, remote, message } => {
                    offer_smart_checkout(&model_for_events, target.clone(), *remote, message.clone(), window, cx)
                }
                RepoEvent::Changed => cx.notify(),
                RepoEvent::MessageRestored(_) => {}
            }),
            cx.observe(&model, |_, _, cx| cx.notify()),
            // Pick up changes made outside the app, like JetBrains does on frame activation.
            cx.observe_window_appearance(window, |_, _, cx| {
                if Settings::read(|s| s.theme) == ThemeChoice::System {
                    crate::settings_window::apply_appearance(cx);
                }
            }),
            cx.observe_window_activation(window, |this, window, cx| {
                if window.is_window_active() {
                    this.model.update(cx, |model, cx| model.refresh(cx));
                }
            }),
        ];
        let name = model.read(cx).repo.name();
        window.set_window_title(&format!("{name} – Banyan"));
        let focus = cx.focus_handle();
        focus.focus(window, cx);
        Self {
            model,
            focus,
            log,
            changes,
            shelf,
            console,
            tab: 0,
            chrome: None,
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
            (Some(branch), RepoState::Merging) => tr!("{} (Merging)", branch),
            (Some(branch), RepoState::Rebasing) => tr!("{} (Rebasing)", branch),
            (Some(branch), _) => branch.clone(),
            (None, RepoState::Rebasing) => tr!("Rebasing").into(),
            (None, _) => tr!("Detached HEAD").into(),
        };
        Button::new("branch-popup")
            .icon(IconName::GitBranch)
            .label(label)
            .ghost()
            .small()
            .dropdown_caret(true)
            .dropdown_menu(move |mut menu, window, cx| {
                let m = model.clone();
                menu = menu.item(PopupMenuItem::new(tr!("New Branch...")).icon(IconName::Plus).on_click(
                    move |_, window, cx| new_branch(&m, "HEAD".into(), window, cx),
                ));
                let m = model.clone();
                menu = menu.item(PopupMenuItem::new(tr!("Checkout Tag or Revision...")).on_click(
                    move |_, window, cx| {
                        let m = m.clone();
                        prompt_text(
                            tr!("Checkout Tag or Revision"),
                            tr!("Checkout"),
                            "",
                            move |rev, _, cx| {
                                m.update(cx, |m, cx| m.checkout(rev, false, cx));
                            },
                            window,
                            cx,
                        );
                    },
                ));
                let (branches, favorites, recent) = {
                    let state = model.read(cx);
                    (state.branches.clone(), state.favorites.clone(), state.recent_branches.clone())
                };
                // ✓ marks the current branch and ★ the favorites, which come first in their group.
                let label = |branch: &Branch| {
                    let mark = if branch.is_head {
                        "✓ "
                    } else if favorites.contains(&branch.full_name()) {
                        "★ "
                    } else {
                        ""
                    };
                    match &branch.track {
                        Some(track) => format!("{mark}{}  ({track})", branch.name),
                        None => format!("{mark}{}", branch.name),
                    }
                };
                let recent: Vec<Branch> = recent
                    .iter()
                    .filter_map(|name| branches.iter().find(|b| b.kind == RefKind::Local && b.name == *name && !b.is_head))
                    .take(5)
                    .cloned()
                    .collect();
                let mut groups = Vec::new();
                if !recent.is_empty() {
                    groups.push((tr!("Recent"), recent));
                }
                for (title, kind) in [(tr!("Local"), RefKind::Local), (tr!("Remote"), RefKind::Remote)] {
                    let mut group: Vec<Branch> = branches.iter().filter(|b| b.kind == kind).cloned().collect();
                    // Stable, so each part keeps git's order.
                    group.sort_by_key(|b| !favorites.contains(&b.full_name()));
                    if !group.is_empty() {
                        groups.push((title, group));
                    }
                }
                for (title, group) in groups {
                    menu = menu.separator().label(title);
                    for branch in group {
                        let m = model.clone();
                        menu = menu.submenu(label(&branch), window, cx, move |menu, _, cx| {
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
            .label(tr!("Stashes ({})", count))
            .ghost()
            .small()
            .dropdown_caret(true)
            .dropdown_menu(move |mut menu, window, cx| {
                let stashes = model.read(cx).stashes.clone();
                if stashes.is_empty() {
                    return menu.label(tr!("No stashes"));
                }
                let m = model.clone();
                menu = menu
                    .item(PopupMenuItem::new(tr!("Show Stashes…")).on_click(move |_, window, cx| {
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
                            for (label, kind) in [(tr!("Pop"), 0), (tr!("Apply"), 1), (tr!("Drop"), 2)] {
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
                                        confirm(tr!("Drop Stash"), tr!("Drop this stash? This cannot be undone."), tr!("Drop"), run, window, cx);
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

    fn render_tabs(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let changes = self.changes.read(cx).change_count(cx);
        TabBar::new("main-tabs")
            .underline()
            .small()
            .px_2()
            .selected_index(self.tab)
            .child(Tab::new().label(tr!("Log")).prefix(Icon::new(IconName::GitGraph).small()))
            .child(
                Tab::new()
                    .label(tr!("Commit ({})", changes))
                    .prefix(Icon::new(IconName::GitCommitHorizontal).small()),
            )
            .child(
                Tab::new()
                    .label(match self.model.read(cx).shelves.len() {
                        0 => tr!("Shelf").to_string(),
                        n => tr!("Shelf ({})", n),
                    })
                    .prefix(Icon::new(IconName::Inbox).small()),
            )
            .child(Tab::new().label(tr!("Console")).prefix(Icon::new(IconName::SquareTerminal).small()))
            .on_click(cx.listener(|this, ix: &usize, _, cx| {
                this.set_tab(*ix, cx);
            }))
    }

    fn render_status_bar(&self, cx: &mut Context<Self>) -> impl IntoElement {
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
                    .unwrap_or_else(|| tr!("Detached HEAD").into()),
            )
            .font_normal()
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
                            PopupMenuItem::new(tr!("Open Repository…"))
                                .icon(IconName::FolderOpen)
                                .on_click(|_, _, cx| crate::prompt_open_repo(None, cx)),
                        );
                        menu = menu.item(
                            PopupMenuItem::new(tr!("Clone Repository…"))
                                .icon(IconName::Download)
                                .on_click(|_, window, cx| crate::clone_dialog::open(None, window, cx)),
                        );
                        let m = repo_model.clone();
                        menu = menu.item(
                            PopupMenuItem::new(tr!("Manage Remotes…"))
                                .icon(IconName::Globe)
                                .on_click(move |_, window, cx| crate::remotes_dialog::open(m.clone(), window, cx)),
                        );
                        let m = repo_model.clone();
                        menu = menu.item(
                            PopupMenuItem::new(tr!("Manage Worktrees…"))
                                .icon(IconName::GitBranch)
                                .on_click(move |_, window, cx| crate::worktrees_dialog::open(m.clone(), None, window, cx)),
                        );
                        let m = repo_model.clone();
                        menu = menu.item(
                            PopupMenuItem::new(tr!("Submodules…"))
                                .icon(IconName::FolderOpen)
                                .on_click(move |_, window, cx| crate::submodules_dialog::open(m.clone(), window, cx)),
                        );
                        let recent: Vec<_> = Settings::get()
                            .recent
                            .into_iter()
                            .filter(|path| path.display().to_string() != root)
                            .collect();
                        if !recent.is_empty() {
                            menu = menu.separator().label(tr!("Recent Repositories"));
                            for path in recent {
                                let label = recent_label(&path);
                                menu = menu.item(PopupMenuItem::new(label).on_click(move |_, window, cx| {
                                    crate::welcome::open_path(&path, None, window, cx)
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
                    .label(tr!("Fetch"))
                    .ghost()
                    .small()
                    .on_click(cx.listener(|this, _, _, cx| this.run(tr!("Fetch"), |repo| repo.fetch(), cx))),
            )
            .child(
                Button::new("pull")
                    .icon(IconName::ArrowDownToLine)
                    .label(tr!("Update"))
                    .ghost()
                    .small()
                    .tooltip(tr!("Update Project (⌘T)"))
                    .on_click(cx.listener(|this, _, window, cx| {
                        crate::update_dialog::open(this.model.clone(), window, cx)
                    })),
            )
            .child(
                Button::new("push")
                    .icon(IconName::ArrowUpFromLine)
                    .label(tr!("Push"))
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
                    .tooltip(tr!("Toggle Theme"))
                    .on_click(move |_, _, cx| {
                        let theme = if is_dark { ThemeChoice::Light } else { ThemeChoice::Dark };
                        crate::settings_window::change(cx, |settings| settings.theme = theme);
                    }),
            )
    }
}

impl Render for Workspace {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let content = match self.tab {
            0 => self.log.clone().into_any_element(),
            1 => self.changes.clone().into_any_element(),
            2 => self.shelf.clone().into_any_element(),
            _ => self.console.clone().into_any_element(),
        };
        let workspace = cx.entity();
        let chrome = self
            .chrome
            .get_or_insert_with(|| Chrome {
                toolbar: SubView::new(&workspace, |this, cx| this.render_toolbar(cx).into_any_element(), cx),
                tabs: SubView::new(&workspace, |this, cx| this.render_tabs(cx).into_any_element(), cx),
                status_bar: SubView::new(&workspace, |this, cx| this.render_status_bar(cx).into_any_element(), cx),
            })
            .clone();
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
            .on_action(cx.listener(|this, _: &ApplyPatch, _, cx| crate::actions::apply_patch(&this.model, cx)))
            .on_action(cx.listener(|this, _: &ManageWorktrees, window, cx| {
                crate::worktrees_dialog::open(this.model.clone(), None, window, cx)
            }))
            .on_action(cx.listener(|this, _: &ShowSubmodules, window, cx| {
                crate::submodules_dialog::open(this.model.clone(), window, cx)
            }))
            .on_action(cx.listener(|this, _: &UpdateProject, window, cx| {
                crate::update_dialog::open(this.model.clone(), window, cx)
            }))
            .on_action(cx.listener(|this, _: &FindInLog, window, cx| {
                this.set_tab(0, cx);
                let search = this.log.read(cx).search_focus(cx);
                search.focus(window, cx);
            }))
            .on_action(cx.listener(|this, _: &GoToRevision, window, cx| {
                this.set_tab(0, cx);
                let log = this.log.downgrade();
                prompt_text(
                    tr!("Go to Hash, Branch or Tag"),
                    tr!("Go"),
                    "",
                    move |rev, window, cx| {
                        log.update(cx, |log, cx| log.go_to(rev, window, cx)).ok();
                    },
                    window,
                    cx,
                );
            }))
            .on_action(cx.listener(|this, _: &ShowLog, _, cx| this.set_tab(0, cx)))
            .on_action(cx.listener(|this, _: &ShowCommitTab, _, cx| this.set_tab(1, cx)))
            .on_action(cx.listener(|this, _: &ShowConsole, _, cx| this.set_tab(3, cx)))
            .on_action(cx.listener(|this, _: &RefreshRepository, _, cx| {
                this.model.update(cx, |model, cx| model.refresh(cx));
                this.log.update(cx, |log, cx| log.reload(cx));
            }))
            // A folder dropped on the window opens in a window of its own.
            .on_drop({
                let root = self.model.read(cx).repo.root().to_path_buf();
                move |paths: &ExternalPaths, window, cx| {
                    let Some(path) = paths.paths().first() else { return };
                    if crate::git::Repo::open(path).is_ok_and(|repo| repo.root() == root) {
                        return;
                    }
                    crate::welcome::open_path(path, None, window, cx);
                }
            })
            .size_full()
            .bg(cx.theme().background)
            .text_color(cx.theme().foreground)
            .child(div().flex_none().h(px(40.)).child(cached(chrome.toolbar)))
            .children(crate::conflicts_dialog::render_banner(&self.model, cx))
            .child(div().flex_none().h(TAB_BAR_HEIGHT).child(cached(chrome.tabs)))
            .child(div().flex_1().min_h_0().child(content))
            .child(div().flex_none().h(px(24.)).child(cached(chrome.status_bar)))
    }
}

/// "name — ~/dir/…/parent" for the recent repositories menu.
fn recent_label(path: &std::path::Path) -> String {
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let mut dir = path.parent().map(|p| p.display().to_string()).unwrap_or_default();
    if let Some(home) = std::env::home_dir().map(|h| h.to_string_lossy().into_owned())
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

/// JetBrains' "Git Checkout Problem" dialog: stash and restore the local
/// changes, or discard them.
fn offer_smart_checkout(
    model: &Entity<RepoModel>,
    target: String,
    remote: bool,
    message: SharedString,
    window: &mut Window,
    cx: &mut App,
) {
    let model = model.clone();
    let files: Vec<SharedString> = crate::git::blocked_files(&message)
        .into_iter()
        .map(SharedString::from)
        .collect();
    window.open_dialog(cx, move |dialog, _, cx| {
        let smart = (model.clone(), target.clone());
        let force = (model.clone(), target.clone());
        dialog
            .title(tr!("Git Checkout Problem"))
            .w(px(520.))
            .child(
                v_flex()
                    .gap_3()
                    .child(div().text_sm().child(tr!(
                        "Your local changes to these files would be overwritten by checking out {}:",
                        crate::actions::short(&target)
                    )))
                    .child(
                        v_flex()
                            .id("blocked-files")
                            .max_h(px(160.))
                            .overflow_y_scroll()
                            .px_2()
                            .text_sm()
                            .font_family(cx.theme().mono_font_family.clone())
                            .children(files.iter().cloned()),
                    )
                    .child(
                        div()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(tr!("Smart Checkout stashes the changes, checks out and restores them.")),
                    )
                    .child(
                        h_flex()
                            .justify_end()
                            .gap_2()
                            .child(
                                Button::new("checkout-cancel")
                                    .label(tr!("Cancel"))
                                    .on_click(|_, window, cx| window.close_dialog(cx)),
                            )
                            .child(Button::new("checkout-force").label(tr!("Force Checkout")).danger().on_click(
                                move |_, window, cx| {
                                    window.close_dialog(cx);
                                    let (model, target) = force.clone();
                                    model.update(cx, |m, cx| {
                                        m.run(
                                            tr!("Force checkout {}", crate::actions::short(&target)),
                                            move |repo| repo.force_checkout(&target, remote),
                                            cx,
                                        )
                                    });
                                },
                            ))
                            .child(Button::new("checkout-smart").label(tr!("Smart Checkout")).primary().on_click(
                                move |_, window, cx| {
                                    window.close_dialog(cx);
                                    let (model, target) = smart.clone();
                                    model.update(cx, |m, cx| {
                                        m.run(
                                            tr!("Smart checkout {}", crate::actions::short(&target)),
                                            move |repo| repo.smart_checkout(&target, remote),
                                            cx,
                                        )
                                    });
                                },
                            )),
                    ),
            )
    });
}
