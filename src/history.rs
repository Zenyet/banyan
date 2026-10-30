//! "Show History" for a file and "Annotate with Git Blame".

use std::rc::Rc;

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Sizable as _, WindowExt as _, button::Button, h_flex, h_resizable,
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
use crate::i18n::tr;

const ROW_HEIGHT: Pixels = px(24.);

pub fn show_history(model: Entity<RepoModel>, path: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| HistoryView::new(model, path.clone(), cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title(tr!("History for {}", path))
            .w(px(1200.))
            .child(view.clone())
    });
}

pub fn annotate(model: Entity<RepoModel>, rev: String, path: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| AnnotateView::new(model, rev.clone(), path.clone(), cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title(tr!("Annotate {} at {}", path, short(&rev)))
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
}

impl HistoryView {
    fn new(model: Entity<RepoModel>, path: String, cx: &mut Context<Self>) -> Self {
        let diff = cx.new(DiffPane::new);
        diff.update(cx, |diff, cx| diff.show_message(None, tr!("Loading history…"), cx));
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
        }
    }

    fn select(&mut self, ix: usize, cx: &mut Context<Self>) {
        let Some(revision) = self.revisions.get(ix).cloned() else {
            self.diff.update(cx, |diff, cx| diff.show_message(None, tr!("No history"), cx));
            return;
        };
        self.selected = Some(ix);
        let repo = self.model.read(cx).repo.clone();
        let title = format!("{}  ({})", revision.path, revision.commit.short_id());
        self.diff.update(cx, |diff, cx| {
            diff.load(title, move || repo.revision_diff(&revision.commit.id, &revision.path), cx)
        });
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
                                let (m, i, p) = (model.clone(), id.clone(), path.clone());
                                menu.item(PopupMenuItem::new(tr!("Annotate at This Revision")).on_click(
                                    move |_, window, cx| {
                                        annotate(model.clone(), id.clone(), path.clone(), window, cx)
                                    },
                                ))
                                .item(PopupMenuItem::new(tr!("Compare with Local")).on_click(move |_, window, cx| {
                                    crate::worktree_diff::open_between(
                                        m.clone(),
                                        i.clone(),
                                        None,
                                        Some(p.clone()),
                                        window,
                                        cx,
                                    )
                                }))
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
                        tr!("Loading…").to_string()
                    } else {
                        tr!("{} revisions of {}", count, self.path)
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
                    .child(resizable_panel().child(crate::ui::cached(self.diff.clone()))),
            )
    }
}

struct AnnotateView {
    model: Entity<RepoModel>,
    /// The revision and path shown; "Annotate Previous Revision" changes them.
    rev: String,
    path: String,
    lines: Rc<Vec<BlameLine>>,
    /// Commit ids ordered from oldest to newest, for age shading.
    ages: Rc<std::collections::HashMap<String, f32>>,
    /// The line clicked, whose commit the bar below describes.
    selected: Option<usize>,
    loading: bool,
    _task: Option<Task<()>>,
}

impl AnnotateView {
    fn new(model: Entity<RepoModel>, rev: String, path: String, cx: &mut Context<Self>) -> Self {
        let mut this = Self {
            model,
            rev: String::new(),
            path: String::new(),
            lines: Rc::default(),
            ages: Rc::default(),
            selected: None,
            loading: true,
            _task: None,
        };
        this.load(rev, path, None, cx);
        this
    }

    /// Blame `path` at `rev`, then select line `select` (from 1) if given.
    fn load(&mut self, rev: String, path: String, select: Option<usize>, cx: &mut Context<Self>) {
        let repo = self.model.read(cx).repo.clone();
        self.rev = rev.clone();
        self.path = path.clone();
        self.loading = true;
        self.selected = None;
        cx.notify();
        self._task = Some(cx.spawn(async move |this, cx| {
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
                        this.selected = select.and_then(|line| line.checked_sub(1)).filter(|&ix| ix < this.lines.len());
                    }
                    Err(err) => report(&this.model, err, cx),
                }
                cx.notify();
            })
            .ok();
        }));
    }

    /// Blame the file as it was before the selected line's commit, at the
    /// same line, the way JetBrains steps back through a line's history.
    fn annotate_previous(&mut self, cx: &mut Context<Self>) {
        let Some(line) = self.selected.and_then(|ix| self.lines.get(ix)) else { return };
        let Some((rev, path)) = line.previous.clone() else { return };
        let at = line.orig_line;
        self.load(rev, path, Some(at), cx);
    }

    /// What can be done with the selected line's commit.
    fn render_line_bar(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let line = self.selected.and_then(|ix| self.lines.get(ix))?.clone();
        let web = self.model.read(cx).web.clone();
        let muted = cx.theme().muted_foreground;
        let diff_from = line.previous.as_ref().map(|(id, _)| id.clone());
        Some(
            h_flex()
                .gap_2()
                .text_sm()
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .child(format!("{}  {}", short(&line.id), line.summary)),
                )
                .child(div().flex_none().text_xs().text_color(muted).child(format!("{}, {}", line.author, format_time(line.time))))
                .child(
                    Button::new("annotate-previous")
                        .label(tr!("Annotate Previous Revision"))
                        .xsmall()
                        .disabled(line.previous.is_none())
                        .on_click(cx.listener(|this, _, _, cx| this.annotate_previous(cx))),
                )
                .child({
                    let (model, id, path) = (self.model.clone(), line.id.clone(), line.path.clone());
                    Button::new("annotate-diff")
                        .label(tr!("Show Diff"))
                        .xsmall()
                        .disabled(diff_from.is_none())
                        .on_click(move |_, window, cx| {
                            if let Some(from) = diff_from.clone() {
                                crate::worktree_diff::open_between(model.clone(), from, Some(id.clone()), Some(path.clone()), window, cx)
                            }
                        })
                })
                .when_some(web, |row, web| {
                    let url = web.file_url(&line.id, &line.path, Some(line.orig_line));
                    row.child(
                        Button::new("annotate-web")
                            .label(tr!("Open on {}", web.name()))
                            .xsmall()
                            .on_click(move |_, _, cx| cx.open_url(&url)),
                    )
                })
                .child({
                    let id = line.id.clone();
                    Button::new("annotate-copy")
                        .label(tr!("Copy Revision Number"))
                        .xsmall()
                        .on_click(move |_, _, cx| cx.write_to_clipboard(ClipboardItem::new_string(id.clone())))
                })
                .into_any_element(),
        )
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
                        let same_commit = this.selected.is_some_and(|sel| this.lines[sel].id == line.id);
                        h_flex()
                            .id(ix)
                            .w_full()
                            .h(px(20.))
                            .text_sm()
                            .when(same_commit, |row| row.bg(selection_bg().opacity(0.5)))
                            .when(this.selected == Some(ix), |row| row.bg(selection_bg()))
                            .on_click(cx.listener(move |this, _, _, cx| {
                                this.selected = Some(ix);
                                cx.notify();
                            }))
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

        let bar = self.render_line_bar(cx);
        v_flex()
            .gap_2()
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(tr!("{} at {}. Click a line to see its commit.", self.path, short(&self.rev))),
            )
            .child(
                div()
                    .h(px(600.))
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .overflow_hidden()
                    .when(self.loading, |el| el.child(div().p_3().text_sm().child(tr!("Loading…"))))
                    .child(list),
            )
            .children(bar)
    }
}
