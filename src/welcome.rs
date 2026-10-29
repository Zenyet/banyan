//! The window shown when no repository is open, like JetBrains' welcome
//! screen: open or clone a repository, reopen a recent one, or drop a folder
//! onto the window.

use std::cell::RefCell;
use std::path::{Path, PathBuf};

use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, StyledExt as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::Repo;
use crate::settings::Settings;
use crate::i18n::tr;

thread_local! {
    static WINDOW: RefCell<Option<AnyWindowHandle>> = const { RefCell::new(None) };
}

/// Show the welcome window, or bring it forward if it is open.
pub fn open(cx: &mut App) {
    if let Some(window) = WINDOW.with_borrow(|w| *w)
        && window.update(cx, |_, window, _| window.activate_window()).is_ok()
    {
        return;
    }
    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(None, size(px(860.), px(540.)), cx))),
        ..Default::default()
    };
    let (handle, _) = gpui_kit::open_window(options, cx, |window, cx| {
        window.set_window_title(tr!("Welcome to Banyan"));
        cx.new(|_| Welcome)
    })
    .expect("failed to open window");
    WINDOW.set(Some(handle));
}

/// The welcome window, while it is open.
pub fn window() -> Option<AnyWindowHandle> {
    WINDOW.with_borrow(|w| *w)
}

/// Whether `id` is the welcome window's.
pub fn is_welcome(id: WindowId) -> bool {
    WINDOW.with_borrow(|w| w.is_some_and(|w| w.window_id() == id))
}

/// Open the repository at `path` (a dropped or chosen folder, or a file in
/// one) in a new window, closing `replace` once it opens. A folder outside
/// any repository can be made one.
pub fn open_path(path: &Path, replace: Option<AnyWindowHandle>, window: &mut Window, cx: &mut App) {
    let dir = if path.is_dir() { path } else { path.parent().unwrap_or(path) };
    match Repo::open(dir) {
        Ok(repo) => {
            crate::open_repo_window(repo, cx);
            close(replace, window, cx);
        }
        Err(_) if dir.is_dir() => offer_init(dir.to_path_buf(), replace, window, cx),
        Err(err) => window.push_notification(
            gpui_kit::component::notification::Notification::error(format!("{err:#}")),
            cx,
        ),
    }
}

/// Close `replace`, which may be the window handling the event: that one
/// can't be updated through its handle.
fn close(replace: Option<AnyWindowHandle>, window: &mut Window, cx: &mut App) {
    match replace {
        Some(replace) if replace == window.window_handle() => window.remove_window(),
        Some(replace) => {
            replace.update(cx, |_, window, _| window.remove_window()).ok();
        }
        None => {}
    }
}

/// "<folder> is not a Git repository": create one there, or not.
fn offer_init(dir: PathBuf, replace: Option<AnyWindowHandle>, window: &mut Window, cx: &mut App) {
    window.open_dialog(cx, move |dialog, _, cx| {
        let dir = dir.clone();
        dialog.title(tr!("Not a Git Repository")).w(px(460.)).child(
            v_flex()
                .gap_4()
                .child(
                    div()
                        .text_sm()
                        .text_color(cx.theme().muted_foreground)
                        .child(tr!("{} is not inside a Git repository. Create one there?", tilde(&dir))),
                )
                .child(
                    h_flex()
                        .justify_end()
                        .gap_2()
                        .child(Button::new("init-cancel").label(tr!("Cancel")).on_click(|_, window, cx| window.close_dialog(cx)))
                        .child(Button::new("init-create").label(tr!("Create Repository")).primary().on_click(
                            move |_, window, cx| {
                                window.close_dialog(cx);
                                match Repo::init(&dir) {
                                    Ok(repo) => {
                                        crate::open_repo_window(repo, cx);
                                        close(replace, window, cx);
                                    }
                                    Err(err) => window.push_notification(
                                        gpui_kit::component::notification::Notification::error(format!("{err:#}")),
                                        cx,
                                    ),
                                }
                            },
                        )),
                ),
        )
    });
}

/// `path` with the home folder shown as `~`.
pub fn tilde(path: &Path) -> String {
    let text = path.display().to_string();
    match std::env::home_dir().map(|home| home.display().to_string()) {
        Some(home) if text.starts_with(&home) => format!("~{}", &text[home.len()..]),
        _ => text,
    }
}

struct Welcome;

impl Welcome {
    fn render_actions(&self, cx: &mut Context<Self>) -> impl IntoElement {
        v_flex()
            .flex_none()
            .w(px(280.))
            .h_full()
            .p_6()
            .gap_2()
            .bg(cx.theme().sidebar)
            .border_r_1()
            .border_color(cx.theme().border)
            .child(
                v_flex()
                    .items_center()
                    .gap_1()
                    .pt_4()
                    .pb_6()
                    .child(img("images/banyan.png").size(px(88.)))
                    .child(div().text_xl().font_semibold().child(tr!("Banyan")))
                    .child(
                        div()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(tr!("Version {}", env!("CARGO_PKG_VERSION"))),
                    ),
            )
            .child(
                Button::new("welcome-open")
                    .primary()
                    .w_full()
                    .icon(IconName::FolderOpen)
                    .label(tr!("Open Repository…"))
                    .on_click(|_, window, cx| crate::prompt_open_repo(Some(window.window_handle()), cx)),
            )
            .child(
                Button::new("welcome-clone")
                    .w_full()
                    .icon(IconName::Download)
                    .label(tr!("Clone Repository…"))
                    .on_click(|_, window, cx| crate::clone_dialog::open(Some(window.window_handle()), window, cx)),
            )
            .child(div().flex_1())
            .child(
                Button::new("welcome-settings")
                    .ghost()
                    .small()
                    .icon(IconName::Settings)
                    .label(tr!("Settings"))
                    .on_click(|_, _, cx| crate::settings_window::open(cx)),
            )
    }

    fn render_recent(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let recent = Settings::get().recent;
        let header = div()
            .px_3()
            .pb_2()
            .text_sm()
            .font_semibold()
            .text_color(cx.theme().muted_foreground)
            .child(tr!("Recent Repositories"));
        let body = if recent.is_empty() {
            v_flex()
                .flex_1()
                .items_center()
                .justify_center()
                .gap_3()
                .m_3()
                .border_2()
                .border_dashed()
                .border_color(cx.theme().border)
                .rounded(cx.theme().radius_lg)
                .text_color(cx.theme().muted_foreground)
                .child(Icon::new(IconName::FolderOpen).large())
                .child(div().text_sm().child(tr!("Drag a repository folder here to open it")))
                .child(div().text_xs().child(tr!("or choose Open Repository… or Clone Repository…")))
                .into_any_element()
        } else {
            v_flex()
                .id("recent-repositories")
                .flex_1()
                .min_h_0()
                .overflow_y_scroll()
                .children(recent.into_iter().enumerate().map(|(ix, path)| self.render_row(ix, path, cx)))
                .into_any_element()
        };
        v_flex()
            .flex_1()
            .min_w_0()
            .h_full()
            .pt_6()
            .px_4()
            .pb_3()
            .child(header)
            .child(body)
            .child(
                div()
                    .pt_2()
                    .px_3()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("Tip: drop a folder anywhere on this window to open it.")),
            )
    }

    fn render_row(&self, ix: usize, path: PathBuf, cx: &mut Context<Self>) -> AnyElement {
        let exists = path.is_dir();
        let name = path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_else(|| path.display().to_string());
        let target = path.clone();
        h_flex()
            .id(("recent", ix))
            .group("recent-row")
            .px_3()
            .py_2()
            .gap_3()
            .rounded(cx.theme().radius)
            .hover(|s| s.bg(cx.theme().list_hover))
            .when(exists, |row| row.cursor_pointer())
            .child(
                div()
                    .flex_none()
                    .size(px(32.))
                    .rounded(cx.theme().radius)
                    .flex()
                    .items_center()
                    .justify_center()
                    .bg(crate::ui::lane_color(ix).opacity(0.18))
                    .text_color(crate::ui::lane_color(ix))
                    .font_semibold()
                    .child(name.chars().next().map(|c| c.to_uppercase().to_string()).unwrap_or_default()),
            )
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .when(!exists, |col| col.opacity(0.5))
                    .child(div().text_sm().font_semibold().truncate().child(name))
                    .child(
                        div()
                            .text_xs()
                            .truncate()
                            .text_color(cx.theme().muted_foreground)
                            .child(if exists { tilde(&path) } else { tr!("{} (not found)", tilde(&path)) }),
                    ),
            )
            .child(
                div().invisible().group_hover("recent-row", |s| s.visible()).child(
                    Button::new(("remove-recent", ix))
                        .icon(IconName::Close)
                        .ghost()
                        .xsmall()
                        .tooltip(tr!("Remove from Recent"))
                        .on_click(cx.listener(move |_, _, _, cx| {
                            cx.stop_propagation();
                            Settings::remove_recent(&path);
                            cx.notify();
                        })),
                ),
            )
            .on_click(move |_, window, cx| {
                if exists {
                    open_path(&target, Some(window.window_handle()), window, cx);
                }
            })
            .into_any_element()
    }
}

impl Render for Welcome {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let dragging = cx.has_active_drag();
        div()
            .relative()
            .size_full()
            .bg(cx.theme().background)
            .text_color(cx.theme().foreground)
            .on_drop(|paths: &ExternalPaths, window, cx| {
                if let Some(path) = paths.paths().first() {
                    open_path(path, Some(window.window_handle()), window, cx);
                }
            })
            .child(h_flex().size_full().child(self.render_actions(cx)).child(self.render_recent(cx)))
            .when(dragging, |root| {
                root.child(
                    v_flex()
                        .absolute()
                        .inset_0()
                        .m_3()
                        .items_center()
                        .justify_center()
                        .gap_2()
                        .rounded(cx.theme().radius_lg)
                        .border_2()
                        .border_dashed()
                        .border_color(cx.theme().primary)
                        .bg(cx.theme().background.opacity(0.92))
                        .child(Icon::new(IconName::FolderOpen).large().text_color(cx.theme().primary))
                        .child(div().text_lg().font_semibold().child(tr!("Drop to open the repository"))),
                )
            })
    }
}
