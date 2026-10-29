//! "Manage Remotes…": add, edit, rename and remove remotes.

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{Input, InputState},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::Remote;
use crate::model::{RepoEvent, RepoModel};
use crate::ui::confirm;
use crate::i18n::tr;

pub fn open(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| RemotesDialog::new(model, window, cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog.title(tr!("Git Remotes")).w(px(720.)).child(view.clone())
    });
}

struct RemotesDialog {
    model: Entity<RepoModel>,
    remotes: Vec<Remote>,
    /// The remote being edited; `None` while adding a new one.
    editing: Option<String>,
    name: Entity<InputState>,
    url: Entity<InputState>,
    load_task: Option<Task<()>>,
    _subscription: Subscription,
}

impl RemotesDialog {
    fn new(model: Entity<RepoModel>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let name = cx.new(|cx| InputState::new(window, cx).placeholder("origin"));
        let url = cx.new(|cx| InputState::new(window, cx).placeholder("https://github.com/owner/repo.git"));
        let subscription = cx.subscribe(&model, |this, _, event, cx| {
            if let RepoEvent::Changed = event {
                this.load(cx);
            }
        });
        let mut this = Self {
            model,
            remotes: Vec::new(),
            editing: None,
            name,
            url,
            load_task: None,
            _subscription: subscription,
        };
        this.load(cx);
        this
    }

    fn load(&mut self, cx: &mut Context<Self>) {
        let repo = self.model.read(cx).repo.clone();
        self.load_task = Some(cx.spawn(async move |this, cx| {
            let remotes = cx
                .background_executor()
                .spawn(async move { repo.remotes().unwrap_or_default() })
                .await;
            this.update(cx, |this, cx| {
                this.remotes = remotes;
                cx.notify();
            })
            .ok();
        }));
    }

    fn edit(&mut self, remote: Option<&Remote>, window: &mut Window, cx: &mut Context<Self>) {
        self.editing = remote.map(|r| r.name.clone());
        let (name, url) = remote
            .map(|r| (r.name.clone(), r.fetch_url.clone()))
            .unwrap_or_default();
        self.name.update(cx, |input, cx| input.set_value(name, window, cx));
        self.url.update(cx, |input, cx| input.set_value(url, window, cx));
        cx.notify();
    }

    fn save(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let name = self.name.read(cx).value().trim().to_string();
        let url = self.url.read(cx).value().trim().to_string();
        if name.is_empty() || url.is_empty() {
            return;
        }
        let editing = self.editing.take();
        self.model.update(cx, |model, cx| match editing {
            None => model.run(tr!("Add remote {}", name), move |repo| repo.add_remote(&name, &url), cx),
            Some(old) => model.run(
                tr!("Update remote {}", name),
                move |repo| {
                    if old != name {
                        repo.rename_remote(&old, &name)?;
                    }
                    repo.set_remote_url(&name, &url)
                },
                cx,
            ),
        });
        self.edit(None, window, cx);
    }

    fn remove(&mut self, name: String, window: &mut Window, cx: &mut Context<Self>) {
        let model = self.model.clone();
        confirm(
            tr!("Remove Remote"),
            tr!("Remove remote '{}' and its remote-tracking branches?", name),
            tr!("Remove"),
            move |_, cx| {
                let name = name.clone();
                model.update(cx, |model, cx| {
                    model.run(tr!("Remove remote {}", name), move |repo| repo.remove_remote(&name), cx)
                });
            },
            window,
            cx,
        );
    }
}

impl Render for RemotesDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let muted = cx.theme().muted_foreground;
        let rows: Vec<AnyElement> = self
            .remotes
            .iter()
            .enumerate()
            .map(|(ix, remote)| {
                let edit_remote = remote.clone();
                let name = remote.name.clone();
                let editing = self.editing.as_deref() == Some(remote.name.as_str());
                h_flex()
                    .px_3()
                    .py_2()
                    .gap_3()
                    .border_b_1()
                    .border_color(cx.theme().border)
                    .when(editing, |row| row.bg(cx.theme().list_active))
                    .child(div().w(px(110.)).flex_none().text_sm().font_weight(FontWeight::SEMIBOLD).child(remote.name.clone()))
                    .child(
                        v_flex()
                            .flex_1()
                            .min_w_0()
                            .text_sm()
                            .child(div().truncate().child(remote.fetch_url.clone()))
                            .when(remote.push_url != remote.fetch_url, |col| {
                                col.child(
                                    div()
                                        .truncate()
                                        .text_xs()
                                        .text_color(muted)
                                        .child(format!("push: {}", remote.push_url)),
                                )
                            }),
                    )
                    .child(
                        Button::new(("remote-edit", ix))
                            .icon(IconName::Pencil)
                            .ghost()
                            .xsmall()
                            .tooltip(tr!("Edit"))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.edit(Some(&edit_remote), window, cx)
                            })),
                    )
                    .child(
                        Button::new(("remote-remove", ix))
                            .icon(IconName::Trash)
                            .ghost()
                            .xsmall()
                            .tooltip(tr!("Remove"))
                            .on_click(cx.listener(move |this, _, window, cx| {
                                this.remove(name.clone(), window, cx)
                            })),
                    )
                    .into_any_element()
            })
            .collect();

        let editing = self.editing.clone();
        v_flex()
            .gap_3()
            .child(
                div()
                    .id("remotes")
                    .max_h(px(280.))
                    .overflow_y_scroll()
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .when(rows.is_empty(), |list| {
                        list.child(div().p_3().text_sm().text_color(muted).child(tr!("No remotes")))
                    })
                    .children(rows),
            )
            .child(
                div()
                    .text_sm()
                    .text_color(muted)
                    .child(match &editing {
                        Some(name) => tr!("Edit remote '{}'", name),
                        None => tr!("Add a remote").to_string(),
                    }),
            )
            .child(
                h_flex()
                    .gap_2()
                    .child(div().w(px(140.)).child(Input::new(&self.name)))
                    .child(div().flex_1().child(Input::new(&self.url)))
                    .when(editing.is_some(), |row| {
                        row.child(
                            Button::new("remote-cancel-edit")
                                .label(tr!("Cancel"))
                                .small()
                                .on_click(cx.listener(|this, _, window, cx| this.edit(None, window, cx))),
                        )
                    })
                    .child(
                        Button::new("remote-save")
                            .label(if editing.is_some() { tr!("Save") } else { tr!("Add") })
                            .primary()
                            .small()
                            .on_click(cx.listener(|this, _, window, cx| this.save(window, cx))),
                    ),
            )
            .child(
                h_flex().justify_end().child(
                    Button::new("remotes-close")
                        .label(tr!("Close"))
                        .small()
                        .on_click(|_, window, cx| window.close_dialog(cx)),
                ),
            )
    }
}
