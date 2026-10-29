// No console window next to the app on Windows.
#![cfg_attr(windows, windows_subsystem = "windows")]

mod actions;
mod askpass;
mod assets;
mod changes_view;
mod clone_dialog;
mod commit_browser;
mod compare;
mod conflict;
mod conflicts_dialog;
mod console;
mod diff_pane;
mod git;
mod graph;
mod history;
mod log_view;
mod merge_tool;
mod model;
mod patch;
mod push_dialog;
mod rebase_dialog;
mod remotes_dialog;
mod settings;
mod settings_window;
mod stash_viewer;
mod ui;
mod update_dialog;
mod watcher;
mod workspace;
mod worktree_diff;

use std::path::{Path, PathBuf};

use gpui_kit::component::{
    ActiveTheme as _, WindowExt as _,
    notification::Notification,
    button::{Button, ButtonVariants as _},
    v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::*;

use crate::git::Repo;
use crate::model::RepoModel;
use crate::workspace::Workspace;

actions!(banyan, [Quit, OpenRepository, CloseWindow, OpenSettings]);

fn main() {
    // Git runs this executable to ask for credentials; answer and exit.
    if let Ok(socket) = std::env::var(askpass::SOCKET_ENV) {
        std::process::exit(askpass::run_client(&socket));
    }
    let requested = std::env::args().nth(1).map(PathBuf::from);

    gpui_kit::application()
        .with_assets(assets::AppAssets)
        .run(move |cx| {
            gpui_kit::init(cx);
            log_view::init(cx);
            workspace::init(cx);
            match askpass::start_server() {
                Ok(requests) => askpass::spawn_prompt_loop(requests, cx),
                Err(err) => eprintln!("credential prompts unavailable: {err}"),
            }
            settings_window::apply_appearance(cx);
            cx.on_action(|_: &Quit, cx| cx.quit());
            cx.on_action(|_: &CloseWindow, cx| {
                // The action is dispatched from inside the window, so close it afterwards.
                if let Some(window) = cx.active_window() {
                    cx.defer(move |cx| {
                        window.update(cx, |_, window, _| window.remove_window()).ok();
                    });
                }
            });
            cx.on_action(|_: &OpenRepository, cx| prompt_open_repo(None, cx));
            cx.on_action(|_: &OpenSettings, cx| settings_window::open(cx));
            cx.bind_keys([
                KeyBinding::new("secondary-q", Quit, None),
                KeyBinding::new("secondary-o", OpenRepository, None),
                KeyBinding::new("secondary-w", CloseWindow, None),
                KeyBinding::new("secondary-,", OpenSettings, None),
            ]);
            cx.set_menus(vec![
                Menu {
                    name: "Banyan".into(),
                    items: vec![
                        MenuItem::action("Settings…", OpenSettings),
                        MenuItem::separator(),
                        MenuItem::action("Quit Banyan", Quit),
                    ],
                    disabled: false,
                },
                Menu {
                    name: "Git".into(),
                    items: vec![
                        MenuItem::action("Commit…", workspace::CommitChanges),
                        MenuItem::action("Push…", workspace::PushCommits),
                        MenuItem::action("Update Project", workspace::UpdateProject),
                        MenuItem::separator(),
                        MenuItem::action("Show Log", workspace::ShowLog),
                        MenuItem::action("Show Commit", workspace::ShowCommitTab),
                        MenuItem::action("Show Console", workspace::ShowConsole),
                        MenuItem::action("Refresh", workspace::RefreshRepository),
                    ],
                    disabled: false,
                },
                Menu {
                    name: "File".into(),
                    items: vec![
                        MenuItem::action("Open Repository…", OpenRepository),
                        MenuItem::separator(),
                        MenuItem::action("Close Window", CloseWindow),
                    ],
                    disabled: false,
                },
            ]);
            cx.on_window_closed(|cx, _| {
                if cx.windows().is_empty() {
                    cx.quit();
                }
            })
            .detach();
            cx.activate(true);

            match initial_repo(requested.as_deref()) {
                Some(repo) => open_repo_window(repo, cx),
                None => open_welcome_window(cx),
            }
        });
}

/// The repository to open at launch: the one named on the command line, the
/// current directory, or (when launched from Finder) the last one opened.
fn initial_repo(requested: Option<&Path>) -> Option<Repo> {
    if let Some(path) = requested {
        return Repo::open(path).ok();
    }
    if let Some(repo) = std::env::current_dir().ok().and_then(|dir| Repo::open(&dir).ok()) {
        return Some(repo);
    }
    settings::Settings::get()
        .recent
        .iter()
        .find_map(|path| Repo::open(path).ok())
}

fn window_options(cx: &App) -> WindowOptions {
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(
            None,
            size(px(1440.), px(920.)),
            cx,
        ))),
        ..Default::default()
    }
}

pub fn open_repo_window(repo: Repo, cx: &mut App) {
    settings::Settings::add_recent(repo.root().to_path_buf());
    let options = window_options(cx);
    gpui_kit::open_window(options, cx, |window, cx| {
        let model = cx.new(|cx| RepoModel::new(repo, cx));
        cx.new(|cx| Workspace::new(model, window, cx))
    })
    .expect("failed to open window");
}

/// Ask for a repository folder and open it in a new window. `replace` is
/// closed once the repository opens (the welcome window).
pub fn prompt_open_repo(replace: Option<AnyWindowHandle>, cx: &mut App) {
    let paths = cx.prompt_for_paths(PathPromptOptions {
        files: false,
        directories: true,
        multiple: false,
        prompt: Some("Open Repository".into()),
    });
    cx.spawn(async move |cx| {
        let Ok(Ok(Some(paths))) = paths.await else { return };
        let Some(path) = paths.into_iter().next() else { return };
        cx.update(|cx| open_path(&path, replace, cx));
    })
    .detach();
}

pub fn open_path(path: &Path, replace: Option<AnyWindowHandle>, cx: &mut App) {
    match Repo::open(path) {
        Ok(repo) => {
            open_repo_window(repo, cx);
            if let Some(window) = replace {
                window.update(cx, |_, window, _| window.remove_window()).ok();
            }
        }
        Err(err) => {
            let message = format!("{err:#}");
            if let Some(window) = cx.active_window() {
                window
                    .update(cx, |_, window, cx| {
                        window.push_notification(Notification::error(message), cx);
                    })
                    .ok();
            }
        }
    }
}

fn open_welcome_window(cx: &mut App) {
    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(
            None,
            size(px(560.), px(380.)),
            cx,
        ))),
        ..Default::default()
    };
    gpui_kit::open_window(options, cx, |window, cx| {
        window.set_window_title("Welcome to Banyan");
        cx.new(|_| Welcome)
    })
    .expect("failed to open window");
}

struct Welcome;

impl Render for Welcome {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        v_flex()
            .size_full()
            .items_center()
            .justify_center()
            .gap_4()
            .bg(cx.theme().background)
            .text_color(cx.theme().foreground)
            .child(div().text_2xl().child("Banyan"))
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child("A JetBrains-style Git client"),
            )
            .child(
                Button::new("open")
                    .primary()
                    .icon(IconName::FolderOpen)
                    .label("Open Repository…")
                    .on_click(|_, window, cx| prompt_open_repo(Some(window.window_handle()), cx)),
            )
            .child(
                Button::new("clone")
                    .icon(IconName::Download)
                    .label("Clone Repository…")
                    .on_click(|_, window, cx| {
                        clone_dialog::open(Some(window.window_handle()), window, cx)
                    }),
            )
    }
}
