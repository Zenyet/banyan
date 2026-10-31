//! JetBrains-style Push dialog: review outgoing commits and their files
//! before pushing.

use std::rc::Rc;

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Icon, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    checkbox::Checkbox,
    h_flex, h_resizable, resizable_panel, v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::git::{Commit, FileChange, PushTarget};
use crate::model::RepoModel;
use crate::settings::Settings;
use crate::ui::{format_time, selection_bg, status_color};
use crate::i18n::tr;

pub fn open(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| PushDialog::new(model, cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog.title(tr!("Push Commits")).w(px(900.)).child(view.clone())
    });
}

struct PushDialog {
    model: Entity<RepoModel>,
    target: Option<PushTarget>,
    commits: Rc<Vec<Commit>>,
    selected: Option<usize>,
    files: Vec<FileChange>,
    force: bool,
    tags: bool,
    error: Option<SharedString>,
    loading: bool,
    _task: Task<()>,
    files_task: Option<Task<()>>,
}

impl PushDialog {
    fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        let repo = model.read(cx).repo.clone();
        let task = cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    let target = repo.push_target()?;
                    let commits = repo.outgoing(&target)?;
                    anyhow::Ok((target, commits))
                })
                .await;
            this.update(cx, |this, cx| {
                this.loading = false;
                match result {
                    Ok((target, commits)) => {
                        this.target = Some(target);
                        this.commits = Rc::new(commits);
                        this.select(0, cx);
                    }
                    Err(err) => this.error = Some(format!("{err:#}").into()),
                }
                cx.notify();
            })
            .ok();
        });
        Self {
            model,
            target: None,
            commits: Rc::default(),
            selected: None,
            files: Vec::new(),
            force: false,
            tags: false,
            error: None,
            loading: true,
            _task: task,
            files_task: None,
        }
    }

    fn select(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(commit) = self.commits.get(ix) else { return };
        self.selected = Some(ix);
        let id = commit.id.clone();
        let repo = self.model.read(cx).repo.clone();
        self.files_task = Some(cx.spawn(async move |this, cx| {
            let details = cx
                .background_executor()
                .spawn(async move { repo.commit_details(&id) })
                .await;
            this.update(cx, |this, cx| {
                this.files = details.map(|d| d.files).unwrap_or_default();
                cx.notify();
            })
            .ok();
        }));
        cx.notify();
    }

    fn push(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(target) = self.target.clone() else { return };
        let (force, tags) = (self.force, self.tags);
        window.close_dialog(cx);
        let label = if force { tr!("Force Push") } else { tr!("Push") };
        self.model.update(cx, |model, cx| {
            model.run(label, move |repo| repo.push_to(&target, force, tags), cx)
        });
    }
}

impl Render for PushDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let muted = cx.theme().muted_foreground;
        let header = match (&self.target, &self.error) {
            (_, Some(error)) => div().text_color(cx.theme().danger).child(error.clone()),
            (Some(target), _) => h_flex()
                .gap_1()
                .child(Icon::new(IconName::GitBranch).small())
                .child(target.branch.clone())
                .child(div().text_color(muted).child("→"))
                .child(format!("{} : {}", target.remote, target.remote_branch))
                .when(target.is_new, |row| {
                    row.child(
                        div()
                            .px_1()
                            .rounded(px(3.))
                            .text_xs()
                            .bg(rgb(0x5f9e5f))
                            .text_color(white())
                            .child(tr!("New")),
                    )
                }),
            _ => div().text_color(muted).child(tr!("Loading…")),
        };

        let commits = uniform_list(
            "push-commits",
            self.commits.len(),
            cx.processor(|this, range: std::ops::Range<usize>, _, cx| {
                range
                    .map(|ix| {
                        let commit = &this.commits[ix];
                        let selected = this.selected == Some(ix);
                        h_flex()
                            .id(ix)
                            .w_full()
                            .h(px(24.))
                            .px_2()
                            .gap_2()
                            .text_sm()
                            .when(selected, |row| row.bg(selection_bg()))
                            .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
                            .child(div().flex_1().min_w_0().truncate().child(commit.subject.clone()))
                            .child(
                                div()
                                    .flex_none()
                                    .text_xs()
                                    .text_color(cx.theme().muted_foreground)
                                    .child(format_time(commit.time)),
                            )
                            .on_click(cx.listener(move |this, _, _, cx| this.select(ix, cx)))
                    })
                    .collect::<Vec<_>>()
            }),
        )
        .size_full();

        let files = div()
            .id("push-files")
            .size_full()
            .overflow_y_scroll()
            .py_1()
            .children(self.files.iter().map(|file| {
                let color = status_color(file.status, cx);
                h_flex()
                    .h(px(22.))
                    .px_2()
                    .gap_1()
                    .text_sm()
                    .child(Icon::new(IconName::File).xsmall().text_color(color))
                    .child(div().truncate().text_color(color).child(file.path.clone()))
            }));

        let can_push = self.target.is_some() && (!self.commits.is_empty() || self.force || self.tags);
        v_flex()
            .gap_3()
            .child(h_flex().text_sm().child(header))
            .child(
                div()
                    .h(px(380.))
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .overflow_hidden()
                    .child(if !self.loading && self.commits.is_empty() && self.error.is_none() {
                        div()
                            .size_full()
                            .flex()
                            .items_center()
                            .justify_center()
                            .text_sm()
                            .text_color(muted)
                            .child(tr!("Nothing to push"))
                            .into_any_element()
                    } else {
                        h_resizable("push")
                            .child(resizable_panel().size(px(520.)).child(commits))
                            .child(resizable_panel().child(files))
                            .into_any_element()
                    }),
            )
            .child(
                h_flex()
                    .gap_2()
                    .map(|row| {
                        let protected = self
                            .target
                            .as_ref()
                            .is_some_and(|t| Settings::read(|s| s.is_protected(&t.remote_branch)));
                        if protected {
                            row.child(
                                div()
                                    .text_xs()
                                    .text_color(cx.theme().muted_foreground)
                                    .child(tr!("Protected branch: no force push")),
                            )
                        } else {
                            row.child(
                                Checkbox::new("force-push")
                                    .label(tr!("Force push (--force-with-lease)"))
                                    .checked(self.force)
                                    .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                        this.force = *checked;
                                        cx.notify();
                                    })),
                            )
                        }
                    })
                    .child(
                        Checkbox::new("push-tags")
                            .label(tr!("Push tags"))
                            .checked(self.tags)
                            .on_click(cx.listener(|this, checked: &bool, _, cx| {
                                this.tags = *checked;
                                cx.notify();
                            })),
                    )
                    .child(div().flex_1())
                    .child(
                        Button::new("push-cancel")
                            .label(tr!("Cancel"))
                            .small()
                            .on_click(|_, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("push-confirm")
                            .label(if self.force { tr!("Force Push") } else { tr!("Push") })
                            .small()
                            .map(|button| if self.force { button.danger() } else { button.primary() })
                            .disabled(!can_push)
                            .on_click(cx.listener(|this, _, window, cx| this.push(window, cx))),
                    ),
            )
    }
}
