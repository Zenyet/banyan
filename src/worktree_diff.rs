//! The changes between two revisions, or between a revision and the work
//! tree: "Show Diff with Working Tree", "Compare with Local" and comparing
//! two commits selected in the Log.

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

/// Changes from `rev` to the work tree.
pub fn open(model: Entity<RepoModel>, rev: String, window: &mut Window, cx: &mut App) {
    open_between(model, rev, None, None, window, cx);
}

/// Changes from `from` to `to`, or to the work tree when `to` is `None`;
/// only those of `path` when given.
pub fn open_between(
    model: Entity<RepoModel>,
    from: String,
    to: Option<String>,
    path: Option<String>,
    window: &mut Window,
    cx: &mut App,
) {
    let target = to.as_deref().map(|to| short(to).to_string()).unwrap_or_else(|| "Working Tree".into());
    let title = match &path {
        Some(path) => format!("{path}: {} vs {target}", short(&from)),
        None => format!("Diff between {} and {target}", short(&from)),
    };
    let view = cx.new(|cx| WorktreeDiff::new(model, from, to, path, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title(title.clone()).w(px(1200.)).child(view.clone()));
}

struct WorktreeDiff {
    model: Entity<RepoModel>,
    rev: String,
    to: Option<String>,
    files: Vec<FileChange>,
    selected: Option<usize>,
    loading: bool,
    diff: Entity<DiffPane>,
    _task: Task<()>,
}

impl WorktreeDiff {
    fn new(model: Entity<RepoModel>, rev: String, to: Option<String>, path: Option<String>, cx: &mut Context<Self>) -> Self {
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.show_message(None, "Loading…", cx));
        let repo = model.read(cx).repo.clone();
        let (load_from, load_to) = (rev.clone(), to.clone());
        let task = cx.spawn(async move |this, cx| {
            let files = cx
                .background_executor()
                .spawn(async move {
                    let files = repo.changes_between(&load_from, load_to.as_deref())?;
                    Ok::<_, anyhow::Error>(match &path {
                        Some(path) => files
                            .into_iter()
                            .filter(|f| &f.path == path || f.orig_path.as_ref() == Some(path))
                            .collect(),
                        None => files,
                    })
                })
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
            to,
            files: Vec::new(),
            selected: None,
            loading: true,
            diff,
            _task: task,
        }
    }

    fn select(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(file) = self.files.get(ix).cloned() else { return };
        self.selected = Some(ix);
        let repo = self.model.read(cx).repo.clone();
        let (from, to) = (self.rev.clone(), self.to.clone());
        let title = file.path.clone();
        self.diff
            .update(cx, |diff, cx| diff.load(title, move || repo.diff_between(&from, to.as_deref(), &file), cx));
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
                    .child(resizable_panel().child(crate::ui::cached(self.diff.clone()))),
            )
    }
}
