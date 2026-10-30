//! "Create Tag": a name, an optional message that makes it an annotated
//! tag, and whether to push it right away.

use gpui_kit::component::{
    ActiveTheme as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex,
    input::{Input, InputState, Textarea, TextareaState},
    v_flex,
};
use gpui_kit::*;

use crate::actions::short;
use crate::i18n::tr;
use crate::model::{RepoEvent, RepoModel};

/// Tag commit `target`.
pub fn open(model: Entity<RepoModel>, target: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| TagDialog::new(model, target.clone(), window, cx));
    let focus = view.read(cx).name.focus_handle(cx);
    window.open_dialog(cx, move |dialog, _, _| {
        dialog.title(tr!("Create Tag on {}", short(&target))).w(px(480.)).child(view.clone())
    });
    window.focus(&focus, cx);
}

struct TagDialog {
    model: Entity<RepoModel>,
    target: String,
    name: Entity<InputState>,
    message: Entity<TextareaState>,
    push: bool,
}

impl TagDialog {
    fn new(model: Entity<RepoModel>, target: String, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let name = cx.new(|cx| InputState::new(window, cx).placeholder("v1.0.0"));
        let message = cx.new(|cx| TextareaState::new(window, cx).placeholder(tr!("Optional")).auto_grow(3, 8));
        Self { model, target, name, message, push: false }
    }

    fn confirm(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let name = self.name.read(cx).value().trim().to_string();
        if name.is_empty() {
            self.model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(tr!("Name the tag").into())));
            return;
        }
        let message = self.message.read(cx).value().trim().to_string();
        let (target, push) = (self.target.clone(), self.push);
        window.close_dialog(cx);
        self.model.update(cx, |m, cx| {
            m.run(
                tr!("Create tag {}", name),
                move |repo| {
                    let out = repo.create_tag(&name, &target, (!message.is_empty()).then_some(message.as_str()))?;
                    if push { repo.push_tag(&name) } else { Ok(out) }
                },
                cx,
            )
        });
    }
}

impl Render for TagDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let muted = cx.theme().muted_foreground;
        v_flex()
            .gap_3()
            .text_sm()
            .child(v_flex().gap_1().child(div().child(tr!("Name"))).child(Input::new(&self.name)))
            .child(
                v_flex()
                    .gap_1()
                    .child(div().child(tr!("Message")))
                    .child(Textarea::new(&self.message))
                    .child(div().text_xs().text_color(muted).child(tr!("With a message the tag is annotated: it records who tagged and when."))),
            )
            .child(
                Checkbox::new("tag-push")
                    .label(tr!("Push the tag"))
                    .checked(self.push)
                    .on_click(cx.listener(|this, checked: &bool, _, cx| {
                        this.push = *checked;
                        cx.notify();
                    })),
            )
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(Button::new("tag-cancel").label(tr!("Cancel")).small().on_click(|_, window, cx| window.close_dialog(cx)))
                    .child(
                        Button::new("tag-ok")
                            .label(tr!("Create"))
                            .primary()
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.confirm(window, cx))),
                    ),
            )
    }
}
