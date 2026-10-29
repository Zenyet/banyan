//! "Compare with Current": commits a branch has that HEAD lacks, and the
//! other way round.

use gpui_kit::component::{
    ActiveTheme as _, Sizable as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::*;

use crate::commit_browser::CommitBrowser;
use crate::git::Commit;
use crate::model::RepoModel;
use crate::i18n::tr;

pub fn open(model: Entity<RepoModel>, branch: String, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| CompareView::new(model, branch.clone(), cx));
    window.open_dialog(cx, move |dialog, _, _| {
        dialog
            .title(tr!("Compare {} with Current Branch", branch))
            .w(px(1200.))
            .child(view.clone())
    });
}

struct CompareView {
    branch: String,
    current: String,
    incoming: Vec<Commit>,
    outgoing: Vec<Commit>,
    showing_incoming: bool,
    browser: Entity<CommitBrowser>,
    loading: bool,
    _task: Task<()>,
}

impl CompareView {
    fn new(model: Entity<RepoModel>, branch: String, cx: &mut Context<Self>) -> Self {
        let current = model
            .read(cx)
            .current_branch
            .clone()
            .unwrap_or_else(|| "HEAD".into());
        let browser = cx.new(|cx| CommitBrowser::new(model.clone(), cx));
        let repo = model.read(cx).repo.clone();
        let target = branch.clone();
        let task = cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { repo.compare_with_head(&target) })
                .await;
            this.update(cx, |this, cx| {
                this.loading = false;
                match result {
                    Ok((incoming, outgoing)) => {
                        this.incoming = incoming;
                        this.outgoing = outgoing;
                        this.show(true, cx);
                    }
                    Err(err) => this.browser.update(cx, |browser, cx| {
                        browser.set_commits(Vec::new(), format!("{err:#}"), cx)
                    }),
                }
            })
            .ok();
        });
        Self {
            branch,
            current,
            incoming: Vec::new(),
            outgoing: Vec::new(),
            showing_incoming: true,
            browser,
            loading: true,
            _task: task,
        }
    }

    fn show(&mut self, incoming: bool, cx: &mut Context<Self>) {
        self.showing_incoming = incoming;
        let (commits, empty) = if incoming {
            (
                self.incoming.clone(),
                tr!("{} has no commits that {} lacks", self.branch, self.current),
            )
        } else {
            (
                self.outgoing.clone(),
                tr!("{} has no commits that {} lacks", self.current, self.branch),
            )
        };
        self.browser.update(cx, |browser, cx| browser.set_commits(commits, empty, cx));
        cx.notify();
    }
}

impl Render for CompareView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let incoming_label = tr!(
            "In {} but not in {} ({})",
            self.branch,
            self.current,
            self.incoming.len()
        );
        let outgoing_label = tr!(
            "In {} but not in {} ({})",
            self.current,
            self.branch,
            self.outgoing.len()
        );
        v_flex()
            .gap_3()
            .child(
                h_flex()
                    .gap_2()
                    .child(
                        Button::new("compare-incoming")
                            .label(incoming_label)
                            .small()
                            .ghost()
                            .toggled(self.showing_incoming)
                            .on_click(cx.listener(|this, _, _, cx| this.show(true, cx))),
                    )
                    .child(
                        Button::new("compare-outgoing")
                            .label(outgoing_label)
                            .small()
                            .ghost()
                            .toggled(!self.showing_incoming)
                            .on_click(cx.listener(|this, _, _, cx| this.show(false, cx))),
                    )
                    .child(div().flex_1())
                    .child(
                        div()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(if self.loading { tr!("Loading…") } else { "" }),
                    ),
            )
            .child(
                div()
                    .h(px(600.))
                    .border_1()
                    .border_color(cx.theme().border)
                    .rounded(cx.theme().radius)
                    .overflow_hidden()
                    .child(self.browser.clone()),
            )
    }
}
