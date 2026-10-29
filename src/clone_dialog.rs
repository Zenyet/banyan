//! "Clone Repository…": URL and destination, then open the clone.

use std::path::PathBuf;

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{Input, InputEvent, InputState},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::Repo;
use crate::settings::Settings;

/// `replace` is closed once the clone opens (the welcome window).
pub fn open(replace: Option<AnyWindowHandle>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| CloneDialog::new(replace, window, cx));
    let focus = view.read(cx).url.clone();
    let needs_focus = std::cell::Cell::new(true);
    window.open_dialog(cx, move |dialog, window, cx| {
        if needs_focus.replace(false) {
            focus.focus_handle(cx).focus(window, cx);
        }
        dialog.title("Clone Repository").w(px(560.)).child(view.clone())
    });
}

struct CloneDialog {
    url: Entity<InputState>,
    dir: Entity<InputState>,
    /// The user typed a directory; stop deriving it from the URL.
    dir_edited: bool,
    /// Set while we fill the directory in ourselves.
    updating_dir: bool,
    cloning: bool,
    error: Option<SharedString>,
    replace: Option<AnyWindowHandle>,
    _subscriptions: Vec<Subscription>,
}

/// Where new clones go: next to the most recently opened repository.
fn default_parent() -> PathBuf {
    Settings::get()
        .recent
        .first()
        .and_then(|repo| repo.parent().map(PathBuf::from))
        .or_else(std::env::home_dir)
        .unwrap_or_else(|| PathBuf::from("."))
}

impl CloneDialog {
    fn new(replace: Option<AnyWindowHandle>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let url = cx.new(|cx| InputState::new(window, cx).placeholder("https://github.com/owner/repo.git"));
        let dir = cx.new(|cx| {
            InputState::new(window, cx).default_value(default_parent().display().to_string())
        });
        let subscriptions = vec![
            cx.subscribe_in(&url, window, |this, url, event, window, cx| match event {
                InputEvent::Change if !this.dir_edited => {
                    let name = Repo::clone_dir_name(&url.read(cx).value());
                    let path = default_parent().join(name);
                    this.set_dir(path, window, cx);
                }
                InputEvent::PressEnter { .. } => this.clone_repo(window, cx),
                _ => {}
            }),
            cx.subscribe_in(&dir, window, |this, _, event, window, cx| match event {
                InputEvent::Change if !this.updating_dir => this.dir_edited = true,
                InputEvent::PressEnter { .. } => this.clone_repo(window, cx),
                _ => {}
            }),
        ];
        Self {
            url,
            dir,
            dir_edited: false,
            updating_dir: false,
            cloning: false,
            error: None,
            replace,
            _subscriptions: subscriptions,
        }
    }

    fn set_dir(&mut self, path: PathBuf, window: &mut Window, cx: &mut Context<Self>) {
        self.updating_dir = true;
        self.dir
            .update(cx, |dir, cx| dir.set_value(path.display().to_string(), window, cx));
        self.updating_dir = false;
    }

    fn browse(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let paths = cx.prompt_for_paths(PathPromptOptions {
            files: false,
            directories: true,
            multiple: false,
            prompt: Some("Clone Into".into()),
        });
        cx.spawn_in(window, async move |this, cx| {
            let Ok(Ok(Some(paths))) = paths.await else { return };
            let Some(parent) = paths.into_iter().next() else { return };
            this.update_in(cx, |this, window, cx| {
                let name = Repo::clone_dir_name(&this.url.read(cx).value());
                this.dir_edited = true;
                this.set_dir(parent.join(name), window, cx);
            })
            .ok();
        })
        .detach();
    }

    fn clone_repo(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let url = self.url.read(cx).value().trim().to_string();
        let dest = PathBuf::from(self.dir.read(cx).value().trim().to_string());
        if url.is_empty() || self.cloning {
            return;
        }
        if dest.exists() && dest.read_dir().is_ok_and(|mut entries| entries.next().is_some()) {
            self.error = Some(format!("{} already exists and is not empty", dest.display()).into());
            cx.notify();
            return;
        }
        self.cloning = true;
        self.error = None;
        cx.notify();
        let handle = window.window_handle();
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { Repo::clone_from(&url, &dest) })
                .await;
            this.update(cx, |this, cx| {
                this.cloning = false;
                match result {
                    Ok(repo) => {
                        let replace = this.replace;
                        cx.defer(move |cx| {
                            handle.update(cx, |_, window, cx| window.close_dialog(cx)).ok();
                            crate::open_repo_window(repo, cx);
                            if let Some(window) = replace {
                                window.update(cx, |_, window, _| window.remove_window()).ok();
                            }
                        });
                    }
                    Err(err) => this.error = Some(format!("{err:#}").into()),
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }
}

impl Render for CloneDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let label = |text: &'static str| div().text_sm().text_color(cx.theme().muted_foreground).child(text);
        v_flex()
            .gap_3()
            .child(label("URL"))
            .child(Input::new(&self.url))
            .child(label("Directory"))
            .child(
                h_flex()
                    .gap_2()
                    .child(div().flex_1().child(Input::new(&self.dir)))
                    .child(
                        Button::new("clone-browse")
                            .label("Browse…")
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.browse(window, cx))),
                    ),
            )
            .when_some(self.error.clone(), |col, error| {
                col.child(div().text_sm().text_color(cx.theme().danger).child(error))
            })
            .child(
                h_flex()
                    .gap_2()
                    .justify_end()
                    .when(self.cloning, |row| {
                        row.child(div().text_sm().text_color(cx.theme().muted_foreground).child("Cloning…"))
                    })
                    .child(
                        Button::new("clone-cancel")
                            .label("Cancel")
                            .small()
                            .on_click(|_, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("clone-confirm")
                            .label("Clone")
                            .primary()
                            .small()
                            .disabled(self.cloning)
                            .on_click(cx.listener(|this, _, window, cx| this.clone_repo(window, cx))),
                    ),
            )
    }
}
