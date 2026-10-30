//! "Stash Changes": the message and what to put aside, like JetBrains'
//! stash dialog.

use gpui_kit::component::{
    ActiveTheme as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex,
    input::{Input, InputEvent, InputState},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::StashOptions;
use crate::i18n::tr;
use crate::model::RepoModel;

/// `checked` are the files checked in the Commit tab, which the stash can
/// be limited to.
pub fn open(model: Entity<RepoModel>, checked: Vec<String>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| StashDialog::new(model, checked, window, cx));
    let focus = view.read(cx).message.focus_handle(cx);
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Stash Changes")).w(px(480.)).child(view.clone()));
    window.focus(&focus, cx);
}

struct StashDialog {
    model: Entity<RepoModel>,
    message: Entity<InputState>,
    checked: Vec<String>,
    only_checked: bool,
    keep_index: bool,
    include_untracked: bool,
}

impl StashDialog {
    fn new(model: Entity<RepoModel>, checked: Vec<String>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let message = cx.new(|cx| {
            InputState::new(window, cx).placeholder(tr!("Message (optional)")).default_value("WIP")
        });
        cx.subscribe_in(&message, window, |_, _, event: &InputEvent, window, cx| {
            if let InputEvent::PressEnter { .. } = event {
                // The input is still handling the key; confirm once it's done.
                cx.defer_in(window, |this, window, cx| this.confirm(window, cx));
            }
        })
        .detach();
        Self { model, message, checked, only_checked: false, keep_index: false, include_untracked: true }
    }

    fn confirm(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let options = StashOptions {
            message: self.message.read(cx).value().trim().to_string(),
            keep_index: self.keep_index,
            include_untracked: self.include_untracked,
            paths: if self.only_checked { self.checked.clone() } else { Vec::new() },
        };
        self.model.update(cx, |m, cx| m.run(tr!("Stash"), move |repo| repo.stash(&options), cx));
        window.close_dialog(cx);
    }
}

impl Render for StashDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let note = |text: String, cx: &App| div().pl(px(24.)).text_xs().text_color(cx.theme().muted_foreground).child(text);
        let checked = self.checked.len();
        v_flex()
            .gap_3()
            .text_sm()
            .child(v_flex().gap_1().child(div().child(tr!("Message"))).child(Input::new(&self.message)))
            .when(checked > 0, |col| {
                col.child(
                    v_flex()
                        .gap_1()
                        .child(
                            Checkbox::new("stash-only-checked")
                                .label(tr!("Only the checked files ({})", checked))
                                .checked(self.only_checked)
                                .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                    this.only_checked = *checked;
                                    cx.notify();
                                })),
                        )
                        .child(note(tr!("Otherwise every local change is stashed").into(), cx)),
                )
            })
            .child(
                Checkbox::new("stash-untracked")
                    .label(tr!("Include unversioned files"))
                    .checked(self.include_untracked)
                    .on_click(cx.listener(|this, checked: &bool, _, cx| {
                        this.include_untracked = *checked;
                        cx.notify();
                    })),
            )
            .child(
                v_flex()
                    .gap_1()
                    .child(
                        Checkbox::new("stash-keep-index")
                            .label(tr!("Keep index"))
                            .checked(self.keep_index)
                            .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                this.keep_index = *checked;
                                cx.notify();
                            })),
                    )
                    .child(note(tr!("Staged changes stay in the work tree as well as going into the stash").into(), cx)),
            )
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(Button::new("stash-cancel").label(tr!("Cancel")).on_click(|_, window, cx| window.close_dialog(cx)))
                    .child(
                        Button::new("stash-ok")
                            .label(tr!("Create Stash"))
                            .primary()
                            .on_click(cx.listener(|this, _, window, cx| this.confirm(window, cx))),
                    ),
            )
    }
}
