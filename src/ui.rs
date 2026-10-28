//! Small presentation helpers shared by the views.

use std::collections::BTreeMap;
use std::rc::Rc;

use chrono::{Local, TimeZone as _};
use gpui_kit::component::{
    ActiveTheme as _, WindowExt as _,
    button::{Button, ButtonVariants as _},
    h_flex,
    input::{Input, InputEvent, InputState},
    tree::TreeItem,
    v_flex,
};
use gpui_kit::*;

use crate::git::RefKind;

pub const FILE_PREFIX: &str = "f:";

/// Graph lane colors, close to the JetBrains palette.
pub fn lane_color(ix: usize) -> Hsla {
    const COLORS: [u32; 10] = [
        0x4d8ee8, 0xe0a33d, 0x58b25a, 0xd85e8a, 0x9a6ad6, 0x2fb5b0, 0xe06c4b, 0x8fae3a, 0x6a7fe0,
        0xc78a4a,
    ];
    rgb(COLORS[ix % COLORS.len()]).into()
}

/// Selected row background, readable in both themes.
pub fn selection_bg() -> Hsla {
    let mut color: Hsla = rgb(0x4d8ee8).into();
    color.a = 0.22;
    color
}

pub fn status_color(status: char, cx: &App) -> Hsla {
    match status {
        'A' => rgb(0x58a65c).into(),
        'D' => cx.theme().muted_foreground,
        'R' | 'C' | 'M' | 'T' => rgb(0x4d8ee8).into(),
        '?' => rgb(0xc75450).into(),
        'U' => rgb(0xd5756c).into(),
        _ => cx.theme().foreground,
    }
}

pub fn ref_label(name: &str, kind: &RefKind, cx: &App) -> AnyElement {
    let (bg, fg): (Hsla, Hsla) = match kind {
        RefKind::Head => (rgb(0xf0c75e).into(), rgb(0x3c2f00).into()),
        RefKind::Local => (rgb(0x5f9e5f).into(), white()),
        RefKind::Remote => (rgb(0x8a6db8).into(), white()),
        RefKind::Tag => (cx.theme().secondary, cx.theme().secondary_foreground),
    };
    div()
        .flex_none()
        .px_1()
        .rounded(px(3.))
        .bg(bg)
        .text_color(fg)
        .text_xs()
        .child(SharedString::from(name.to_string()))
        .into_any_element()
}

pub fn format_time(timestamp: i64) -> String {
    let Some(time) = Local.timestamp_opt(timestamp, 0).single() else {
        return String::new();
    };
    let now = Local::now();
    let delta = now.signed_duration_since(time);
    if delta.num_seconds() < 0 {
        time.format("%Y/%m/%d %H:%M").to_string()
    } else if delta.num_minutes() < 1 {
        "Moments ago".into()
    } else if delta.num_hours() < 1 {
        format!("{} minutes ago", delta.num_minutes())
    } else if time.date_naive() == now.date_naive() {
        format!("Today {}", time.format("%H:%M"))
    } else if delta.num_days() < 2 {
        format!("Yesterday {}", time.format("%H:%M"))
    } else {
        time.format("%Y/%m/%d %H:%M").to_string()
    }
}

pub fn format_full_time(timestamp: i64) -> String {
    Local
        .timestamp_opt(timestamp, 0)
        .single()
        .map(|time| time.format("%Y/%m/%d %H:%M:%S").to_string())
        .unwrap_or_default()
}

#[derive(Default)]
struct DirNode {
    dirs: BTreeMap<String, DirNode>,
    files: Vec<(String, String)>,
}

/// Group paths by directory, collapsing single-child chains like JetBrains does.
/// File items get the id `f:<path>`.
pub fn file_tree<'a>(paths: impl IntoIterator<Item = &'a str>) -> Vec<TreeItem> {
    path_tree(paths, FILE_PREFIX)
}

/// Like [`file_tree`] with a custom id prefix for the leaves.
pub fn path_tree<'a>(paths: impl IntoIterator<Item = &'a str>, id_prefix: &str) -> Vec<TreeItem> {
    let mut root = DirNode::default();
    for path in paths {
        let mut node = &mut root;
        let mut parts: Vec<&str> = path.split('/').collect();
        let name = parts.pop().unwrap_or(path);
        for part in parts {
            node = node.dirs.entry(part.to_string()).or_default();
        }
        node.files.push((name.to_string(), path.to_string()));
    }
    to_items(root, "", id_prefix)
}

fn to_items(node: DirNode, prefix: &str, id_prefix: &str) -> Vec<TreeItem> {
    let mut items = Vec::new();
    for (mut name, mut child) in node.dirs {
        while child.files.is_empty() && child.dirs.len() == 1 {
            let (sub, grandchild) = child.dirs.into_iter().next().unwrap();
            name = format!("{name}/{sub}");
            child = grandchild;
        }
        let full = if prefix.is_empty() { name.clone() } else { format!("{prefix}/{name}") };
        let children = to_items(child, &full, id_prefix);
        items.push(
            TreeItem::new(format!("d:{id_prefix}{full}"), name)
                .children(children)
                .expanded(true),
        );
    }
    let mut files = node.files;
    files.sort();
    for (name, full) in files {
        items.push(TreeItem::new(format!("{id_prefix}{full}"), name));
    }
    items
}

/// Accepts the dialog's value; returns whether it was valid.
type SubmitFn = dyn Fn(&mut Window, &mut App) -> bool;

/// Ask for a line of text in a dialog.
pub fn prompt_text(
    title: impl Into<SharedString>,
    ok_label: impl Into<SharedString>,
    default_value: impl Into<SharedString>,
    on_ok: impl Fn(String, &mut Window, &mut App) + 'static,
    window: &mut Window,
    cx: &mut App,
) {
    let title = title.into();
    let ok_label = ok_label.into();
    let default_value = default_value.into();
    let input = cx.new(|cx| InputState::new(window, cx).default_value(default_value));
    // Returns whether the value was accepted.
    let submit: Rc<SubmitFn> = Rc::new({
        let input = input.clone();
        move |window, cx| {
            let value = input.read(cx).value().trim().to_string();
            if value.is_empty() {
                return false;
            }
            on_ok(value, window, cx);
            true
        }
    });
    // The input swallows Enter, so confirm from its event. The window is busy
    // dispatching the key at that point, so defer until it is free.
    let handle = window.window_handle();
    let on_enter = submit.clone();
    cx.subscribe(&input, move |_, event: &InputEvent, cx| {
        if let InputEvent::PressEnter { .. } = event {
            let on_enter = on_enter.clone();
            cx.defer(move |cx| {
                handle
                    .update(cx, |_, window, cx| {
                        if on_enter(window, cx) {
                            window.close_dialog(cx);
                        }
                    })
                    .ok();
            });
        }
    })
    .detach();
    let needs_focus = std::cell::Cell::new(true);
    window.open_dialog(cx, move |dialog, window, cx| {
        if needs_focus.replace(false) {
            input.focus_handle(cx).focus(window, cx);
        }
        let on_click = submit.clone();
        dialog
            .title(title.clone())
            .w(px(420.))
            .child(
                v_flex()
                    .gap_4()
                    .child(Input::new(&input))
                    .child(dialog_buttons(ok_label.clone(), false, move |_, window, cx| {
                        if on_click(window, cx) {
                            window.close_dialog(cx);
                        }
                    })),
            )
    });
}

/// Ask for confirmation before doing something destructive.
pub fn confirm(
    title: impl Into<SharedString>,
    message: impl Into<SharedString>,
    ok_label: impl Into<SharedString>,
    on_ok: impl Fn(&mut Window, &mut App) + 'static,
    window: &mut Window,
    cx: &mut App,
) {
    let title = title.into();
    let message = message.into();
    let ok_label = ok_label.into();
    let on_ok = Rc::new(on_ok);
    window.open_dialog(cx, move |dialog, _, cx| {
        let on_ok = on_ok.clone();
        dialog.title(title.clone()).w(px(440.)).child(
            v_flex()
                .gap_4()
                .child(
                    div()
                        .text_sm()
                        .text_color(cx.theme().muted_foreground)
                        .child(message.clone()),
                )
                .child(dialog_buttons(ok_label.clone(), true, move |_, window, cx| {
                    window.close_dialog(cx);
                    on_ok(window, cx);
                })),
        )
    });
}

fn dialog_buttons(
    ok_label: SharedString,
    danger: bool,
    on_ok: impl Fn(&ClickEvent, &mut Window, &mut App) + 'static,
) -> impl IntoElement {
    let ok = Button::new("dialog-ok").label(ok_label).on_click(on_ok);
    h_flex()
        .justify_end()
        .gap_2()
        .child(
            Button::new("dialog-cancel")
                .label("Cancel")
                .on_click(|_, window, cx| window.close_dialog(cx)),
        )
        .child(if danger { ok.danger() } else { ok.primary() })
}
