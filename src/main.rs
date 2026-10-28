mod actions;
mod changes_view;
mod commit_browser;
mod compare;
mod diff_pane;
mod git;
mod graph;
mod history;
mod log_view;
mod merge_tool;
mod model;
mod push_dialog;
mod rebase_dialog;
mod settings;
mod ui;
mod workspace;

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

actions!(gitbrains, [Quit, OpenRepository, CloseWindow]);

fn main() {
    let path = std::env::args()
        .nth(1)
        .map(PathBuf::from)
        .or_else(|| std::env::current_dir().ok());

    gpui_kit::application()
        .with_assets(gpui_kit::assets::AllAssets)
        .run(move |cx| {
            gpui_kit::init(cx);
            log_view::init(cx);
            if settings::Settings::load().dark {
                gpui_kit::component::Theme::change(gpui_kit::component::ThemeMode::Dark, None, cx);
            }
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
            cx.bind_keys([
                KeyBinding::new("cmd-q", Quit, None),
                KeyBinding::new("cmd-o", OpenRepository, None),
                KeyBinding::new("cmd-w", CloseWindow, None),
            ]);
            cx.set_menus(vec![
                Menu {
                    name: "GitBrains".into(),
                    items: vec![MenuItem::action("Quit GitBrains", Quit)],
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

            match path.as_deref().map(Repo::open) {
                Some(Ok(repo)) => open_repo_window(repo, cx),
                _ => open_welcome_window(cx),
            }
        });
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
        window.set_window_title("Welcome to GitBrains");
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
            .child(div().text_2xl().child("GitBrains"))
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
    }
}
