//! Browse stashes with their files and diffs, and pop, apply or drop them.

use gpui_kit::component::{
    ActiveTheme as _, Disableable as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::*;

use crate::commit_browser::CommitBrowser;
use crate::git::Stash;
use crate::model::{RepoEvent, RepoModel};
use crate::ui::{confirm, prompt_text};
use crate::i18n::tr;

pub fn open(model: Entity<RepoModel>, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| StashViewer::new(model, cx));
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Stashes")).w(px(1200.)).child(view.clone()))
}

struct StashViewer {
    model: Entity<RepoModel>,
    stashes: Vec<Stash>,
    browser: Entity<CommitBrowser>,
    _subscriptions: Vec<Subscription>,
}

impl StashViewer {
    fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        let browser = cx.new(|cx| CommitBrowser::new(model.clone(), cx));
        let subscriptions = vec![
            // Popping or dropping changes the list.
            cx.subscribe(&model, |this, _, event, cx| {
                if let RepoEvent::Changed = event {
                    this.reload(cx);
                }
            }),
            cx.observe(&browser, |_, _, cx| cx.notify()),
        ];
        let mut this = Self {
            model,
            stashes: Vec::new(),
            browser,
            _subscriptions: subscriptions,
        };
        this.reload(cx);
        this
    }

    fn reload(&mut self, cx: &mut Context<Self>) {
        self.stashes = self.model.read(cx).stashes.clone();
        let commits = self
            .stashes
            .iter()
            .map(|stash| {
                let mut commit = stash.commit.clone();
                commit.subject = format!("{}  {}", stash.name, stash.message);
                commit
            })
            .collect();
        self.browser
            .update(cx, |browser, cx| browser.set_commits(commits, tr!("No stashes"), cx));
        cx.notify();
    }

    fn selected(&self, cx: &App) -> Option<&Stash> {
        self.browser.read(cx).selected_index().and_then(|ix| self.stashes.get(ix))
    }

    fn run(&mut self, kind: StashOp, window: &mut Window, cx: &mut Context<Self>) {
        let Some(name) = self.selected(cx).map(|s| s.name.clone()) else { return };
        let model = self.model.clone();
        let run = move |_: &mut Window, cx: &mut App| {
            let name = name.clone();
            model.update(cx, |model, cx| {
                model.run(
                    format!("{} {name}", kind.label()),
                    move |repo| match kind {
                        StashOp::Pop => repo.stash_pop(&name),
                        StashOp::Apply => repo.stash_apply(&name),
                        StashOp::Drop => repo.stash_drop(&name),
                    },
                    cx,
                )
            });
        };
        if kind == StashOp::Drop {
            confirm(tr!("Drop Stash"), tr!("Drop this stash? This cannot be undone."), tr!("Drop"), run, window, cx);
        } else {
            run(window, cx);
        }
    }
}

impl StashViewer {
    fn unstash_as_branch(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(name) = self.selected(cx).map(|s| s.name.clone()) else { return };
        let model = self.model.clone();
        window.close_dialog(cx);
        prompt_text(
            tr!("Unstash as Branch"),
            tr!("Create Branch"),
            "",
            move |branch, _, cx| {
                let name = name.clone();
                model.update(cx, |m, cx| {
                    m.run(tr!("Unstash {} as {}", name, branch), move |repo| repo.stash_branch(&name, &branch), cx)
                });
            },
            window,
            cx,
        );
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum StashOp {
    Pop,
    Apply,
    Drop,
}

impl StashOp {
    fn label(self) -> &'static str {
        match self {
            StashOp::Pop => tr!("Pop"),
            StashOp::Apply => tr!("Apply"),
            StashOp::Drop => tr!("Drop"),
        }
    }
}

impl Render for StashViewer {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let has_selection = self.selected(cx).is_some();
        let button = |id: &'static str, op: StashOp, cx: &mut Context<Self>| {
            Button::new(id)
                .label(op.label())
                .small()
                .disabled(!has_selection)
                .on_click(cx.listener(move |this, _, window, cx| this.run(op, window, cx)))
        };
        v_flex()
            .gap_3()
            .child(
                div()
                    .h(px(600.))
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .overflow_hidden()
                    .child(self.browser.clone()),
            )
            .child(
                h_flex()
                    .gap_2()
                    .child(button("stash-pop", StashOp::Pop, cx).primary())
                    .child(button("stash-apply", StashOp::Apply, cx))
                    .child(button("stash-drop", StashOp::Drop, cx).danger())
                    .child(
                        Button::new("stash-branch")
                            .label(tr!("Unstash as Branch…"))
                            .small()
                            .disabled(!has_selection)
                            .tooltip(tr!("Check out a new branch where the stash was made and apply it there"))
                            .on_click(cx.listener(|this, _, window, cx| this.unstash_as_branch(window, cx))),
                    )
                    .child(div().flex_1())
                    .child(
                        Button::new("stash-close")
                            .label(tr!("Close"))
                            .small()
                            .on_click(|_, window, cx| window.close_dialog(cx)),
                    ),
            )
    }
}
