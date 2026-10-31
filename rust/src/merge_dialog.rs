//! "Merge with Options": fast-forwarding, squashing, stopping before the
//! commit and the commit's message, like JetBrains' Merge dialog.

use gpui_kit::component::{
    ActiveTheme as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex,
    input::{Input, InputState},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::{FastForward, MergeOptions};
use crate::i18n::tr;
use crate::model::RepoModel;

/// Merge `branch` into the current branch.
pub fn open(model: Entity<RepoModel>, branch: String, window: &mut Window, cx: &mut App) {
    let current = model.read(cx).current_branch.clone().unwrap_or_else(|| "HEAD".into());
    let view = cx.new(|cx| MergeDialog::new(model, branch.clone(), window, cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog.title(tr!("Merge '{}' into '{}'", branch, current)).w(px(480.)).child(view.clone())
    });
}

struct MergeDialog {
    model: Entity<RepoModel>,
    branch: String,
    fast_forward: FastForward,
    squash: bool,
    no_commit: bool,
    message: Entity<InputState>,
}

impl MergeDialog {
    fn new(model: Entity<RepoModel>, branch: String, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let message = cx.new(|cx| InputState::new(window, cx).placeholder(tr!("Git's message: Merge branch '{}'", branch)));
        Self { model, branch, fast_forward: FastForward::Allow, squash: false, no_commit: false, message }
    }

    fn options(&self, cx: &App) -> MergeOptions {
        let message = self.message.read(cx).value().trim().to_string();
        MergeOptions {
            // Squashing never fast-forwards or commits, and git refuses --no-ff with it.
            fast_forward: if self.squash { FastForward::Allow } else { self.fast_forward },
            squash: self.squash,
            no_commit: self.no_commit && !self.squash,
            message: (!message.is_empty() && !self.squash && !self.no_commit).then_some(message),
        }
    }

    fn confirm(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let options = self.options(cx);
        let branch = self.branch.clone();
        window.close_dialog(cx);
        self.model.update(cx, |m, cx| m.run(tr!("Merge {}", branch), move |repo| repo.merge_with(&branch, &options), cx));
    }

    fn fast_forward_choice(&self, value: FastForward, label: &'static str, cx: &mut Context<Self>) -> Button {
        let button = Button::new(label)
            .label(label)
            .xsmall()
            .on_click(cx.listener(move |this, _, _, cx| {
                this.fast_forward = value;
                cx.notify();
            }));
        if self.fast_forward == value { button.primary() } else { button.ghost() }
    }
}

impl Render for MergeDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let note = |text: &'static str, cx: &App| div().pl(px(24.)).text_xs().text_color(cx.theme().muted_foreground).child(text);
        let commits = !self.squash && !self.no_commit;
        v_flex()
            .gap_3()
            .text_sm()
            .when(!self.squash, |col| {
                col.child(
                    v_flex()
                        .gap_1()
                        .child(div().child(tr!("Fast-forward")))
                        .child(
                            h_flex()
                                .gap_1()
                                .child(self.fast_forward_choice(FastForward::Allow, tr!("When possible"), cx))
                                .child(self.fast_forward_choice(FastForward::Only, tr!("Only fast-forward"), cx))
                                .child(self.fast_forward_choice(FastForward::Never, tr!("Always create a merge commit"), cx)),
                        ),
                )
            })
            .child(
                v_flex()
                    .gap_1()
                    .child(
                        Checkbox::new("merge-squash")
                            .label(tr!("Squash commits"))
                            .checked(self.squash)
                            .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                this.squash = *checked;
                                cx.notify();
                            })),
                    )
                    .child(note(tr!("Brings the changes in as one change to commit, without a merge commit"), cx)),
            )
            .when(!self.squash, |col| {
                col.child(
                    v_flex()
                        .gap_1()
                        .child(
                            Checkbox::new("merge-no-commit")
                                .label(tr!("Don't commit"))
                                .checked(self.no_commit)
                                .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                    this.no_commit = *checked;
                                    cx.notify();
                                })),
                        )
                        .child(note(tr!("Stops before the merge commit, to look at or change the result first"), cx)),
                )
            })
            .when(commits, |col| {
                col.child(v_flex().gap_1().child(div().child(tr!("Commit message"))).child(Input::new(&self.message)))
            })
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(Button::new("merge-cancel").label(tr!("Cancel")).small().on_click(|_, window, cx| window.close_dialog(cx)))
                    .child(
                        Button::new("merge-ok")
                            .label(tr!("Merge"))
                            .primary()
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.confirm(window, cx))),
                    ),
            )
    }
}
