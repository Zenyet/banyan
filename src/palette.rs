//! "Find Action" (⇧⌘A): type part of a command's name and run it, like
//! JetBrains' action search.

use gpui_kit::component::{
    ActiveTheme as _, WindowExt as _,
    input::{Input, InputEvent, InputState},
    v_flex, h_flex,
};
use gpui_kit::prelude::FluentBuilder as _;
use gpui_kit::*;

use crate::i18n::tr;
use crate::keymap::{self, Command};
use crate::ui::selection_bg;

/// `workspace` is focused again before the command runs, so that actions
/// handled by the workspace reach it.
pub fn open(workspace: FocusHandle, window: &mut Window, cx: &mut App) {
    let view = cx.new(|cx| Palette::new(workspace, window, cx));
    let focus = view.read(cx).query.focus_handle(cx);
    window.open_dialog(cx, move |dialog, _, _| dialog.title(tr!("Find Action")).w(px(520.)).child(view.clone()));
    window.focus(&focus, cx);
}

struct Palette {
    workspace: FocusHandle,
    query: Entity<InputState>,
    commands: Vec<Command>,
    /// Indices into `commands` matching the query.
    matches: Vec<usize>,
    selected: usize,
}

impl Palette {
    fn new(workspace: FocusHandle, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let query = cx.new(|cx| InputState::new(window, cx).placeholder(tr!("Type a command")));
        cx.subscribe_in(&query, window, |this, _, event: &InputEvent, window, cx| match event {
            InputEvent::Change => this.update_matches(cx),
            // The input is still handling the key; run once it's done.
            InputEvent::PressEnter { .. } => cx.defer_in(window, |this, window, cx| this.run(this.selected, window, cx)),
            _ => {}
        })
        .detach();
        let commands = keymap::commands().into_iter().filter(|c| c.id != "find_action").collect::<Vec<_>>();
        let matches = (0..commands.len()).collect();
        Self { workspace, query, commands, matches, selected: 0 }
    }

    fn update_matches(&mut self, cx: &mut Context<Self>) {
        let query = self.query.read(cx).value().trim().to_lowercase();
        let words: Vec<&str> = query.split_whitespace().collect();
        self.matches = (0..self.commands.len())
            .filter(|&ix| {
                let label = self.commands[ix].label.to_lowercase();
                words.iter().all(|word| label.contains(word))
            })
            .collect();
        self.selected = 0;
        cx.notify();
    }

    fn move_selection(&mut self, delta: isize, cx: &mut Context<Self>) {
        if self.matches.is_empty() {
            return;
        }
        let last = self.matches.len() as isize - 1;
        self.selected = (self.selected as isize + delta).clamp(0, last) as usize;
        cx.notify();
    }

    /// Run the `row`th match.
    fn run(&mut self, row: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(command) = self.matches.get(row).map(|&ix| &self.commands[ix]) else { return };
        let action = (command.action)();
        let workspace = self.workspace.clone();
        window.close_dialog(cx);
        // After the dialog has gone, from the workspace.
        window.defer(cx, move |window, cx| {
            workspace.focus(window, cx);
            window.dispatch_action(action, cx);
        });
    }
}

impl Render for Palette {
    fn render(&mut self, _: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let muted = cx.theme().muted_foreground;
        v_flex()
            .gap_2()
            .text_sm()
            .capture_key_down(cx.listener(|this, event: &KeyDownEvent, _, cx| {
                match event.keystroke.key.as_str() {
                    "up" => this.move_selection(-1, cx),
                    "down" => this.move_selection(1, cx),
                    _ => return,
                }
                cx.stop_propagation();
            }))
            .child(Input::new(&self.query))
            .child(
                v_flex()
                    .id("palette-matches")
                    .max_h(px(360.))
                    .overflow_y_scroll()
                    .when(self.matches.is_empty(), |list| {
                        list.child(div().p_2().text_color(muted).child(tr!("No matching commands")))
                    })
                    .children(self.matches.iter().enumerate().map(|(row, &ix)| {
                        let command = &self.commands[ix];
                        let keys = command.keys();
                        h_flex()
                            .id(("palette-command", row))
                            .px_2()
                            .h(px(28.))
                            .rounded(px(4.))
                            .when(row == self.selected, |item| item.bg(selection_bg()))
                            .hover(|style| style.bg(cx.theme().list_hover))
                            .child(div().flex_1().child(command.label))
                            .when(!keys.is_empty(), |item| {
                                item.child(div().text_xs().text_color(muted).child(keymap::display(&keys)))
                            })
                            .on_click(cx.listener(move |this, _, window, cx| this.run(row, window, cx)))
                    })),
            )
            .child(div().text_xs().text_color(muted).child(tr!("↑ ↓ to choose, Enter to run")))
    }
}
