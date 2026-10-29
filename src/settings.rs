//! Tiny persisted settings: theme, recently opened repositories and recent
//! commit messages.
//! Stored as `key=value` lines in ~/Library/Application Support/GitBrains/settings.

use std::path::PathBuf;

const MAX_RECENT: usize = 10;
const MAX_MESSAGES: usize = 20;

#[derive(Default)]
pub struct Settings {
    pub dark: bool,
    /// Update Project rebases instead of merging.
    pub update_rebase: bool,
    pub recent: Vec<PathBuf>,
    /// Commit messages, newest first.
    pub messages: Vec<String>,
}

/// Keep a value on one line.
fn escape(value: &str) -> String {
    value.replace('\\', "\\\\").replace('\n', "\\n")
}

fn unescape(value: &str) -> String {
    let mut out = String::new();
    let mut chars = value.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('n') => out.push('\n'),
            Some(other) => out.push(other),
            None => out.push('\\'),
        }
    }
    out
}

fn file() -> Option<PathBuf> {
    let home = std::env::var_os("HOME")?;
    Some(PathBuf::from(home).join("Library/Application Support/GitBrains/settings"))
}

impl Settings {
    pub fn load() -> Self {
        let mut settings = Settings::default();
        let Some(text) = file().and_then(|path| std::fs::read_to_string(path).ok()) else {
            return settings;
        };
        for line in text.lines() {
            match line.split_once('=') {
                Some(("theme", value)) => settings.dark = value == "dark",
                Some(("update", value)) => settings.update_rebase = value == "rebase",
                Some(("recent", value)) => settings.recent.push(PathBuf::from(value)),
                Some(("message", value)) => settings.messages.push(unescape(value)),
                _ => {}
            }
        }
        settings
    }

    pub fn save(&self) {
        let Some(path) = file() else { return };
        let mut text = format!(
            "theme={}\nupdate={}\n",
            if self.dark { "dark" } else { "light" },
            if self.update_rebase { "rebase" } else { "merge" }
        );
        for repo in &self.recent {
            text.push_str(&format!("recent={}\n", repo.display()));
        }
        for message in &self.messages {
            text.push_str(&format!("message={}\n", escape(message)));
        }
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).ok();
        }
        std::fs::write(path, text).ok();
    }

    pub fn update(f: impl FnOnce(&mut Settings)) {
        let mut settings = Settings::load();
        f(&mut settings);
        settings.save();
    }

    pub fn add_recent(repo: PathBuf) {
        Settings::update(|settings| {
            settings.recent.retain(|path| path != &repo);
            settings.recent.insert(0, repo);
            settings.recent.truncate(MAX_RECENT);
        });
    }

    pub fn add_message(message: String) {
        Settings::update(|settings| {
            settings.messages.retain(|m| m != &message);
            settings.messages.insert(0, message);
            settings.messages.truncate(MAX_MESSAGES);
        });
    }
}

#[cfg(test)]
mod tests {
    use super::{escape, unescape};

    #[test]
    fn messages_round_trip() {
        for message in ["Subject", "Subject\n\nBody line\nC:\\path\\n", "trailing \\"] {
            let escaped = escape(message);
            assert!(!escaped.contains('\n'));
            assert_eq!(unescape(&escaped), message);
        }
    }
}
