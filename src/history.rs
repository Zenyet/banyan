//! "Show History" for a file and "Annotate with Git Blame".

use std::rc::Rc;

use gpui_kit::component::{
    ActiveTheme as _, WindowExt as _, h_flex, h_resizable,
    menu::{ContextMenuExt as _, PopupMenuItem},
    resizable_panel, v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::actions::short;
use crate::diff_pane::DiffPane;
use crate::git::{BlameLine, FileRevision};
use crate::model::{RepoEvent, RepoModel};
use crate::ui::{format_time, lane_color, selection_bg};

const ROW_HEIGHT: Pixels = px(24.);

pub fn show_history(model: Entity<RepoModel>, path: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| HistoryView::new(model, path.clone(), cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title(format!("History for {path}"))
            .w(px(1200.))
            .child(view.clone())
    });
}

pub fn annotate(model: Entity<RepoModel>, rev: String, path: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| AnnotateView::new(model, rev.clone(), path.clone(), cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title(format!("Annotate {path} at {}", short(&rev)))
            .w(px(1200.))
            .child(view.clone())
    });
}

fn report(model: &Entity<RepoModel>, err: anyhow::Error, cx: &mut App) {
    let message: SharedString = format!("{err:#}").into();
    model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(message)));
}

struct HistoryView {
    model: Entity<RepoModel>,
    path: String,
    revisions: Rc<Vec<FileRevision>>,
    selected: Option<usize>,
    diff: Entity<DiffPane>,
    loading: bool,
    _task: Option<Task<()>>,
    diff_task: Option<Task<()>>,
}

impl HistoryView {
    fn new(model: Entity<RepoModel>, path: String, cx: &mut Context<Self>) -> Self {
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.show_message(None, "Loading history…", cx));
        let repo = model.read(cx).repo.clone();
        let file = path.clone();
        let task = cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.file_history(&file) })
                .await;
            this.update(cx, |this, cx| {
                this.loading = false;
                match result {
                    Ok(revisions) => {
                        this.revisions = Rc::new(revisions);
                        this.select(0, cx);
                    }
                    Err(err) => report(&this.model, err, cx),
                }
                cx.notify();
            })
            .ok();
        });
        Self {
            model,
            path,
            revisions: Rc::default(),
            selected: None,
            diff,
            loading: true,
            _task: Some(task),
            diff_task: None,
        }
    }

    fn select(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(revision) = self.revisions.get(ix).cloned() else {
            self.diff.update(cx, |diff, cx| diff.show_message(None, "No history", cx));
            return;
        };
        self.selected = Some(ix);
        let repo = self.model.read(cx).repo.clone();
        let title = format!("{}  ({})", revision.path, revision.commit.short_id());
        self.diff_task = Some(cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.revision_diff(&revision.commit.id, &revision.path) })
                .await;
            this.update(cx, |this, cx| {
                this.diff.update(cx, |diff, cx| match result {
                    Ok(patch) => diff.show_patch(title, &patch, cx),
                    Err(err) => diff.show_message(Some(title.into()), format!("{err:#}"), cx),
                })
            })
            .ok();
        }));
        cx.notify();
    }
}

impl Render for HistoryView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let count = self.revisions.len();
        let list = uniform_list(
            "file-history",
            count,
            cx.processor(|this, range: std::ops::Range<usize>, _, cx| {
                range
                    .map(|ix| {
                        let revision = &this.revisions[ix];
                        let commit = &revision.commit;
                        let selected = this.selected == Some(ix);
                        let model = this.model.clone();
                        let id = commit.id.clone();
                        let path = revision.path.clone();
                        h_flex()
                            .id(ix)
                            .w_full()
                            .h(ROW_HEIGHT)
                            .px_2()
                            .gap_2()
                            .text_sm()
                            .when(selected, |row| row.bg(selection_bg()))
                            .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
                            .child(
                                div()
                                    .flex_none()
                                    .w(px(64.))
                                    .whitespace_nowrap()
                                    .text_color(cx.theme().muted_foreground)
                                    .child(commit.short_id().to_string()),
                            )
                            .child(div().flex_1().min_w_0().truncate().child(commit.subject.clone()))
                            .child(
                                div()
                                    .flex_none()
                                    .w(px(90.))
                                    .truncate()
                                    .text_color(cx.theme().muted_foreground)
                                    .child(commit.author.clone()),
                            )
                            .child(
                                div()
                                    .flex_none()
                                    .w(px(120.))
                                    .text_color(cx.theme().muted_foreground)
                                    .child(format_time(commit.time)),
                            )
                            .on_click(cx.listener(move |this, _, _, cx| this.select(ix, cx)))
                            .context_menu(move |menu, _, _| {
                                let model = model.clone();
                                let id = id.clone();
                                let path = path.clone();
                                menu.item(PopupMenuItem::new("Annotate at This Revision").on_click(
                                    move |_, window, cx| {
                                        annotate(model.clone(), id.clone(), path.clone(), window, cx)
                                    },
                                ))
                            })
                    })
                    .collect::<Vec<_>>()
            }),
        )
        .size_full();

        let left = v_flex()
            .size_full()
            .child(
                div()
                    .h(px(28.))
                    .px_2()
                    .flex()
                    .items_center()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .border_b_1()
                    .border_color(cx.theme().border)
                    .child(if self.loading {
                        "Loading…".to_string()
                    } else {
                        format!("{count} revisions of {}", self.path)
                    }),
            )
            .child(div().flex_1().min_h_0().child(list));

        div()
            .h(px(640.))
            .border_1()
            .border_color(cx.theme().border)
            .rounded(cx.theme().radius)
            .overflow_hidden()
            .child(
                h_resizable("history")
                    .child(resizable_panel().size(px(460.)).child(left))
                    .child(resizable_panel().child(self.diff.clone())),
            )
    }
}

struct AnnotateView {
    lines: Rc<Vec<BlameLine>>,
    /// Commit ids ordered from oldest to newest, for age shading.
    ages: Rc<std::collections::HashMap<String, f32>>,
    loading: bool,
    _task: Task<()>,
}

impl AnnotateView {
    fn new(model: Entity<RepoModel>, rev: String, path: String, cx: &mut Context<Self>) -> Self {
        let repo = model.read(cx).repo.clone();
        let task = cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.blame(&rev, &path) })
                .await;
            this.update(cx, |this, cx| {
                this.loading = false;
                match result {
                    Ok(lines) => {
                        let mut times: Vec<(i64, String)> =
                            lines.iter().map(|l| (l.time, l.id.clone())).collect();
                        times.sort();
                        times.dedup();
                        let n = times.len().max(2) as f32 - 1.;
                        this.ages = Rc::new(
                            times
                                .into_iter()
                                .enumerate()
                                .map(|(ix, (_, id))| (id, ix as f32 / n))
                                .collect(),
                        );
                        this.lines = Rc::new(lines);
                    }
                    Err(err) => report(&model, err, cx),
                }
                cx.notify();
            })
            .ok();
        });
        Self {
            lines: Rc::default(),
            ages: Rc::default(),
            loading: true,
            _task: task,
        }
    }
}

impl Render for AnnotateView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let list = uniform_list(
            "annotate",
            self.lines.len(),
            cx.processor(|this, range: std::ops::Range<usize>, _, cx| {
                range
                    .map(|ix| {
                        let line = &this.lines[ix];
                        let first_of_run = ix == 0 || this.lines[ix - 1].id != line.id;
                        let age = this.ages.get(&line.id).copied().unwrap_or(0.);
                        // Newer commits get a stronger tint, like JetBrains' annotation gutter.
                        let mut tint = lane_color(0);
                        tint.a = 0.06 + 0.3 * age;
                        h_flex()
                            .id(ix)
                            .w_full()
                            .h(px(20.))
                            .text_sm()
                            .child(
                                h_flex()
                                    .id(("gutter", ix))
                                    .flex_none()
                                    .w(px(300.))
                                    .h_full()
                                    .px_2()
                                    .gap_2()
                                    .bg(tint)
                                    .text_xs()
                                    .text_color(cx.theme().muted_foreground)
                                    .when(first_of_run, |gutter| {
                                        gutter
                                            .child(div().flex_none().w(px(60.)).child(line.id[..8].to_string()))
                                            .child(div().flex_none().w(px(80.)).truncate().child(line.author.clone()))
                                            .child(div().flex_1().truncate().child(format_time(line.time)))
                                    })
                                    .tooltip({
                                        let tip: SharedString = format!("{} {}", &line.id[..8], line.summary).into();
                                        move |window, cx| {
                                            gpui_kit::component::tooltip::Tooltip::new(tip.clone()).build(window, cx)
                                        }
                                    }),
                            )
                            .child(
                                div()
                                    .pl_1()
                                    .w(px(30.))
                                    .flex_none()
                                    .text_xs()
                                    .text_color(cx.theme().muted_foreground)
                                    .child((ix + 1).to_string()),
                            )
                            .child(
                                div()
                                    .font_family(cx.theme().mono_font_family.clone())
                                    .whitespace_nowrap()
                                    .child(line.text.replace('\t', "    ")),
                            )
                    })
                    .collect::<Vec<_>>()
            }),
        )
        .size_full();

        div()
            .h(px(640.))
            .border_1()
            .border_color(cx.theme().border)
            .rounded(cx.theme().radius)
            .overflow_hidden()
            .when(self.loading, |el| el.child(div().p_3().text_sm().child("Loading…")))
            .child(list)
    }
}
