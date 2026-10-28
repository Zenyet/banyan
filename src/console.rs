//! The Git console, like JetBrains' "Console" tab: every git command run by a
//! user action, with its output. Background refreshes are not recorded.

use std::cell::Cell;
use std::collections::VecDeque;
use std::path::{Path, PathBuf};
use std::sync::{LazyLock, Mutex};

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Icon, Sizable as _,
    button::{Button, ButtonVariants as _},
    h_flex, v_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::model::RepoModel;
use crate::ui::format_full_time;

const MAX_ENTRIES: usize = 500;
const MAX_OUTPUT: usize = 8000;

#[derive(Clone)]
pub struct Entry {
    /// Directory git ran in.
    pub dir: PathBuf,
    pub command: String,
    pub output: String,
    pub ok: bool,
    pub at: i64,
    pub millis: u128,
}

static ENTRIES: LazyLock<Mutex<VecDeque<Entry>>> = LazyLock::new(Default::default);

thread_local! {
    static RECORDING: Cell<bool> = const { Cell::new(false) };
}

/// Record the git commands `f` runs on this thread.
pub fn recording<T>(f: impl FnOnce() -> T) -> T {
    let previous = RECORDING.replace(true);
    let result = f();
    RECORDING.set(previous);
    result
}

pub fn is_recording() -> bool {
    RECORDING.get()
}

pub fn record(dir: &Path, args: &[&str], stdout: &str, stderr: &str, ok: bool, millis: u128) {
    let mut output = [stdout.trim(), stderr.trim()]
        .into_iter()
        .filter(|text| !text.is_empty())
        .collect::<Vec<_>>()
        .join("\n");
    if output.len() > MAX_OUTPUT {
        let mut end = MAX_OUTPUT;
        while !output.is_char_boundary(end) {
            end -= 1;
        }
        output.truncate(end);
        output.push_str("\n…");
    }
    let entry = Entry {
        dir: dir.to_path_buf(),
        command: format!("git {}", args.join(" ")),
        output,
        ok,
        at: chrono::Local::now().timestamp(),
        millis,
    };
    let mut entries = ENTRIES.lock().unwrap();
    entries.push_back(entry);
    while entries.len() > MAX_ENTRIES {
        entries.pop_front();
    }
}

/// Entries run in `root`, plus the clone that created it.
pub fn entries_for(root: &Path) -> Vec<Entry> {
    let root_text = root.to_string_lossy();
    ENTRIES
        .lock()
        .unwrap()
        .iter()
        .filter(|e| e.dir == root || (e.command.starts_with("git clone") && e.command.ends_with(&*root_text)))
        .cloned()
        .collect()
}

pub fn clear(root: &Path) {
    ENTRIES.lock().unwrap().retain(|e| e.dir != root);
}

pub struct ConsoleView {
    model: Entity<RepoModel>,
    scroll: ScrollHandle,
    shown: usize,
    _subscription: Subscription,
}

impl ConsoleView {
    pub fn new(model: Entity<RepoModel>, cx: &mut Context<Self>) -> Self {
        // Operations finish with a model notification.
        let subscription = cx.observe(&model, |_, _, cx| cx.notify());
        Self {
            model,
            scroll: ScrollHandle::new(),
            shown: 0,
            _subscription: subscription,
        }
    }
}

impl Render for ConsoleView {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let root = self.model.read(cx).repo.root().to_path_buf();
        let entries = entries_for(&root);
        if entries.len() != self.shown {
            self.shown = entries.len();
            self.scroll.scroll_to_bottom();
        }
        let mono = cx.theme().mono_font_family.clone();
        let muted = cx.theme().muted_foreground;
        let rows = entries.iter().map(|entry| {
            v_flex()
                .px_3()
                .py_1()
                .gap_0p5()
                .border_b_1()
                .border_color(cx.theme().border)
                .child(
                    h_flex()
                        .gap_2()
                        .text_sm()
                        .child(
                            Icon::new(if entry.ok { IconName::Check } else { IconName::X })
                                .xsmall()
                                .text_color(if entry.ok { cx.theme().success } else { cx.theme().danger }),
                        )
                        .child(div().text_xs().text_color(muted).child(format_full_time(entry.at)))
                        .child(div().font_family(mono.clone()).child(entry.command.clone()))
                        .child(div().text_xs().text_color(muted).child(format!("{} ms", entry.millis))),
                )
                .when(!entry.output.is_empty(), |col| {
                    col.child(
                        div()
                            .pl_6()
                            .text_xs()
                            .font_family(mono.clone())
                            .text_color(if entry.ok { muted } else { cx.theme().danger })
                            .whitespace_normal()
                            .child(entry.output.clone()),
                    )
                })
        });
        v_flex()
            .size_full()
            .child(
                h_flex()
                    .flex_none()
                    .h(px(32.))
                    .px_2()
                    .gap_2()
                    .border_b_1()
                    .border_color(cx.theme().border)
                    .child(
                        div()
                            .flex_1()
                            .text_xs()
                            .text_color(muted)
                            .child(format!("{} commands", entries.len())),
                    )
                    .child(
                        Button::new("console-clear")
                            .icon(IconName::Trash)
                            .ghost()
                            .xsmall()
                            .tooltip("Clear")
                            .on_click(cx.listener(move |_, _, _, cx| {
                                clear(&root);
                                cx.notify();
                            })),
                    ),
            )
            .child(if entries.is_empty() {
                div()
                    .flex_1()
                    .flex()
                    .items_center()
                    .justify_center()
                    .text_sm()
                    .text_color(muted)
                    .child("Git commands run by your actions appear here")
                    .into_any_element()
            } else {
                div()
                    .id("console-entries")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .track_scroll(&self.scroll)
                    .children(rows)
                    .into_any_element()
            })
    }
}

#[cfg(test)]
mod tests {
    // Not a glob: gpui exports its own `test` attribute.
    use super::{clear, entries_for, is_recording, record, recording};
    use std::path::Path;

    #[test]
    fn records_only_inside_recording() {
        let dir = Path::new("/tmp/console-test-repo");
        assert!(!is_recording());
        recording(|| {
            assert!(is_recording());
            record(dir, &["push"], "", "Everything up-to-date", true, 12);
        });
        assert!(!is_recording());
        let entries = entries_for(dir);
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].command, "git push");
        assert_eq!(entries[0].output, "Everything up-to-date");
        clear(dir);
        assert!(entries_for(dir).is_empty());
    }
}
