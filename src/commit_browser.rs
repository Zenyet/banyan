//! A list of commits with the selected commit's files and diff, used by the
//! branch comparison and stash dialogs.

use std::rc::Rc;
use std::sync::Arc;

use gpui_kit::assets::IconName;
use gpui_kit::component::{ActiveTheme as _, Icon, Sizable as _, h_flex, h_resizable, resizable_panel, v_flex, v_resizable};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::diff_pane::DiffPane;
use crate::git::{Commit, CommitDetails};
use crate::model::RepoModel;
use crate::ui::{format_time, selection_bg, status_color};

pub struct CommitBrowser {
    model: Entity<RepoModel>,
    commits: Rc<Vec<Commit>>,
    selected: Option<usize>,
    details: Option<Arc<CommitDetails>>,
    selected_file: Option<usize>,
    diff: Entity<DiffPane>,
    empty_message: SharedString,
    details_task: Option<Task<()>>,
    diff_task: Option<Task<()>>,
}

impl CommitBrowser {
    pub fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.clear(cx));
        Self {
            model,
            commits: Rc::default(),
            selected: None,
            details: None,
            selected_file: None,
            diff,
            empty_message: "No commits".into(),
            details_task: None,
            diff_task: None,
        }
    }

    pub fn set_commits(&mut self, commits: Vec<Commit>, empty_message: impl Into<SharedString>, cx: &mut Context<Self>) {
        self.commits = Rc::new(commits);
        self.empty_message = empty_message.into();
        self.select(if self.commits.is_empty() { None } else { Some(0) }, cx);
    }

    fn select(&mut self, ix: Option<usize>, cx: &mut Context<Self>) {
        self.selected = ix;
        self.details = None;
        self.selected_file = None;
        cx.notify();
        let Some(commit) = ix.and_then(|ix| self.commits.get(ix)) else {
            self.diff.update(cx, |diff, cx| diff.show_message(None, self.empty_message.clone(), cx));
            return;
        };
        let id = commit.id.clone();
        let repo = self.model.read(cx).repo.clone();
        self.details_task = Some(cx.spawn(async move |this, cx| {
            let details = cx
                .background_executor()
                .spawn(async move { repo.commit_details(&id) })
                .await;
            this.update(cx, |this, cx| match details {
                Ok(details) => {
                    let has_files = !details.files.is_empty();
                    this.details = Some(Arc::new(details));
                    if has_files {
                        this.select_file(0, cx);
                    } else {
                        this.diff.update(cx, |diff, cx| diff.show_message(None, "No changed files", cx));
                    }
                    cx.notify();
                }
                Err(err) => this.diff.update(cx, |diff, cx| diff.show_message(None, format!("{err:#}"), cx)),
            })
            .ok();
        }));
    }

    fn select_file(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(details) = self.details.clone() else { return };
        let Some(file) = details.files.get(ix).cloned() else { return };
        self.selected_file = Some(ix);
        cx.notify();
        let repo = self.model.read(cx).repo.clone();
        let title = file.path.clone();
        self.diff_task = Some(cx.spawn(async move |this, cx| {
            let patch = cx
                .background_executor()
                .spawn(async move { repo.commit_file_diff(&details, &file) })
                .await;
            this.update(cx, |this, cx| {
                this.diff.update(cx, |diff, cx| match patch {
                    Ok(patch) => diff.show_patch(title, &patch, cx),
                    Err(err) => diff.show_message(Some(title.into()), format!("{err:#}"), cx),
                })
            })
            .ok();
        }));
    }
}

impl Render for CommitBrowser {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let commits = uniform_list(
            "browser-commits",
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
                                    .child(format!("{}  {}", commit.author, format_time(commit.time))),
                            )
                            .on_click(cx.listener(move |this, _, _, cx| this.select(Some(ix), cx)))
                    })
                    .collect::<Vec<_>>()
            }),
        )
        .size_full();

        let commits_panel = if self.commits.is_empty() {
            div()
                .size_full()
                .flex()
                .items_center()
                .justify_center()
                .text_sm()
                .text_color(cx.theme().muted_foreground)
                .child(self.empty_message.clone())
                .into_any_element()
        } else {
            commits.into_any_element()
        };

        let files: Vec<AnyElement> = self
            .details
            .iter()
            .flat_map(|details| details.files.iter().enumerate())
            .map(|(ix, file)| {
                let color = status_color(file.status, cx);
                let selected = self.selected_file == Some(ix);
                h_flex()
                    .id(("browser-file", ix))
                    .h(px(22.))
                    .px_2()
                    .gap_1()
                    .text_sm()
                    .when(selected, |row| row.bg(selection_bg()))
                    .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
                    .child(Icon::new(IconName::File).xsmall().text_color(color))
                    .child(div().truncate().text_color(color).child(file.path.clone()))
                    .on_click(cx.listener(move |this, _, _, cx| this.select_file(ix, cx)))
                    .into_any_element()
            })
            .collect();

        h_resizable("commit-browser")
            .child(resizable_panel().size(px(380.)).child(commits_panel))
            .child(
                resizable_panel().child(
                    v_resizable("commit-browser-right")
                        .child(
                            resizable_panel().size(px(150.)).child(
                                v_flex()
                                    .id("browser-files")
                                    .size_full()
                                    .overflow_y_scroll()
                                    .py_1()
                                    .children(files),
                            ),
                        )
                        .child(resizable_panel().child(self.diff.clone())),
                ),
            )
    }
}
