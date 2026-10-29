// No console window next to the app on Windows.
#![cfg_attr(windows, windows_subsystem = "windows")]

/// A key binding with ⌘ on macOS and Ctrl elsewhere: `secondary!("shift-k")`.
/// `KeyBinding::new` can't resolve GPUI's own `secondary-` prefix.
macro_rules! secondary {
    ($keys:literal) => {
        if cfg!(target_os = "macos") { concat!("cmd-", $keys) } else { concat!("ctrl-", $keys) }
    };
}

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
mod edit_history;
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
mod welcome;
mod workspace;
mod worktree_diff;

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};

use gpui_kit::*;

use crate::git::Repo;
use crate::model::RepoModel;
use crate::workspace::Workspace;

actions!(banyan, [Quit, OpenRepository, CloseWindow, OpenSettings]);

/// Set by Quit, so that windows closing on the way out don't bring back the
/// welcome window.
static QUITTING: AtomicBool = AtomicBool::new(false);

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
            cx.on_action(|_: &Quit, cx| {
                QUITTING.store(true, Ordering::Relaxed);
                cx.quit();
            });
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
                KeyBinding::new(secondary!("q"), Quit, None),
                KeyBinding::new(secondary!("o"), OpenRepository, None),
                KeyBinding::new(secondary!("w"), CloseWindow, None),
                KeyBinding::new(secondary!(","), OpenSettings, None),
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
            // Closing the last repository goes back to the welcome window;
            // closing that quits.
            cx.on_window_closed(|cx, closed| {
                if cx.windows().is_empty() {
                    if welcome::is_welcome(closed) || QUITTING.load(Ordering::Relaxed) {
                        cx.quit();
                    } else {
                        welcome::open(cx);
                    }
                }
            })
            .detach();
            cx.activate(true);

            match initial_repo(requested.as_deref()) {
                Some(repo) => open_repo_window(repo, cx),
                None => {
                    welcome::open(cx);
                    // A folder named on the command line that isn't a repository yet.
                    if let Some(path) = requested.filter(|path| path.is_dir()) {
                        open_path(&path, welcome::window(), cx);
                    }
                }
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

/// Open the repository at `path` in a new window, from `replace` or the
/// active window, which shows any problem; see [`welcome::open_path`].
pub fn open_path(path: &Path, replace: Option<AnyWindowHandle>, cx: &mut App) {
    let path = path.to_path_buf();
    let Some(window) = replace.or_else(|| cx.active_window()).or_else(|| cx.windows().into_iter().next()) else {
        if let Ok(repo) = Repo::open(&path) {
            open_repo_window(repo, cx);
        }
        return;
    };
    window
        .update(cx, |_, window, cx| welcome::open_path(&path, replace, window, cx))
        .ok();
}

#[cfg(test)]
mod tests {
    /// `KeyBinding::new` panics on keystrokes it can't parse, at launch.
    #[test]
    fn key_bindings_parse() {
        for keys in [
            secondary!("q"), secondary!("o"), secondary!("w"), secondary!(","), secondary!("k"),
            secondary!("shift-k"), secondary!("t"), secondary!("9"), secondary!("0"), secondary!("r"),
        ] {
            assert!(gpui_kit::Keystroke::parse(keys).is_ok(), "{keys}");
        }
    }
}
