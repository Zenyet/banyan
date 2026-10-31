//! "Submodules…": the repository's submodules with their state, updating
//! them to the recorded commits, and opening one as a repository of its own.

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Icon, Sizable as _, StyledExt as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::{Submodule, SubmoduleState};
use crate::i18n::tr;
use crate::model::{RepoEvent, RepoModel};

pub fn open(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| SubmodulesDialog::new(model, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Submodules")).w(px(680.)).child(view.clone()));
}

struct SubmodulesDialog {
    model: Entity<RepoModel>,
    submodules: Option<Vec<Submodule>>,
    load_task: Option<Task<()>>,
    _subscription: Subscription,
}

impl SubmodulesDialog {
    fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        let subscription = cx.subscribe(&model, |this, _, event, cx| {
            if let RepoEvent::Changed = event {
                this.load(cx);
            }
        });
        let mut this = Self { model, submodules: None, load_task: None, _subscription: subscription };
        this.load(cx);
        this
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        let repo = self.model.read(cx).repo.clone();
        self.load_task = Some(cx.spawn(async move |this, cx| {
            let submodules = cx.background_executor().spawn(async move { repo.submodules().unwrap_or_default() }).await;
            this.update(cx, |this, cx| {
                this.submodules = Some(submodules);
                cx.notify();
            })
            .ok();
        }));
    }

    fn update(&self, paths: Vec<String>, cx: &mut Context<Self>) {
        let label = match paths.as_slice() {
            [path] => tr!("Update submodule {}", path),
            _ => tr!("Update submodules").to_string(),
        };
        self.model.update(cx, |m, cx| m.run(label, move |repo| repo.update_submodules(&paths), cx));
    }

    fn render_row(&self, ix: usize, sub: &Submodule, cx: &mut Context<Self>) -> AnyElement {
        let (state, color) = match sub.state {
            SubmoduleState::UpToDate => (tr!("up to date"), cx.theme().success),
            SubmoduleState::OutOfDate => (tr!("at another commit"), cx.theme().warning),
            SubmoduleState::Uninitialized => (tr!("not initialized"), cx.theme().muted_foreground),
            SubmoduleState::Conflict => (tr!("conflict"), cx.theme().danger),
        };
        let path = sub.path.clone();
        let full = self.model.read(cx).repo.root().join(&sub.path);
        h_flex()
            .id(("submodule", ix))
            .px_2()
            .py_1p5()
            .gap_2()
            .rounded(cx.theme().radius)
            .hover(|s| s.bg(cx.theme().list_hover))
            .child(Icon::new(IconName::FolderOpen).small().text_color(cx.theme().muted_foreground))
            .child(
                v_flex()
                    .flex_1()
                    .min_w_0()
                    .child(div().text_sm().font_semibold().truncate().child(sub.path.clone()))
                    .child(
                        h_flex()
                            .gap_2()
                            .text_xs()
                            .child(div().text_color(color).child(state))
                            .child(
                                div()
                                    .text_color(cx.theme().muted_foreground)
                                    .child(sub.commit[..sub.commit.len().min(8)].to_string()),
                            ),
                    ),
            )
            .when(sub.state != SubmoduleState::UpToDate, |row| {
                row.child(
                    Button::new(("update-submodule", ix))
                        .label(tr!("Update"))
                        .small()
                        .on_click(cx.listener(move |this, _, _, cx| this.update(vec![path.clone()], cx))),
                )
            })
            .when(sub.state != SubmoduleState::Uninitialized, |row| {
                row.child(
                    Button::new(("open-submodule", ix))
                        .label(tr!("Open"))
                        .small()
                        .on_click(move |_, window, cx| crate::welcome::open_path(&full, None, window, cx)),
                )
            })
            .into_any_element()
    }
}

impl Render for SubmodulesDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let submodules = self.submodules.clone();
        let body = match &submodules {
            None => div().p_3().text_sm().text_color(cx.theme().muted_foreground).child(tr!("Loading…")).into_any_element(),
            Some(subs) if subs.is_empty() => div()
                .p_3()
                .text_sm()
                .text_color(cx.theme().muted_foreground)
                .child(tr!("This repository has no submodules."))
                .into_any_element(),
            Some(subs) => v_flex()
                .id("submodules")
                .max_h(px(360.))
                .overflow_y_scroll()
                .border_1()
                .border_color(cx.theme().border)
                .rounded(cx.theme().radius)
                .p_1()
                .children(subs.iter().enumerate().map(|(ix, sub)| self.render_row(ix, sub, cx)).collect::<Vec<_>>())
                .into_any_element(),
        };
        let needs_update = submodules.as_ref().is_some_and(|subs| subs.iter().any(|s| s.state != SubmoduleState::UpToDate));
        v_flex()
            .gap_3()
            .child(body)
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        div()
                            .flex_1()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(tr!("Updating clones missing submodules and checks each out at the commit this repository records.")),
                    )
                    .child(
                        Button::new("update-all-submodules")
                            .label(tr!("Update All"))
                            .primary()
                            .small()
                            .disabled(!needs_update)
                            .on_click(cx.listener(|this, _, _, cx| this.update(Vec::new(), cx))),
                    ),
            )
    }
}
