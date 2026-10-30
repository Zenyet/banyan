//! The commands that have (or can be given) a keyboard shortcut: what Find
//! Action lists, what Settings › Keyboard Shortcuts lets you change, and
//! the bindings themselves.
//!
//! Keys are written the way GPUI parses them, with `secondary-` for ⌘ on
//! macOS and Ctrl elsewhere, e.g. `secondary-shift-k`; a space separates
//! the strokes of a chord.

use std::collections::HashMap;
use std::rc::Rc;
use std::sync::Mutex;

use gpui_kit::*;

use crate::i18n::tr;
use crate::settings::Settings;
use crate::{repo_window, workspace};

pub struct Command {
    /// Its name in the settings file.
    pub id: &'static str,
    pub label: &'static str,
    /// The shortcut out of the box; empty for none.
    pub default_keys: &'static str,
    /// The key context it works in; `None` anywhere in the app.
    pub context: Option<&'static str>,
    pub action: fn() -> Box<dyn Action>,
}

impl Command {
    /// The shortcut in effect: the user's, else the default.
    pub fn keys(&self) -> String {
        Settings::read(|s| s.shortcuts.iter().find(|(id, _)| id == self.id).map(|(_, keys)| keys.clone()))
            .unwrap_or_else(|| self.default_keys.to_string())
    }
}

pub fn commands() -> Vec<Command> {
    fn command(
        id: &'static str,
        label: &'static str,
        default_keys: &'static str,
        context: Option<&'static str>,
        action: fn() -> Box<dyn Action>,
    ) -> Command {
        Command { id, label, default_keys, context, action }
    }
    let ws = Some("Workspace");
    let rw = Some("RepoWindow");
    vec![
        command("find_action", tr!("Find Action…"), "secondary-shift-a", ws, || Box::new(workspace::FindAction)),
        command("commit", tr!("Commit…"), "secondary-k", ws, || Box::new(workspace::CommitChanges)),
        command("push", tr!("Push…"), "secondary-shift-k", ws, || Box::new(workspace::PushCommits)),
        command("update", tr!("Update Project"), "secondary-t", ws, || Box::new(workspace::UpdateProject)),
        command("fetch", tr!("Fetch"), "", ws, || Box::new(workspace::FetchRemotes)),
        command("apply_patch", tr!("Apply Patch…"), "", ws, || Box::new(workspace::ApplyPatch)),
        command("worktrees", tr!("Manage Worktrees…"), "", ws, || Box::new(workspace::ManageWorktrees)),
        command("submodules", tr!("Submodules…"), "", ws, || Box::new(workspace::ShowSubmodules)),
        command("show_log", tr!("Show Log"), "secondary-9", ws, || Box::new(workspace::ShowLog)),
        command("show_commit", tr!("Show Commit"), "secondary-0", ws, || Box::new(workspace::ShowCommitTab)),
        command("show_console", tr!("Show Console"), "", ws, || Box::new(workspace::ShowConsole)),
        command("search_log", tr!("Search the Log"), "secondary-f", ws, || Box::new(workspace::FindInLog)),
        command("go_to", tr!("Go to Hash, Branch or Tag"), "secondary-g", ws, || Box::new(workspace::GoToRevision)),
        command("refresh", tr!("Refresh"), "secondary-r", ws, || Box::new(workspace::RefreshRepository)),
        command("next_repo", tr!("Next Repository"), "secondary-shift-]", rw, || Box::new(repo_window::NextRepository)),
        command("previous_repo", tr!("Previous Repository"), "secondary-shift-[", rw, || Box::new(repo_window::PreviousRepository)),
        command("fetch_all", tr!("Fetch All Repositories"), "", rw, || Box::new(repo_window::FetchAllRepositories)),
        command("update_all", tr!("Update All Repositories"), "", rw, || Box::new(repo_window::UpdateAllRepositories)),
        command("open", tr!("Open Repository…"), "secondary-o", None, || Box::new(crate::OpenRepository)),
        command("settings", tr!("Settings…"), "secondary-,", None, || Box::new(crate::OpenSettings)),
        command("close_window", tr!("Close Window"), "secondary-w", None, || Box::new(crate::CloseWindow)),
        command("quit", tr!("Quit Banyan"), "secondary-q", None, || Box::new(crate::Quit)),
    ]
}

/// `keys` for `KeyBinding::new`, or `None` if they don't parse.
pub fn platform_keys(keys: &str) -> Option<String> {
    let keys = keys.split_whitespace().map(|stroke| {
        let stroke = stroke.to_lowercase();
        match stroke.strip_prefix("secondary-") {
            Some(rest) => format!("{}{rest}", if cfg!(target_os = "macos") { "cmd-" } else { "ctrl-" }),
            None => stroke,
        }
    });
    let keys: Vec<String> = keys.collect();
    let valid = !keys.is_empty() && keys.iter().all(|stroke| Keystroke::parse_with_os(stroke, OperatingSystem::Mac).is_ok());
    valid.then(|| keys.join(" "))
}

/// The keys bound to each command now, to unbind when they change.
static BOUND: Mutex<Option<HashMap<&'static str, String>>> = Mutex::new(None);

/// Bind every command's shortcut; again after one changes. Bindings made
/// later win, so a changed shortcut rebinds the old keys to nothing.
pub fn apply(cx: &mut App) {
    let mut bound = BOUND.lock().unwrap();
    let bound = bound.get_or_insert_with(HashMap::new);
    let mut bindings = Vec::new();
    for command in commands() {
        let keys = command.keys();
        let previous = bound.get(command.id);
        if previous == Some(&keys) {
            continue;
        }
        let context = command.context.and_then(|c| KeyBindingContextPredicate::parse(c).ok()).map(Rc::new);
        let binding = |keys: &str, action: Box<dyn Action>| {
            KeyBinding::load(keys, action, context.clone(), false, None, cx).ok()
        };
        if let Some(old) = previous.and_then(|old| platform_keys(old)) {
            bindings.extend(binding(&old, Box::new(NoAction)));
        }
        match platform_keys(&keys) {
            Some(new) => bindings.extend(binding(&new, (command.action)())),
            None if !keys.trim().is_empty() => eprintln!("ignoring the shortcut {keys:?} for {}", command.id),
            None => {}
        }
        bound.insert(command.id, keys);
    }
    cx.bind_keys(bindings);
}

/// How `keys` read on this platform, e.g. ⇧⌘K or Ctrl+Shift+K.
pub fn display(keys: &str) -> String {
    keys.split_whitespace().map(display_stroke).collect::<Vec<_>>().join(" ")
}

fn display_stroke(stroke: &str) -> String {
    let mac = cfg!(target_os = "macos");
    let mut modifiers: Vec<&str> = Vec::new();
    let parts: Vec<&str> = stroke.split('-').collect();
    let (key, mods) = match parts.split_last() {
        // "secondary--" binds the minus key.
        Some((&"", rest)) => ("-", &rest[..rest.len().saturating_sub(1)]),
        Some((last, rest)) => (*last, rest),
        None => return String::new(),
    };
    for m in mods {
        modifiers.push(match (*m, mac) {
            ("secondary", true) | ("cmd", true) => "⌘",
            ("secondary", false) | ("ctrl", false) => "Ctrl",
            ("cmd", false) => "Super",
            ("ctrl", true) => "⌃",
            ("shift", true) => "⇧",
            ("shift", false) => "Shift",
            ("alt", true) => "⌥",
            ("alt", false) => "Alt",
            (other, _) => other,
        });
    }
    let key = match key {
        "enter" if mac => "↩".to_string(),
        "enter" => "Enter".to_string(),
        "up" => "↑".into(),
        "down" => "↓".into(),
        "left" => "←".into(),
        "right" => "→".into(),
        "pageup" => "PgUp".into(),
        "pagedown" => "PgDn".into(),
        "home" => "Home".into(),
        "end" => "End".into(),
        "escape" => "Esc".into(),
        other => other.to_uppercase(),
    };
    if mac {
        // macOS lists ⌃ ⌥ ⇧ ⌘ in that order.
        modifiers.sort_by_key(|m| match *m { "⌃" => 0, "⌥" => 1, "⇧" => 2, _ => 3 });
        format!("{}{key}", modifiers.concat())
    } else {
        modifiers.push(&key);
        modifiers.join("+")
    }
}

#[cfg(test)]
mod tests {
    // Not `super::*`: GPUI's own `test` attribute would come with it.
    use super::{commands, display, platform_keys};

    #[test]
    fn default_shortcuts_parse_and_are_distinct() {
        let mut seen = std::collections::HashSet::new();
        for command in commands() {
            if command.default_keys.is_empty() {
                continue;
            }
            assert!(platform_keys(command.default_keys).is_some(), "{}", command.id);
            assert!(seen.insert(command.default_keys), "{} is bound twice", command.default_keys);
        }
    }

    #[test]
    fn reads_keys_written_by_hand() {
        let primary = if cfg!(target_os = "macos") { "cmd" } else { "ctrl" };
        assert_eq!(platform_keys("secondary-shift-K").unwrap(), format!("{primary}-shift-k"));
        assert_eq!(platform_keys("ctrl-alt-p").as_deref(), Some("ctrl-alt-p"));
        assert_eq!(platform_keys("secondary-k secondary-c").unwrap(), format!("{primary}-k {primary}-c"));
        assert_eq!(platform_keys("  "), None);
        assert_eq!(platform_keys("hyper-k"), None);
        if cfg!(target_os = "macos") {
            assert_eq!(display("secondary-shift-k"), "⇧⌘K");
            assert_eq!(display("alt-secondary-enter"), "⌥⌘↩");
        } else {
            assert_eq!(display("secondary-shift-k"), "Ctrl+Shift+K");
        }
    }
}
