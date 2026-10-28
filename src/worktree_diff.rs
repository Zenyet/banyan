//! "Show Diff with Working Tree" for a commit or branch.

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _, WindowExt as _, h_flex, h_resizable, resizable_panel, v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::actions::short;
use crate::diff_pane::DiffPane;
use crate::git::FileChange;
use crate::model::RepoModel;
use crate::ui::{selection_bg, status_color};

pub fn open(model: Entity<RepoModel>, rev: String, window: &mut Window, cx: &mut App) {
    let title = format!("Diff between {} and Working Tree", short(&rev));
    let view = cx.new(|cx| WorktreeDiff::new(model, rev, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title(title.clone()).w(px(1200.)).child(view.clone()));
}

struct WorktreeDiff {
    model: Entity<RepoModel>,
    rev: String,
    files: Vec<FileChange>,
    selected: Option<usize>,
    loading: bool,
    diff: Entity<DiffPane>,
    _task: Task<()>,
    diff_task: Option<Task<()>>,
}

impl WorktreeDiff {
    fn new(model: Entity<RepoModel>, rev: String, cx: &mut Context<Self>) -> Self {
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.show_message(None, "Loading…", cx));
        let repo = model.read(cx).repo.clone();
        let load_rev = rev.clone();
        let task = cx.spawn(async move |this, cx| {
            let files = cx
                .background_executor()
                .spawn(async move { repo.worktree_changes_since(&load_rev) })
                .await;
            this.update(cx, |this, cx| {
                this.loading = false;
                match files {
                    Ok(files) => {
                        this.files = files;
                        if this.files.is_empty() {
                            this.diff.update(cx, |d, cx| d.show_message(None, "No differences", cx));
                        } else {
                            this.select(0, cx);
                        }
                    }
                    Err(err) => this.diff.update(cx, |d, cx| d.show_message(None, format!("{err:#}"), cx)),
                }
                cx.notify();
            })
            .ok();
        });
        Self {
            model,
            rev,
            files: Vec::new(),
            selected: None,
            loading: true,
            diff,
            _task: task,
            diff_task: None,
        }
    }

    fn select(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(file) = self.files.get(ix).cloned() else { return };
        self.selected = Some(ix);
        let repo = self.model.read(cx).repo.clone();
        let rev = self.rev.clone();
        let title = file.path.clone();
        self.diff_task = Some(cx.spawn(async move |this, cx| {
            let patch = cx
                .background_executor()
                .spawn(async move { repo.worktree_diff_since(&rev, &file) })
                .await;
            this.update(cx, |this, cx| {
                this.diff.update(cx, |diff, cx| match patch {
                    Ok(patch) => diff.show_patch(title, &patch, cx),
                    Err(err) => diff.show_message(Some(title.into()), format!("{err:#}"), cx),
                })
            })
            .ok();
        }));
        cx.notify();
    }
}

impl Render for WorktreeDiff {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let rows: Vec<AnyElement> = self
            .files
            .iter()
            .enumerate()
            .map(|(ix, file)| {
                let color = status_color(file.status, cx);
                let selected = self.selected == Some(ix);
                h_flex()
                    .id(("worktree-file", ix))
                    .h(px(24.))
                    .px_2()
                    .gap_1()
                    .text_sm()
                    .when(selected, |row| row.bg(selection_bg()))
                    .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
                    .child(Icon::new(IconName::File).xsmall().text_color(color))
                    .child(div().truncate().text_color(color).child(file.path.clone()))
                    .on_click(cx.listener(move |this, _, _, cx| this.select(ix, cx)))
                    .into_any_element()
            })
            .collect();
        let count = self.files.len();
        div()
            .h(px(620.))
            .border_1()
            .border_color(cx.theme().border)
            .rounded(cx.theme().radius)
            .overflow_hidden()
            .child(
                h_resizable("worktree-diff")
                    .child(
                        resizable_panel().size(px(320.)).child(
                            v_flex()
                                .size_full()
                                .child(
                                    div()
                                        .px_2()
                                        .py_1()
                                        .text_xs()
                                        .text_color(cx.theme().muted_foreground)
                                        .child(if self.loading { "Loading…".to_string() } else { format!("{count} files") }),
                                )
                                .child(div().id("worktree-files").flex_1().min_h_0().overflow_y_scroll().children(rows)),
                        ),
                    )
                    .child(resizable_panel().child(self.diff.clone())),
            )
    }
}
