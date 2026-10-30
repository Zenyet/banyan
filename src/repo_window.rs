//! A window of repositories, one tab each, like the several roots of a
//! JetBrains project: open another repository into the window, switch
//! between them, or fetch them all at once.

use std::cell::RefCell;
use std::path::PathBuf;

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::Repo;
use crate::i18n::tr;
use crate::model::RepoModel;
use crate::settings::Settings;
use crate::workspace::Workspace;

actions!(repo_window, [NextRepository, PreviousRepository, FetchAllRepositories]);

thread_local! {
    /// Every open repository window, oldest first.
    static WINDOWS: RefCell<Vec<(AnyWindowHandle, WeakEntity<RepoWindow>)>> = const { RefCell::new(Vec::new()) };
}

fn live_windows() -> Vec<(AnyWindowHandle, WeakEntity<RepoWindow>)> {
    WINDOWS.with_borrow_mut(|windows| {
        windows.retain(|(_, view)| view.upgrade().is_some());
        windows.clone()
    })
}

fn window_options(cx: &App) -> WindowOptions {
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(None, size(px(1440.), px(920.)), cx))),
        ..Default::default()
    }
}

/// Open `repo`: as a tab of the active repository window (or the last one
/// used), unless the settings say a window of its own, and where it's open
/// already, by switching to it.
pub fn open(repo: Repo, cx: &mut App) {
    open_all(vec![repo], cx)
}

/// Open several repositories as tabs of one window, the first one shown.
pub fn open_all(repos: Vec<Repo>, cx: &mut App) {
    let Some(first) = repos.first() else { return };
    let active = cx.active_window();
    // Whoever asked may be updating that very window; act once it's done.
    // (A deferred call would wait for the next event when the app starts.)
    let first_root = first.root().to_path_buf();
    cx.spawn(async move |cx| cx.update(move |cx| {
        let mut repos = repos;
        // Already open somewhere: show it there.
        if repos.len() == 1 && show_existing(&first_root, cx) {
            return;
        }
        for repo in &repos {
            Settings::add_recent(repo.root().to_path_buf());
        }
        let windows = live_windows();
        let target = if Settings::read(|s| s.open_in_new_window) {
            None
        } else {
            windows
                .iter()
                .find(|(handle, _)| Some(*handle) == active)
                .or_else(|| windows.last())
                .cloned()
        };
        match target {
            Some((handle, view)) => {
                handle
                    .update(cx, |_, window, cx| {
                        view.update(cx, |this, cx| {
                            for repo in repos.drain(..) {
                                this.add(repo, window, cx);
                            }
                        })
                        .ok();
                        window.activate_window();
                    })
                    .ok();
            }
            None => {
                let opened = gpui_kit::open_window(window_options(cx), cx, |window, cx| {
                    let view = cx.new(|cx| RepoWindow::new(window, cx));
                    view.update(cx, |this, cx| {
                        for repo in repos.drain(..) {
                            this.add(repo, window, cx);
                        }
                        this.activate(0, window, cx);
                    });
                    WINDOWS.with_borrow_mut(|windows| windows.push((window.window_handle(), view.downgrade())));
                    view
                });
                if let Err(err) = opened {
                    eprintln!("failed to open a window: {err:#}");
                }
            }
        }
    }))
    .detach();
}

/// Switch to the tab showing `root`, in whichever window has it.
fn show_existing(root: &std::path::Path, cx: &mut App) -> bool {
    for (handle, view) in live_windows() {
        let Some(view) = view.upgrade() else { continue };
        let Some(ix) = view.read(cx).index_of(root, cx) else { continue };
        handle
            .update(cx, |_, window, cx| {
                view.update(cx, |this, cx| this.activate(ix, window, cx));
                window.activate_window();
            })
            .ok();
        return true;
    }
    false
}

/// Close the active tab of `handle` if it has others; `false` when the
/// window should close instead.
pub fn close_active_tab(handle: AnyWindowHandle, cx: &mut App) -> bool {
    let Some(view) = live_windows().into_iter().find(|(h, _)| *h == handle).and_then(|(_, v)| v.upgrade()) else {
        return false;
    };
    if view.read(cx).tabs.len() < 2 {
        return false;
    }
    handle
        .update(cx, |_, window, cx| {
            view.update(cx, |this, cx| {
                let active = this.active;
                this.close(active, window, cx)
            })
        })
        .is_ok()
}

pub struct RepoWindow {
    tabs: Vec<Entity<Workspace>>,
    active: usize,
    _subscriptions: Vec<Subscription>,
}

impl RepoWindow {
    fn new(_: &mut Window, _: &mut Context<Self>) -> Self {
        Self { tabs: Vec::new(), active: 0, _subscriptions: Vec::new() }
    }

    fn model(&self, ix: usize, cx: &App) -> Entity<RepoModel> {
        self.tabs[ix].read(cx).model().clone()
    }

    fn index_of(&self, root: &std::path::Path, cx: &App) -> Option<usize> {
        (0..self.tabs.len()).find(|&ix| self.model(ix, cx).read(cx).repo.root() == root)
    }

    /// Open `repo` in a new tab and show it, or show its tab.
    fn add(&mut self, repo: Repo, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(ix) = self.index_of(repo.root(), cx) {
            return self.activate(ix, window, cx);
        }
        let model = cx.new(|cx| RepoModel::new(repo, cx));
        // The tab shows the branch and whether something runs.
        self._subscriptions.push(cx.observe(&model, |_, _, cx| cx.notify()));
        let workspace = cx.new(|cx| Workspace::new(model, window, cx));
        self.tabs.push(workspace);
        self.activate(self.tabs.len() - 1, window, cx);
    }

    fn activate(&mut self, ix: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(workspace) = self.tabs.get(ix) else { return };
        self.active = ix;
        let name = workspace.read(cx).model().read(cx).repo.name();
        window.set_window_title(&format!("{name} – Banyan"));
        workspace.read(cx).focus_handle().focus(window, cx);
        self.remember(cx);
        cx.notify();
    }

    fn close(&mut self, ix: usize, window: &mut Window, cx: &mut Context<Self>) {
        if ix >= self.tabs.len() {
            return;
        }
        if self.tabs.len() == 1 {
            window.remove_window();
            return;
        }
        self.tabs.remove(ix);
        let active = if self.active > ix || self.active == self.tabs.len() { self.active.saturating_sub(1) } else { self.active };
        self.activate(active, window, cx);
    }

    fn step(&mut self, delta: isize, window: &mut Window, cx: &mut Context<Self>) {
        let count = self.tabs.len() as isize;
        if count > 1 {
            let ix = (self.active as isize + delta).rem_euclid(count) as usize;
            self.activate(ix, window, cx);
        }
    }

    fn fetch_all(&mut self, cx: &mut Context<Self>) {
        for ix in 0..self.tabs.len() {
            self.model(ix, cx).update(cx, |m, cx| m.run(tr!("Fetch"), |repo| repo.fetch(), cx));
        }
    }

    /// The window's repositories, to open again at the next launch.
    fn remember(&self, cx: &App) {
        let mut roots: Vec<PathBuf> =
            (0..self.tabs.len()).map(|ix| self.model(ix, cx).read(cx).repo.root().to_path_buf()).collect();
        // The one shown comes first.
        if self.active < roots.len() {
            let active = roots.remove(self.active);
            roots.insert(0, active);
        }
        if Settings::read(|s| s.session != roots) {
            Settings::update(|s| s.session = roots);
        }
    }

    fn render_tab(&self, ix: usize, cx: &mut Context<Self>) -> impl IntoElement {
        let model = self.model(ix, cx);
        let model = model.read(cx);
        let active = ix == self.active;
        let muted = cx.theme().muted_foreground;
        let changes = model.status.len();
        h_flex()
            .id(("repo-tab", ix))
            .flex_none()
            .h_full()
            .pl_3()
            .pr_1()
            .gap_1p5()
            .border_r_1()
            .border_color(cx.theme().border)
            .text_sm()
            .when(active, |tab| tab.bg(cx.theme().background))
            .when(!active, |tab| tab.text_color(muted).hover(|style| style.bg(cx.theme().list_hover)))
            .child(Icon::new(IconName::FolderGit2).xsmall())
            .child(model.repo.name())
            .when_some(model.current_branch.clone(), |tab, branch| {
                tab.child(div().text_xs().text_color(muted).child(branch))
            })
            .when(changes > 0, |tab| {
                tab.child(div().text_xs().text_color(muted).child(format!("· {changes}")))
            })
            .when(model.busy.is_some(), |tab| tab.child(div().text_xs().text_color(cx.theme().primary).child("●")))
            .child(
                Button::new(("close-repo-tab", ix))
                    .icon(IconName::Close)
                    .ghost()
                    .xsmall()
                    .tooltip(tr!("Close Repository"))
                    .on_click(cx.listener(move |this, _, window, cx| {
                        cx.stop_propagation();
                        this.close(ix, window, cx);
                    })),
            )
            .on_click(cx.listener(move |this, _, window, cx| this.activate(ix, window, cx)))
    }
}

impl Render for RepoWindow {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let show_tabs = self.tabs.len() > 1;
        let tabs: Vec<AnyElement> = if show_tabs {
            (0..self.tabs.len()).map(|ix| self.render_tab(ix, cx).into_any_element()).collect()
        } else {
            Vec::new()
        };
        v_flex()
            .key_context("RepoWindow")
            .size_full()
            .bg(cx.theme().background)
            .on_action(cx.listener(|this, _: &NextRepository, window, cx| this.step(1, window, cx)))
            .on_action(cx.listener(|this, _: &PreviousRepository, window, cx| this.step(-1, window, cx)))
            .on_action(cx.listener(|this, _: &FetchAllRepositories, _, cx| this.fetch_all(cx)))
            .when(show_tabs, |col| {
                col.child(
                    h_flex()
                        .id("repo-tabs")
                        .flex_none()
                        .h(px(32.))
                        .bg(cx.theme().title_bar)
                        .border_b_1()
                        .border_color(cx.theme().border)
                        .overflow_x_scroll()
                        .children(tabs)
                        .child(
                            div().px_1().child(
                                Button::new("open-repo-tab")
                                    .icon(IconName::Plus)
                                    .ghost()
                                    .xsmall()
                                    .tooltip(tr!("Open Repository…"))
                                    .on_click(|_, _, cx| crate::prompt_open_repo(None, cx)),
                            ),
                        ),
                )
            })
            .children(self.tabs.get(self.active).cloned().map(|workspace| div().flex_1().min_h_0().child(workspace)))
    }
}
