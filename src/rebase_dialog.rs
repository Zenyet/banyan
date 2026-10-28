//! "Interactively Rebase from Here": reorder, reword, squash, fixup or drop
//! commits, like JetBrains' rebase editor.

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    menu::{DropdownMenu as _, PopupMenuItem},
    v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::actions::short;
use crate::git::{Commit, RebaseAction};
use crate::model::{RepoEvent, RepoModel};
use crate::ui::{prompt_text, selection_bg};

pub fn open(model: Entity<RepoModel>, from: String, window: &mut Window, cx: &mut App) {
    let repo = model.read(cx).repo.clone();
    let from_for_load = from.clone();
    let handle = window.window_handle();
    cx.spawn(async move |cx| {
        let commits = cx
            .background_executor()
            .spawn(async move { repo.rebase_commits(&from_for_load) })
            .await;
        cx.update(|cx| match commits {
            Ok(commits) => {
                handle
                    .update(cx, |_, window, cx| {
                        let view = cx.new(|_| RebaseDialog::new(model, from, commits));
                        window.open_dialog(cx, move |dialog, _, _| {
                            dialog
                                .title("Interactive Rebase")
                                .w(px(860.))
                                .child(view.clone())
                        });
                    })
                    .ok();
            }
            Err(err) => model.update(cx, |_, cx| {
                cx.emit(RepoEvent::OpFailed(format!("Cannot rebase: {err:#}").into()))
            }),
        });
    })
    .detach();
}

struct Entry {
    commit: Commit,
    action: RebaseAction,
}

struct RebaseDialog {
    model: Entity<RepoModel>,
    from: String,
    /// Short id of the commit the rebased commits will sit on.
    onto: String,
    entries: Vec<Entry>,
    selected: usize,
}

impl RebaseDialog {
    fn new(model: Entity<RepoModel>, from: String, commits: Vec<Commit>) -> Self {
        let onto = commits
            .first()
            .and_then(|c| c.parents.first())
            .map(|p| short(p).to_string())
            .unwrap_or_else(|| "the root".into());
        Self {
            model,
            from,
            onto,
            entries: commits
                .into_iter()
                .map(|commit| Entry {
                    commit,
                    action: RebaseAction::Pick,
                })
                .collect(),
            selected: 0,
        }
    }

    fn set_action(&mut self, ix: usize, action: RebaseAction, cx: &mut Context<Self>) {
        if let Some(entry) = self.entries.get_mut(ix) {
            entry.action = action;
        }
        self.selected = ix;
        cx.notify();
    }

    fn move_selected(&mut self, delta: isize, cx: &mut Context<Self>) {
        let target = self.selected as isize + delta;
        if target < 0 || target as usize >= self.entries.len() {
            return;
        }
        self.entries.swap(self.selected, target as usize);
        self.selected = target as usize;
        cx.notify();
    }

    fn validation_error(&self) -> Option<&'static str> {
        let first_kept = self.entries.iter().find(|e| e.action != RebaseAction::Drop)?;
        if matches!(first_kept.action, RebaseAction::Squash | RebaseAction::Fixup) {
            return Some("The first commit cannot be squashed or fixed up");
        }
        None
    }

    fn is_unchanged(&self) -> bool {
        // The original order is oldest first with every commit picked.
        self.entries.iter().all(|e| e.action == RebaseAction::Pick)
            && self.entries.windows(2).all(|w| {
                w[1].commit.parents.first() == Some(&w[0].commit.id)
            })
    }

    fn start(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let plan: Vec<(String, RebaseAction)> = self
            .entries
            .iter()
            .map(|e| (e.commit.id.clone(), e.action.clone()))
            .collect();
        let from = self.from.clone();
        window.close_dialog(cx);
        self.model.update(cx, |model, cx| {
            model.run("Interactive rebase", move |repo| repo.interactive_rebase(&from, &plan), cx)
        });
    }

    fn render_row(&self, ix: usize, cx: &mut Context<Self>) -> AnyElement {
        let entry = &self.entries[ix];
        let selected = self.selected == ix;
        let view = cx.entity().downgrade();
        let (label, color): (&str, Hsla) = match entry.action {
            RebaseAction::Pick => ("Pick", cx.theme().foreground),
            RebaseAction::Reword(_) => ("Reword", rgb(0x4d8ee8).into()),
            RebaseAction::Squash => ("Squash", rgb(0x9a6ad6).into()),
            RebaseAction::Fixup => ("Fixup", rgb(0x9a6ad6).into()),
            RebaseAction::Drop => ("Drop", cx.theme().danger),
        };
        let dropped = entry.action == RebaseAction::Drop;
        let subject = match &entry.action {
            RebaseAction::Reword(message) => message.lines().next().unwrap_or_default().to_string(),
            _ => entry.commit.subject.clone(),
        };
        let original_subject = entry.commit.subject.clone();
        h_flex()
            .id(ix)
            .w_full()
            .h(px(30.))
            .px_2()
            .gap_2()
            .text_sm()
            .when(selected, |row| row.bg(selection_bg()))
            .when(!selected, |row| row.hover(|s| s.bg(cx.theme().list_hover)))
            .on_click(cx.listener(move |this, _, _, cx| {
                this.selected = ix;
                cx.notify();
            }))
            .child(
                Button::new(("action", ix))
                    .label(label)
                    .xsmall()
                    .ghost()
                    .w(px(90.))
                    .text_color(color)
                    .dropdown_caret(true)
                    .dropdown_menu(move |mut menu, _, _| {
                        for (name, action) in [
                            ("Pick", RebaseAction::Pick),
                            ("Squash", RebaseAction::Squash),
                            ("Fixup", RebaseAction::Fixup),
                            ("Drop", RebaseAction::Drop),
                        ] {
                            let view = view.clone();
                            menu = menu.item(PopupMenuItem::new(name).on_click(move |_, _, cx| {
                                let action = action.clone();
                                view.update(cx, |this, cx| this.set_action(ix, action, cx)).ok();
                            }));
                        }
                        let view = view.clone();
                        let original = original_subject.clone();
                        menu.item(PopupMenuItem::new("Reword...").on_click(move |_, window, cx| {
                            let view = view.clone();
                            prompt_text(
                                "Reword Commit",
                                "Reword",
                                original.clone(),
                                move |message, _, cx| {
                                    view.update(cx, |this, cx| {
                                        this.set_action(ix, RebaseAction::Reword(message), cx)
                                    })
                                    .ok();
                                },
                                window,
                                cx,
                            );
                        }))
                    }),
            )
            .child(
                div()
                    .flex_none()
                    .w(px(70.))
                    .text_color(cx.theme().muted_foreground)
                    .child(short(&entry.commit.id).to_string()),
            )
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .when(dropped, |el| el.line_through().text_color(cx.theme().muted_foreground))
                    .when(
                        matches!(entry.action, RebaseAction::Squash | RebaseAction::Fixup),
                        |el| el.pl_4(),
                    )
                    .child(subject),
            )
            .child(
                div()
                    .flex_none()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(entry.commit.author.clone()),
            )
            .into_any_element()
    }
}

impl Render for RebaseDialog {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let rows: Vec<AnyElement> = (0..self.entries.len()).map(|ix| self.render_row(ix, cx)).collect();
        let error = self.validation_error();
        let can_start = error.is_none() && !self.is_unchanged();
        v_flex()
            .gap_3()
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child(format!(
                        "{} commits, oldest first. Rebasing onto {}.",
                        self.entries.len(),
                        self.onto
                    )),
            )
            .child(
                div()
                    .id("rebase-entries")
                    .h(px(360.))
                    .overflow_y_scroll()
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .children(rows),
            )
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Button::new("move-up")
                            .icon(IconName::ArrowUp)
                            .small()
                            .tooltip("Move Up")
                            .on_click(cx.listener(|this, _, _, cx| this.move_selected(-1, cx))),
                    )
                    .child(
                        Button::new("move-down")
                            .icon(IconName::ArrowDown)
                            .small()
                            .tooltip("Move Down")
                            .on_click(cx.listener(|this, _, _, cx| this.move_selected(1, cx))),
                    )
                    .when_some(error, |row, error| {
                        row.child(div().text_sm().text_color(cx.theme().danger).child(error))
                    })
                    .child(div().flex_1())
                    .child(
                        Button::new("rebase-cancel")
                            .label("Cancel")
                            .small()
                            .on_click(|_, window, cx| window.close_dialog(cx)),
                    )
                    .child(
                        Button::new("rebase-start")
                            .label("Start Rebasing")
                            .primary()
                            .small()
                            .disabled(!can_start)
                            .on_click(cx.listener(|this, _, window, cx| this.start(window, cx))),
                    ),
            )
    }
}
