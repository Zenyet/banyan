//! Diff preview shared by the Log and Commit views.

use gpui_kit::component::{
    ActiveTheme as _, Sizable as _,
    button::{Button, ButtonVariants as _},
    diff::{Diff, DiffFile, DiffLinePosition, DiffMode, DiffSide, DiffState},
    h_flex, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use std::sync::Arc;

use crate::settings::Settings;

/// Makes the patch shown, again whenever a diff option changes.
type Loader = Arc<dyn Fn() -> anyhow::Result<String> + Send + Sync>;

pub struct DiffPane {
    state: Entity<DiffState>,
    title: Option<SharedString>,
    message: Option<SharedString>,
    mode: DiffMode,
    /// What [`Self::load`] last showed, and the whitespace option it used.
    loader: Option<(Loader, bool)>,
    _load: Option<Task<()>>,
    _relayout: Option<Task<()>>,
    _observe_state: Subscription,
}

impl DiffPane {
    pub fn new(cx: &mut Context<Self>) -> Self {
        let mode = preferred_mode();
        let state = cx.new(|cx| DiffState::new(Vec::new(), cx).with_mode(mode));
        Self {
            // The pane is shown as a cached view, so repaint it whenever the
            // diff changes, such as when its highlighting finishes.
            _observe_state: cx.observe(&state, |_, _, cx| cx.notify()),
            state,
            title: None,
            message: None,
            mode,
            loader: None,
            _load: None,
            _relayout: None,
        }
    }

    /// Show the patch `load` makes, off the main thread. It runs again when
    /// the whitespace option changes.
    pub fn load(
        &mut self,
        title: impl Into<SharedString>,
        load: impl Fn() -> anyhow::Result<String> + Send + Sync + 'static,
        cx: &mut Context<Self>,
    ) {
        self.title = Some(title.into());
        self.run_loader(Arc::new(load), cx);
    }

    fn run_loader(&mut self, loader: Loader, cx: &mut Context<Self>) {
        let whitespace = Settings::read(|s| s.ignore_whitespace);
        self.loader = Some((loader.clone(), whitespace));
        let title = self.title.clone().unwrap_or_default();
        self._load = Some(cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { loader() }).await;
            this.update(cx, |this, cx| match result {
                Ok(patch) => this.set_patch(&patch, cx),
                Err(err) => this.set_message(Some(title), format!("{err:#}"), cx),
            })
            .ok();
        }));
    }

    fn set_patch(&mut self, patch: &str, cx: &mut Context<Self>) {
        match DiffFile::parse(patch) {
            Ok(files) if files.is_empty() => {
                self.message = Some("No differences".into());
                self.state.update(cx, |state, cx| state.set_files(Vec::new(), cx));
            }
            Ok(files) => {
                self.message = None;
                self.state.update(cx, |state, cx| state.set_files(files, cx));
                self.relayout_after_resize(cx);
            }
            Err(err) => {
                self.message = Some(format!("Cannot show diff: {err:?}").into());
                self.state.update(cx, |state, cx| state.set_files(Vec::new(), cx));
            }
        }
        cx.notify();
    }

    /// The diff sizes its columns from the width it was last painted at. When
    /// it first appears inside a growing container (a dialog's entrance, a
    /// window settling at launch) that width is stale, so lay it out again
    /// once the container has its final size.
    fn relayout_after_resize(&mut self, cx: &mut Context<Self>) {
        let state = self.state.downgrade();
        self._relayout = Some(cx.spawn(async move |_, cx| {
            for delay in [16, 400] {
                cx.background_executor().timer(std::time::Duration::from_millis(delay)).await;
                if state.update(cx, |_, cx| cx.notify()).is_err() {
                    return;
                }
            }
        }));
    }

    pub fn show_message(&mut self, title: Option<SharedString>, message: impl Into<SharedString>, cx: &mut Context<Self>) {
        self.loader = None;
        self._load = None;
        self.set_message(title, message, cx);
    }

    fn set_message(&mut self, title: Option<SharedString>, message: impl Into<SharedString>, cx: &mut Context<Self>) {
        self.title = title;
        self.message = Some(message.into());
        self.state.update(cx, |state, cx| state.set_files(Vec::new(), cx));
        cx.notify();
    }

    /// Scroll to a line of the new version of the file shown.
    pub fn scroll_to_line(&mut self, line: usize, cx: &mut Context<Self>) {
        self.state.update(cx, |state, cx| {
            let Some(path) = state.files().first().map(|f| f.path().clone()) else { return };
            state.scroll_to_line(DiffLinePosition::new(path, DiffSide::Modified, line), cx);
        });
    }

    pub fn clear(&mut self, cx: &mut Context<Self>) {
        self.show_message(None, "Select a file to see its changes", cx);
    }

    /// Remember `mode` for every diff, as the setting it is.
    fn set_mode(&mut self, mode: DiffMode, cx: &mut Context<Self>) {
        crate::settings_window::change(cx, |settings| settings.unified_diff = mode == DiffMode::Unified);
    }
}

fn preferred_mode() -> DiffMode {
    if Settings::read(|s| s.unified_diff) { DiffMode::Unified } else { DiffMode::Split }
}

impl Render for DiffPane {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let mode = preferred_mode();
        if mode != self.mode {
            self.mode = mode;
            self.state.update(cx, |state, cx| state.set_mode(mode, cx));
        }
        let ignore_whitespace = Settings::read(|s| s.ignore_whitespace);
        if let Some((loader, used)) = self.loader.clone()
            && used != ignore_whitespace
        {
            self.run_loader(loader, cx);
        }
        let toolbar = h_flex()
            .flex_none()
            .h(px(32.))
            .px_2()
            .gap_1()
            .border_b_1()
            .border_color(cx.theme().border)
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .text_sm()
                    .child(self.title.clone().unwrap_or_else(|| "Diff".into())),
            )
            .child(
                Button::new("diff-prev")
                    .icon(IconName::ArrowUp)
                    .ghost()
                    .xsmall()
                    .tooltip("Previous Change")
                    .on_click(cx.listener(|this, _, _, cx| {
                        this.state.update(cx, |state, cx| state.previous_change(cx))
                    })),
            )
            .child(
                Button::new("diff-next")
                    .icon(IconName::ArrowDown)
                    .ghost()
                    .xsmall()
                    .tooltip("Next Change")
                    .on_click(cx.listener(|this, _, _, cx| {
                        this.state.update(cx, |state, cx| state.next_change(cx))
                    })),
            )
            .child(
                Button::new("diff-whitespace")
                    .label("Ignore Whitespace")
                    // Toggled ghost buttons hardly show it in the dark theme.
                    .when(ignore_whitespace, |button| button.icon(IconName::Check))
                    .ghost()
                    .xsmall()
                    .toggled(ignore_whitespace)
                    .tooltip("Leave out changes that only touch whitespace")
                    .on_click(|_, _, cx| {
                        crate::settings_window::change(cx, |s| s.ignore_whitespace = !s.ignore_whitespace)
                    }),
            )
            .child(
                Button::new("diff-split")
                    .label("Side-by-side")
                    .ghost()
                    .xsmall()
                    .toggled(mode == DiffMode::Split)
                    .on_click(cx.listener(|this, _, _, cx| this.set_mode(DiffMode::Split, cx))),
            )
            .child(
                Button::new("diff-unified")
                    .label("Unified")
                    .ghost()
                    .xsmall()
                    .toggled(mode == DiffMode::Unified)
                    .on_click(cx.listener(|this, _, _, cx| this.set_mode(DiffMode::Unified, cx))),
            );

        v_flex()
            .size_full()
            .bg(cx.theme().background)
            .child(toolbar)
            .child(match &self.message {
                Some(message) => div()
                    .flex_1()
                    .flex()
                    .items_center()
                    .justify_center()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child(message.clone())
                    .into_any_element(),
                None => div()
                    .flex_1()
                    .min_h_0()
                    .child(Diff::new(&self.state).header_visible(false).size_full())
                    .into_any_element(),
            })
    }
}
