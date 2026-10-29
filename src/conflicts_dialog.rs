//! What a stopped merge, rebase, cherry-pick or revert needs from the user:
//! the banner above the tabs, and JetBrains' "Conflicts" dialog listing the
//! conflicted files with Accept Yours / Accept Theirs / Merge… for each.

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Icon, Sizable as _, StyledExt as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::StatusEntry;
use crate::model::RepoModel;
use crate::ui::{confirm, selection_bg};

pub fn open(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| ConflictsDialog::new(model, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title("Conflicts").w(px(760.)).child(view.clone()))
}

/// Ask, then abort the stopped operation.
pub fn abort(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let Some(operation) = model.read(cx).operation.clone() else { return };
    let state = operation.state;
    let noun = state.noun().to_lowercase();
    confirm(
        format!("Abort {}", state.noun()),
        format!(
            "{}. Abort it and go back to how things were before the {noun} started? \
             Conflicts resolved so far are lost.",
            operation.title
        ),
        format!("Abort {}", state.noun()),
        move |_, cx| {
            model.update(cx, |m, cx| m.run(format!("Abort {noun}"), move |repo| repo.abort(state), cx))
        },
        window,
        cx,
    );
}

/// Commit the merge, or continue the rebase, cherry-pick or revert.
pub fn proceed(model: &Entity<RepoModel>, cx: &mut App) {
    let state = model.read(cx).state;
    model.update(cx, |m, cx| m.run(state.continue_label(), move |repo| repo.continue_op(state), cx));
}

/// The bar above the tabs while an operation is stopped.
pub fn render_banner(model: &Entity<RepoModel>, cx: &mut App) -> Option<AnyElement> {
    let state = model.read(cx);
    let operation = state.operation.clone()?;
    let conflicts = state.conflicts().count();
    let busy = state.busy.is_some();
    let theme = cx.theme();
    let (icon, accent) = if conflicts > 0 {
        (IconName::TriangleAlert, theme.warning)
    } else {
        (IconName::CircleCheck, theme.success)
    };
    let detail = match conflicts {
        0 => "All conflicts resolved".to_string(),
        1 => "1 file has conflicts".to_string(),
        n => format!("{n} files have conflicts"),
    };
    let resolve_model = model.clone();
    let proceed_model = model.clone();
    let abort_model = model.clone();
    Some(
        h_flex()
            .h(px(36.))
            .px_3()
            .gap_2()
            .text_sm()
            .bg(accent.opacity(0.12))
            .border_b_1()
            .border_color(accent.opacity(0.4))
            .child(Icon::new(icon).small().text_color(accent))
            .child(div().flex_none().font_semibold().child(operation.title))
            .child(div().min_w_0().truncate().text_color(theme.muted_foreground).child(detail))
            .child(div().flex_1())
            .map(|row| {
                if conflicts > 0 {
                    row.child(
                        Button::new("banner-resolve")
                            .label("Resolve Conflicts…")
                            .primary()
                            .small()
                            .on_click(move |_, window, cx| open(resolve_model.clone(), window, cx)),
                    )
                } else {
                    row.child(
                        Button::new("banner-continue")
                            .label(operation.state.continue_label())
                            .primary()
                            .small()
                            .disabled(busy)
                            .on_click(move |_, _, cx| proceed(&proceed_model, cx)),
                    )
                }
            })
            .child(
                Button::new("banner-abort")
                    .label(format!("Abort {}", operation.state.noun()))
                    .danger()
                    .small()
                    .disabled(busy)
                    .on_click(move |_, window, cx| abort(abort_model.clone(), window, cx)),
            )
            .into_any_element(),
    )
}

struct ConflictsDialog {
    model: Entity<RepoModel>,
    selected: Option<String>,
    _observe: Subscription,
}

impl ConflictsDialog {
    fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        Self {
            _observe: cx.observe(&model, |_, _, cx| cx.notify()),
            model,
            selected: None,
        }
    }

    fn resolve(&self, path: String, ours: bool, cx: &mut Context<Self>) {
        let label = format!("Accept {} for {path}", if ours { "yours" } else { "theirs" });
        self.model
            .update(cx, |m, cx| m.run(label, move |repo| repo.resolve_conflict(&path, Some(ours)), cx));
    }

    fn render_row(&self, entry: &StatusEntry, selected: bool, cx: &mut Context<Self>) -> AnyElement {
        let path = entry.path.clone();
        let (ours, theirs) = entry.conflict_sides().map(|(o, t)| (o.label(), t.label())).unwrap_or(("", ""));
        let mergeable = entry.can_merge_text();
        h_flex()
            .id(SharedString::from(format!("conflict-{path}")))
            .h(px(26.))
            .px_2()
            .gap_2()
            .text_sm()
            .when(selected, |row| row.bg(selection_bg()))
            .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
            .child(Icon::new(IconName::File).xsmall().text_color(cx.theme().danger))
            .child(div().flex_1().min_w_0().truncate().child(path.clone()))
            .child(div().flex_none().w(px(130.)).text_color(cx.theme().muted_foreground).child(ours))
            .child(div().flex_none().w(px(130.)).text_color(cx.theme().muted_foreground).child(theirs))
            .on_click(cx.listener(move |this, event: &ClickEvent, window, cx| {
                this.selected = Some(path.clone());
                cx.notify();
                if mergeable && event.click_count() >= 2 {
                    crate::merge_tool::open(this.model.clone(), path.clone(), window, cx);
                }
            }))
            .into_any_element()
    }
}

impl Render for ConflictsDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let (operation, conflicts, busy) = {
            let model = self.model.read(cx);
            (model.operation.clone(), model.conflicts().cloned().collect::<Vec<_>>(), model.busy.is_some())
        };
        if !self.selected.as_ref().is_some_and(|p| conflicts.iter().any(|e| &e.path == p)) {
            self.selected = conflicts.first().map(|e| e.path.clone());
        }
        let selected = conflicts.iter().find(|e| Some(&e.path) == self.selected.as_ref()).cloned();
        let model = self.model.clone();
        let footer = h_flex()
            .gap_2()
            .when_some(operation.clone(), |row, operation| {
                let model = model.clone();
                row.child(
                    Button::new("conflicts-abort")
                        .label(format!("Abort {}", operation.state.noun()))
                        .danger()
                        .small()
                        .disabled(busy)
                        .on_click(move |_, window, cx| abort(model.clone(), window, cx)),
                )
            })
            .child(div().flex_1())
            .child(
                Button::new("conflicts-close")
                    .label("Close")
                    .small()
                    .on_click(|_, window, cx| window.close_dialog(cx)),
            );

        let intro = operation
            .as_ref()
            .map(|op| op.title.clone())
            .unwrap_or_else(|| "Conflicts".into());

        if conflicts.is_empty() {
            let footer = footer.when_some(operation.clone(), |row, operation| {
                let model = model.clone();
                row.child(
                    Button::new("conflicts-continue")
                        .label(operation.state.continue_label())
                        .primary()
                        .small()
                        .disabled(busy)
                        .on_click(move |_, window, cx| {
                            window.close_dialog(cx);
                            proceed(&model, cx);
                        }),
                )
            });
            return v_flex()
                .gap_3()
                .child(
                    h_flex()
                        .gap_2()
                        .text_sm()
                        .child(Icon::new(IconName::CircleCheck).text_color(cx.theme().success))
                        .child(format!("All conflicts are resolved. {intro}.")),
                )
                .when(operation.is_some(), |col| {
                    col.child(
                        div()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child("Review the result in the Commit tab, then finish the operation."),
                    )
                })
                .child(footer)
                .into_any_element();
        }

        let (ours_label, theirs_label) = operation
            .as_ref()
            .map(|op| (op.ours.clone(), op.theirs.clone()))
            .unwrap_or_else(|| ("yours".into(), "theirs".into()));
        let rows: Vec<AnyElement> = conflicts
            .iter()
            .map(|entry| self.render_row(entry, Some(&entry.path) == self.selected.as_ref(), cx))
            .collect();
        let selected_path = selected.as_ref().map(|e| e.path.clone());
        let mergeable = selected.as_ref().is_some_and(|e| e.can_merge_text());
        let side_button = |id: &'static str, label: &'static str| {
            Button::new(id).label(label).small().w_full().disabled(selected_path.is_none() || busy)
        };

        v_flex()
            .gap_3()
            .child(div().text_sm().child(format!("{intro}. Resolve each file to continue:")))
            .child(
                h_flex()
                    .items_start()
                    .gap_3()
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .border_1()
                            .border_color(cx.theme().border)
                            .rounded(cx.theme().radius)
                            .child(
                                h_flex()
                                    .h(px(26.))
                                    .px_2()
                                    .gap_2()
                                    .text_xs()
                                    .text_color(cx.theme().muted_foreground)
                                    .border_b_1()
                                    .border_color(cx.theme().border)
                                    .child(div().w(px(12.)))
                                    .child(div().flex_1().child("Name"))
                                    .child(div().flex_none().w(px(130.)).truncate().child(format!("Yours ({ours_label})")))
                                    .child(div().flex_none().w(px(130.)).truncate().child(format!("Theirs ({theirs_label})"))),
                            )
                            .child(
                                v_flex()
                                    .id("conflict-files")
                                    .h(px(220.))
                                    .overflow_y_scroll()
                                    .py_1()
                                    .children(rows),
                            ),
                    )
                    .child(
                        v_flex()
                            .flex_none()
                            .w(px(130.))
                            .gap_2()
                            .child(side_button("conflicts-yours", "Accept Yours").on_click(cx.listener({
                                let path = selected_path.clone();
                                move |this, _, _, cx| {
                                    if let Some(path) = path.clone() {
                                        this.resolve(path, true, cx)
                                    }
                                }
                            })))
                            .child(side_button("conflicts-theirs", "Accept Theirs").on_click(cx.listener({
                                let path = selected_path.clone();
                                move |this, _, _, cx| {
                                    if let Some(path) = path.clone() {
                                        this.resolve(path, false, cx)
                                    }
                                }
                            })))
                            .child(
                                side_button("conflicts-merge", "Merge…")
                                    .primary()
                                    .disabled(!mergeable || busy)
                                    .on_click(cx.listener({
                                        let path = selected_path.clone();
                                        move |this, _, window, cx| {
                                            if let Some(path) = path.clone() {
                                                crate::merge_tool::open(this.model.clone(), path, window, cx)
                                            }
                                        }
                                    })),
                            ),
                    ),
            )
            .when(!mergeable && selected.is_some(), |col| {
                col.child(
                    div()
                        .text_xs()
                        .text_color(cx.theme().muted_foreground)
                        .child("One side deleted this file, so it can only be resolved by accepting a side."),
                )
            })
            .child(footer)
            .into_any_element()
    }
}
