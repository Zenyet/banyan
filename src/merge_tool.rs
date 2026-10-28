//! Conflict resolution dialog, like JetBrains' "Merge Revisions" window:
//! pick yours / theirs / both per conflict, then apply and mark resolved.

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    diff::{Diff, DiffConflictResolution, DiffEvent, DiffFile, DiffMode, DiffState},
    h_flex, v_flex,
};
use gpui_kit::*;

use crate::model::{RepoEvent, RepoModel};

pub fn open(model: Entity<RepoModel>, path: String, window: &mut Window, cx: &mut App) {
    let full_path = model.read(cx).repo.root().join(&path);
    let text = match std::fs::read_to_string(&full_path) {
        Ok(text) => text,
        Err(err) => {
            model.update(cx, |_, cx| {
                cx.emit(RepoEvent::OpFailed(format!("Cannot read {path}: {err}").into()))
            });
            return;
        }
    };
    let file = match DiffFile::parse_conflicts(path.clone(), &text) {
        Ok(file) if !file.conflicts().is_empty() => file,
        _ => {
            model.update(cx, |_, cx| {
                cx.emit(RepoEvent::OpFailed(
                    format!("{path} has no conflict markers. Use Accept Yours / Theirs or Mark as Resolved.").into(),
                ))
            });
            return;
        }
    };
    let tool = cx.new(|cx| MergeTool::new(model, path, file, cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title("Merge Revisions")
            .w(px(1100.))
            .child(tool.clone())
    });
}

struct MergeTool {
    model: Entity<RepoModel>,
    path: String,
    state: Entity<DiffState>,
    _subscription: Subscription,
}

impl MergeTool {
    fn new(model: Entity<RepoModel>, path: String, file: DiffFile, cx: &mut Context<Self>) -> Self {
        let state = cx.new(|cx| DiffState::new([file], cx).with_mode(DiffMode::Unified));
        let subscription = cx.subscribe(&state, |_, _, event, cx| {
            if let DiffEvent::ConflictResolved(..) = event {
                cx.notify();
            }
        });
        Self {
            model,
            path,
            state,
            _subscription: subscription,
        }
    }

    fn counts(&self, cx: &App) -> (usize, usize) {
        let state = self.state.read(cx);
        let total = state.files().first().map(|f| f.conflicts().len()).unwrap_or(0);
        let resolved = (0..total)
            .filter(|&ix| state.conflict_resolution(&self.path, ix).is_some())
            .count();
        (resolved, total)
    }

    fn resolve_all(&mut self, resolution: DiffConflictResolution, cx: &mut Context<Self>) {
        let (_, total) = self.counts(cx);
        let path = self.path.clone();
        self.state.update(cx, |state, cx| {
            for ix in 0..total {
                state.resolve_conflict(&path, ix, Some(resolution), cx);
            }
        });
        cx.notify();
    }

    fn apply(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(text) = self.state.read(cx).resolved_text(&self.path) else {
            return;
        };
        let path = self.path.clone();
        window.close_dialog(cx);
        self.model.update(cx, |model, cx| {
            model.run(
                format!("Resolve {path}"),
                move |repo| {
                    std::fs::write(repo.root().join(&path), text)?;
                    repo.resolve_conflict(&path, None)
                },
                cx,
            )
        });
    }
}

impl Render for MergeTool {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let (resolved, total) = self.counts(cx);
        let done = resolved == total;
        v_flex()
            .gap_3()
            .child(
                h_flex()
                    .gap_2()
                    .text_sm()
                    .child(div().flex_1().truncate().child(self.path.clone()))
                    .child(
                        div()
                            .text_color(if done { cx.theme().success } else { cx.theme().muted_foreground })
                            .child(format!("{resolved} of {total} conflicts resolved")),
                    ),
            )
            .child(
                div()
                    .h(px(560.))
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .overflow_hidden()
                    .child(Diff::new(&self.state).header_visible(false).size_full()),
            )
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Button::new("accept-yours")
                            .label("Accept Yours")
                            .small()
                            .on_click(cx.listener(|this, _, _, cx| {
                                this.resolve_all(DiffConflictResolution::Current, cx)
                            })),
                    )
                    .child(
                        Button::new("accept-theirs")
                            .label("Accept Theirs")
                            .small()
                            .on_click(cx.listener(|this, _, _, cx| {
                                this.resolve_all(DiffConflictResolution::Incoming, cx)
                            })),
                    )
                    .child(div().flex_1())
                    .child(
                        Button::new("merge-cancel")
                            .label("Cancel")
                            .small()
                            .on_click(|_, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("merge-apply")
                            .label("Apply")
                            .primary()
                            .small()
                            .disabled(!done)
                            .on_click(cx.listener(|this, _, window, cx| this.apply(window, cx))),
                    ),
            )
    }
}
