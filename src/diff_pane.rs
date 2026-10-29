//! Diff preview shared by the Log and Commit views.

use gpui_kit::component::{
    ActiveTheme as _, Sizable as _,
    button::{Button, ButtonVariants as _},
    diff::{Diff, DiffFile, DiffLinePosition, DiffMode, DiffSide, DiffState},
    h_flex, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::*;

use crate::settings::Settings;

pub struct DiffPane {
    state: Entity<DiffState>,
    title: Option<SharedString>,
    message: Option<SharedString>,
    mode: DiffMode,
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
            _relayout: None,
        }
    }

    pub fn show_patch(&mut self, title: impl Into<SharedString>, patch: &str, cx: &mut Context<Self>) {
        self.title = Some(title.into());
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
