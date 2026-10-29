//! Conflict resolution dialog, like JetBrains' "Merge Revisions" window:
//! yours on the left, theirs on the right and the result in the middle.
//! Each conflict takes yours (»), theirs («), both, or text typed in.

use std::collections::{HashMap, HashSet};
use std::ops::Range;

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Icon, Rope, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    diff::DiffFile,
    h_flex,
    highlighter::{HighlightTheme, SyntaxHighlighter},
    input::{Textarea, TextareaState},
    v_flex,
};
use gpui_kit::assets::IconName;
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::conflict::{self, Conflict, Resolution, Segment};
use crate::model::{RepoEvent, RepoModel};

const LINE_HEIGHT: Pixels = px(20.);
const GUTTER: Pixels = px(30.);
/// Clean runs longer than this are folded down to their edges.
const FOLD_OVER: usize = 10;
const FOLD_CONTEXT: usize = 3;

pub fn open(model: Entity<RepoModel>, path: String, window: &mut Window, cx: &mut App) {
    let (repo, operation) = {
        let model = model.read(cx);
        (model.repo.clone(), model.operation.clone())
    };
    let text = match repo.merge_conflict_text(&path) {
        Ok(text) => text,
        Err(err) => {
            model.update(cx, |_, cx| cx.emit(RepoEvent::OpFailed(format!("{err:#}").into())));
            return;
        }
    };
    let (ours, theirs) = operation
        .map(|op| (op.ours, op.theirs))
        .unwrap_or_else(|| ("yours".into(), "theirs".into()));
    let segments = conflict::parse(&text);
    let theme = cx.theme().highlight_theme.clone();
    let tool = cx.new(|_| MergeTool::new(model, path.clone(), ours, theirs, segments, theme));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title(format!("Merge Revisions for {path}"))
            .w(px(1280.))
            .child(tool.clone())
    });
}

struct MergeTool {
    model: Entity<RepoModel>,
    path: String,
    ours: SharedString,
    theirs: SharedString,
    segments: Vec<Segment>,
    /// One per conflict, in order.
    resolutions: Vec<Option<Resolution>>,
    /// The conflict whose result is being typed.
    editing: Option<(usize, Entity<TextareaState>)>,
    /// Folded clean segments the user opened.
    unfolded: HashSet<usize>,
    /// The conflict the arrows last moved to.
    current: usize,
    scroll: ScrollHandle,
    highlighter: SyntaxHighlighter,
    theme: std::sync::Arc<HighlightTheme>,
    /// Syntax styles of each line shown, by its text.
    styles: HashMap<String, Styles>,
}

type Styles = Vec<(Range<usize>, HighlightStyle)>;

/// A line as shown: without its ending, tabs as spaces.
fn shown(line: &str) -> String {
    conflict::display(line).replace('\t', "    ")
}

impl MergeTool {
    fn new(
        model: Entity<RepoModel>,
        path: String,
        ours: String,
        theirs: String,
        segments: Vec<Segment>,
        theme: std::sync::Arc<HighlightTheme>,
    ) -> Self {
        let conflicts = segments.iter().filter(|s| matches!(s, Segment::Conflict(_))).count();
        let language = DiffFile::parse_conflicts(path.clone(), "").map(|f| f.language()).unwrap_or_else(|_| "text".into());
        let mut this = Self {
            model,
            path,
            ours: ours.into(),
            theirs: theirs.into(),
            segments,
            resolutions: vec![None; conflicts],
            editing: None,
            unfolded: HashSet::new(),
            current: 0,
            scroll: ScrollHandle::new(),
            highlighter: SyntaxHighlighter::new(&language),
            theme,
            styles: HashMap::new(),
        };
        let blocks: Vec<Vec<String>> = this
            .segments
            .iter()
            .flat_map(|segment| match segment {
                Segment::Clean(lines) => vec![lines.clone()],
                Segment::Conflict(c) => vec![c.ours.clone(), c.base.clone(), c.theirs.clone()],
            })
            .collect();
        for block in &blocks {
            this.highlight(block);
        }
        this
    }

    /// Remember the syntax styles of `lines`, highlighted together so that
    /// strings and comments spanning lines come out right.
    fn highlight(&mut self, lines: &[String]) {
        let lines: Vec<String> = lines.iter().map(|line| shown(line)).collect();
        let text = lines.join("\n");
        self.highlighter.update(None, &Rope::from_str(&text), None);
        let styles = self.highlighter.styles(&(0..text.len()), &*self.theme);
        let mut start = 0;
        for line in lines {
            let end = start + line.len();
            let line_styles = styles
                .iter()
                .filter(|(range, _)| range.start < end && range.end > start)
                .map(|(range, style)| (range.start.max(start) - start..range.end.min(end) - start, *style))
                .collect();
            self.styles.entry(line).or_insert(line_styles);
            start = end + 1;
        }
    }

    fn resolved_count(&self) -> usize {
        self.resolutions.iter().filter(|r| r.is_some()).count()
    }

    /// Take one side. Taking the other side of a conflict that already took
    /// one keeps both, in the order they were taken, as JetBrains does.
    fn accept(&mut self, conflict: usize, ours: bool, cx: &mut Context<Self>) {
        let resolution = match (&self.resolutions[conflict], ours) {
            (Some(Resolution::Theirs), true) => Resolution::TheirsThenOurs,
            (Some(Resolution::Ours), false) => Resolution::OursThenTheirs,
            (_, true) => Resolution::Ours,
            (_, false) => Resolution::Theirs,
        };
        self.set(conflict, Some(resolution), cx);
    }

    fn set(&mut self, conflict: usize, resolution: Option<Resolution>, cx: &mut Context<Self>) {
        if self.editing.as_ref().is_some_and(|(ix, _)| *ix == conflict) {
            self.editing = None;
        }
        self.resolutions[conflict] = resolution;
        cx.notify();
    }

    fn accept_all(&mut self, ours: bool, cx: &mut Context<Self>) {
        self.editing = None;
        let resolution = if ours { Resolution::Ours } else { Resolution::Theirs };
        self.resolutions.fill(Some(resolution));
        cx.notify();
    }

    fn edit(&mut self, conflict: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(Segment::Conflict(c)) = self.conflict_segment(conflict) else { return };
        let lines = match &self.resolutions[conflict] {
            Some(resolution) => c.resolved(resolution),
            None => c.resolved(&Resolution::OursThenTheirs),
        };
        let text: String = lines.concat();
        let state = cx.new(|cx| TextareaState::new(window, cx).auto_grow(3, 20));
        state.update(cx, |state, cx| state.set_value(text.trim_end_matches('\n').to_string(), window, cx));
        state.focus_handle(cx).focus(window, cx);
        self.editing = Some((conflict, state));
        cx.notify();
    }

    fn finish_edit(&mut self, cx: &mut Context<Self>) {
        if let Some((conflict, state)) = self.editing.take() {
            let text = state.read(cx).value().to_string();
            self.highlight(&conflict::split_lines(&text));
            self.resolutions[conflict] = Some(Resolution::Custom(text));
        }
        cx.notify();
    }

    fn conflict_segment(&self, conflict: usize) -> Option<&Segment> {
        self.segments.iter().filter(|s| matches!(s, Segment::Conflict(_))).nth(conflict)
    }

    /// Scroll to the next or previous conflict, preferring unresolved ones.
    fn go(&mut self, forward: bool, cx: &mut Context<Self>) {
        let total = self.resolutions.len();
        if total == 0 {
            return;
        }
        let order: Vec<usize> = (1..=total)
            .map(|step| if forward { (self.current + step) % total } else { (self.current + total - step) % total })
            .collect();
        let target = order
            .iter()
            .copied()
            .find(|&ix| self.resolutions[ix].is_none())
            .unwrap_or(order[0]);
        self.current = target;
        if let Some(segment) = self
            .segments
            .iter()
            .enumerate()
            .filter(|(_, s)| matches!(s, Segment::Conflict(_)))
            .nth(target)
            .map(|(ix, _)| ix)
        {
            self.scroll.scroll_to_item(segment);
        }
        cx.notify();
    }

    fn apply(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.finish_edit(cx);
        let Some(text) = conflict::result_text(&self.segments, &self.resolutions) else {
            return;
        };
        let path = self.path.clone();
        window.close_dialog(cx);
        self.model.update(cx, |model, cx| {
            model.run(
                format!("Resolve {path}"),
                move |repo| {
                    std::fs::write(repo.root().join(&path), text)?;
                    repo.resolve_conflict(&path, None)
                },
                cx,
            )
        });
    }

    /// Lines in `color`, syntax-highlighted unless muted. Long lines wrap;
    /// every column keeps the line-number gutter so they wrap alike.
    fn render_lines(&self, lines: &[String], numbers: Option<usize>, color: Hsla, cx: &App) -> Vec<AnyElement> {
        let highlight = color == cx.theme().foreground;
        lines
            .iter()
            .enumerate()
            .map(|(ix, line)| {
                let text = shown(line);
                let styles = highlight.then(|| self.styles.get(&text)).flatten().cloned();
                let content = match styles {
                    Some(styles) => StyledText::new(text).with_highlights(styles).into_any_element(),
                    None => text.into_any_element(),
                };
                h_flex()
                    .items_start()
                    .min_h(LINE_HEIGHT)
                    .line_height(LINE_HEIGHT)
                    .child(
                        div()
                            .flex_none()
                            .w(px(36.))
                            .pr_2()
                            .text_right()
                            .text_color(cx.theme().muted_foreground)
                            .when_some(numbers, |gutter, first| gutter.child((first + ix).to_string())),
                    )
                    .child(div().flex_1().min_w_0().pl_2().text_color(color).child(content))
                    .into_any_element()
            })
            .collect()
    }

    fn render_clean(&self, segment: usize, lines: &[String], first_line: usize, cx: &mut Context<Self>) -> AnyElement {
        let is_first = segment == 0;
        let is_last = segment + 1 == self.segments.len();
        let fold = lines.len() > FOLD_OVER && !self.unfolded.contains(&segment);
        let head = if is_first { 0 } else { FOLD_CONTEXT };
        let tail = if is_last { 0 } else { FOLD_CONTEXT };
        let muted = cx.theme().muted_foreground;
        let fg = cx.theme().foreground;
        let column = |lines: &[String], first: usize, numbers: bool, color: Hsla, cx: &App| {
            v_flex().children(self.render_lines(lines, numbers.then_some(first), color, cx))
        };
        let row = |left: Div, middle: Div, right: Div| {
            h_flex()
                .items_start()
                .child(left.flex_1().min_w_0())
                .child(div().flex_none().w(GUTTER))
                .child(middle.flex_1().min_w_0())
                .child(div().flex_none().w(GUTTER))
                .child(right.flex_1().min_w_0())
        };
        if !fold || head + tail >= lines.len() {
            return row(
                column(lines, first_line, false, muted, cx),
                column(lines, first_line, true, fg, cx),
                column(lines, first_line, false, muted, cx),
            )
            .into_any_element();
        }
        let hidden = lines.len() - head - tail;
        let top = &lines[..head];
        let bottom = &lines[lines.len() - tail..];
        let bottom_first = first_line + head + hidden;
        v_flex()
            .child(row(
                column(top, first_line, false, muted, cx),
                column(top, first_line, true, fg, cx),
                column(top, first_line, false, muted, cx),
            ))
            .child(
                div()
                    .id(SharedString::from(format!("unfold-{segment}")))
                    .h(LINE_HEIGHT)
                    .flex()
                    .items_center()
                    .justify_center()
                    .bg(cx.theme().muted)
                    .text_xs()
                    .text_color(muted)
                    .cursor_pointer()
                    .hover(|s| s.text_color(cx.theme().foreground))
                    .child(format!("⋯ {hidden} lines without conflicts"))
                    .on_click(cx.listener(move |this, _, _, cx| {
                        this.unfolded.insert(segment);
                        cx.notify();
                    })),
            )
            .child(row(
                column(bottom, bottom_first, false, muted, cx),
                column(bottom, bottom_first, true, fg, cx),
                column(bottom, bottom_first, false, muted, cx),
            ))
            .into_any_element()
    }

    fn render_conflict(&self, conflict: usize, c: &Conflict, first_line: usize, cx: &mut Context<Self>) -> AnyElement {
        let resolution = self.resolutions[conflict].clone();
        let theme = cx.theme();
        let fg = theme.foreground;
        let muted = theme.muted_foreground;
        let ours_bg = hsla(0.58, 0.7, 0.5, 0.12);
        let theirs_bg = hsla(0.08, 0.8, 0.5, 0.12);
        let unresolved_bg = hsla(0.0, 0.75, 0.55, 0.12);
        let resolved_bg = hsla(0.36, 0.6, 0.45, 0.14);
        let (uses_ours, uses_theirs) = match &resolution {
            None => (true, true),
            Some(Resolution::Ours) => (true, false),
            Some(Resolution::Theirs) => (false, true),
            Some(Resolution::Custom(_)) => (false, false),
            Some(_) => (true, true),
        };
        let side = |lines: &[String], used: bool, bg: Hsla, cx: &App| {
            v_flex()
                .flex_1()
                .min_w_0()
                .py_1()
                .bg(bg)
                .opacity(if used { 1. } else { 0.45 })
                .when(lines.is_empty(), |col| {
                    col.child(div().h(LINE_HEIGHT).pl_2().italic().text_color(muted).child("(no lines)"))
                })
                .children(self.render_lines(lines, None, fg, cx))
        };
        let arrow = |id: &str, icon: IconName, tooltip: &'static str, ours: bool, cx: &mut Context<Self>| {
            div().flex_none().w(GUTTER).pt_1().flex().justify_center().child(
                Button::new(SharedString::from(format!("{id}-{conflict}")))
                    .icon(Icon::new(icon))
                    .ghost()
                    .xsmall()
                    .tooltip(tooltip)
                    .on_click(cx.listener(move |this, _, _, cx| this.accept(conflict, ours, cx))),
            )
        };

        let middle = match (&self.editing, &resolution) {
            (Some((ix, state)), _) if *ix == conflict => v_flex()
                .p_1()
                .gap_1()
                .bg(resolved_bg)
                .child(Textarea::new(state))
                .child(
                    h_flex()
                        .gap_1()
                        .justify_end()
                        .child(
                            Button::new(SharedString::from(format!("edit-cancel-{conflict}")))
                                .label("Cancel")
                                .ghost()
                                .xsmall()
                                .on_click(cx.listener(|this, _, _, cx| {
                                    this.editing = None;
                                    cx.notify();
                                })),
                        )
                        .child(
                            Button::new(SharedString::from(format!("edit-done-{conflict}")))
                                .label("Done")
                                .primary()
                                .xsmall()
                                .on_click(cx.listener(|this, _, _, cx| this.finish_edit(cx))),
                        ),
                ),
            (_, Some(resolution)) => v_flex()
                .bg(resolved_bg)
                .child(self.conflict_toolbar(conflict, true, cx))
                .children(self.render_lines(&c.resolved(resolution), Some(first_line), fg, cx))
                .when(c.resolved(resolution).is_empty(), |col| {
                    col.child(div().h(LINE_HEIGHT).pl_2().italic().text_color(muted).child("(no lines)"))
                }),
            (_, None) => v_flex()
                .bg(unresolved_bg)
                .child(self.conflict_toolbar(conflict, false, cx))
                .when(c.base.is_empty(), |col| {
                    col.child(div().h(LINE_HEIGHT).pl_2().italic().text_color(muted).child("(not in the common ancestor)"))
                })
                .children(self.render_lines(&c.base, None, muted, cx)),
        };

        h_flex()
            .items_stretch()
            .my_1()
            .child(side(&c.ours, uses_ours, ours_bg, cx))
            .child(arrow("take-ours", IconName::ChevronsRight, "Accept Yours", true, cx))
            .child(middle.flex_1().min_w_0())
            .child(arrow("take-theirs", IconName::ChevronsLeft, "Accept Theirs", false, cx))
            .child(side(&c.theirs, uses_theirs, theirs_bg, cx))
            .into_any_element()
    }

    fn conflict_toolbar(&self, conflict: usize, resolved: bool, cx: &mut Context<Self>) -> impl IntoElement {
        let muted = cx.theme().muted_foreground;
        h_flex()
            .h(px(24.))
            .px_1()
            .gap_1()
            .text_xs()
            .child(
                div()
                    .flex_1()
                    .pl_1()
                    .text_color(if resolved { cx.theme().success } else { cx.theme().danger })
                    .child(if resolved { "Resolved" } else { "Conflict · base shown" }),
            )
            .when(!resolved, |row| {
                row.child(
                    Button::new(SharedString::from(format!("both-{conflict}")))
                        .label("Accept Both")
                        .ghost()
                        .xsmall()
                        .tooltip("Yours, then theirs")
                        .on_click(cx.listener(move |this, _, _, cx| {
                            this.set(conflict, Some(Resolution::OursThenTheirs), cx)
                        })),
                )
            })
            .child(
                Button::new(SharedString::from(format!("edit-{conflict}")))
                    .icon(Icon::new(IconName::Pencil))
                    .ghost()
                    .xsmall()
                    .tooltip("Edit the Result")
                    .on_click(cx.listener(move |this, _, window, cx| this.edit(conflict, window, cx))),
            )
            .when(resolved, |row| {
                row.child(
                    Button::new(SharedString::from(format!("undo-{conflict}")))
                        .icon(Icon::new(IconName::Undo2).text_color(muted))
                        .ghost()
                        .xsmall()
                        .tooltip("Undo")
                        .on_click(cx.listener(move |this, _, _, cx| this.set(conflict, None, cx))),
                )
            })
    }

    fn render_header(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let title = |label: &str, name: &SharedString, cx: &App| {
            h_flex()
                .flex_1()
                .min_w_0()
                .gap_1()
                .text_sm()
                .child(div().flex_none().font_weight(FontWeight::SEMIBOLD).child(label.to_string()))
                .child(div().min_w_0().truncate().text_color(cx.theme().muted_foreground).child(name.clone()))
        };
        h_flex()
            .pb_1()
            .child(title("Yours", &self.ours, cx))
            .child(div().flex_none().w(GUTTER))
            .child(
                h_flex()
                    .flex_1()
                    .min_w_0()
                    .gap_1()
                    .child(div().text_sm().font_weight(FontWeight::SEMIBOLD).child("Result"))
                    .child(div().flex_1())
                    .child(
                        Button::new("prev-conflict")
                            .icon(Icon::new(IconName::ArrowUp))
                            .ghost()
                            .xsmall()
                            .tooltip("Previous Conflict")
                            .on_click(cx.listener(|this, _, _, cx| this.go(false, cx))),
                    )
                    .child(
                        Button::new("next-conflict")
                            .icon(Icon::new(IconName::ArrowDown))
                            .ghost()
                            .xsmall()
                            .tooltip("Next Conflict")
                            .on_click(cx.listener(|this, _, _, cx| this.go(true, cx))),
                    ),
            )
            .child(div().flex_none().w(GUTTER))
            .child(title("Theirs", &self.theirs, cx))
    }
}

impl Render for MergeTool {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let total = self.resolutions.len();
        let resolved = self.resolved_count();
        let done = resolved == total && self.editing.is_none();

        let mut rows = Vec::new();
        let mut line = 1;
        let mut conflict = 0;
        for (ix, segment) in self.segments.iter().enumerate() {
            match segment {
                Segment::Clean(lines) => {
                    rows.push(self.render_clean(ix, lines, line, cx));
                    line += lines.len();
                }
                Segment::Conflict(c) => {
                    rows.push(self.render_conflict(conflict, c, line, cx));
                    if let Some(resolution) = &self.resolutions[conflict] {
                        line += c.resolved(resolution).len();
                    }
                    conflict += 1;
                }
            }
        }

        let status = if total == 0 {
            "No conflicts left; the changes merged cleanly".to_string()
        } else {
            format!("{resolved} of {total} conflicts resolved")
        };
        v_flex()
            .gap_2()
            .child(self.render_header(cx))
            .child(
                div()
                    .id("merge-rows")
                    .max_h(px(580.))
                    .overflow_y_scroll()
                    .track_scroll(&self.scroll)
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .font_family(cx.theme().mono_font_family.clone())
                    .text_sm()
                    .children(rows),
            )
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Button::new("accept-yours")
                            .label("Accept Yours")
                            .small()
                            .tooltip("Take your side for every conflict")
                            .on_click(cx.listener(|this, _, _, cx| this.accept_all(true, cx))),
                    )
                    .child(
                        Button::new("accept-theirs")
                            .label("Accept Theirs")
                            .small()
                            .tooltip("Take their side for every conflict")
                            .on_click(cx.listener(|this, _, _, cx| this.accept_all(false, cx))),
                    )
                    .child(
                        div()
                            .flex_1()
                            .text_sm()
                            .text_color(if done { cx.theme().success } else { cx.theme().muted_foreground })
                            .child(status),
                    )
                    .child(
                        Button::new("merge-cancel")
                            .label("Cancel")
                            .small()
                            .on_click(|_, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("merge-apply")
                            .label("Apply")
                            .primary()
                            .small()
                            .disabled(!done)
                            .on_click(cx.listener(|this, _, window, cx| this.apply(window, cx))),
                    ),
            )
    }
}
