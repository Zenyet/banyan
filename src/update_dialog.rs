//! JetBrains' "Update Project" dialog: merge or rebase the incoming
//! changes, or only fast-forward.

use gpui_kit::component::{
    ActiveTheme as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::*;

use crate::git::UpdateMethod;
use crate::model::RepoModel;
use crate::settings::Settings;
use crate::i18n::tr;

pub fn open(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|_| UpdateDialog {
        model,
        method: Settings::get().update_method,
    });
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Update Project")).w(px(440.)).child(view.clone()));
}

struct UpdateDialog {
    model: Entity<RepoModel>,
    method: UpdateMethod,
}

impl UpdateDialog {
    fn update(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let method = self.method;
        Settings::update(|settings| settings.update_method = method);
        window.close_dialog(cx);
        self.model.update(cx, |model, cx| {
            model.run(tr!("Update Project"), move |repo| repo.update_project(method), cx)
        });
    }

    fn option(&self, method: UpdateMethod, cx: &mut Context<Self>) -> impl IntoElement {
        let selected = self.method == method;
        v_flex()
            .id(method.key())
            .p_2()
            .gap_0p5()
            .rounded(cx.theme().radius)
            .border_1()
            .border_color(if selected { cx.theme().primary } else { cx.theme().border })
            .child(div().text_sm().child(method.label()))
            .child(div().text_xs().text_color(cx.theme().muted_foreground).child(method.detail()))
            .on_click(cx.listener(move |this, _, _, cx| {
                this.method = method;
                cx.notify();
            }))
    }
}

impl Render for UpdateDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let branch = self.model.read(cx).current_branch.clone().unwrap_or_else(|| "HEAD".into());
        v_flex()
            .gap_3()
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("Fetch and update {} from its tracked branch. Local changes are stashed and restored.", branch)),
            )
            .child(self.option(UpdateMethod::Merge, cx))
            .child(self.option(UpdateMethod::Rebase, cx))
            .child(self.option(UpdateMethod::FastForwardOnly, cx))
            .child(
                h_flex()
                    .justify_end()
                    .gap_2()
                    .child(Button::new("update-cancel").label(tr!("Cancel")).small().on_click(|_, window, cx| window.close_dialog(cx)))
                    .child(
                        Button::new("update-ok")
                            .label(tr!("Update"))
                            .primary()
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.update(window, cx))),
                    ),
            )
    }
}
