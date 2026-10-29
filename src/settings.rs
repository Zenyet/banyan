//! Persisted settings: the preferences in the Settings dialog, recently
//! opened repositories and recent commit messages.
//! Stored as `key=value` lines in ~/Library/Application Support/GitBrains/settings,
//! and kept in memory so that views can read them while rendering.

use std::path::PathBuf;
use std::sync::{LazyLock, RwLock};

const MAX_RECENT: usize = 10;
const MAX_MESSAGES: usize = 20;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ThemeChoice {
    #[default]
    System,
    Light,
    Dark,
}

impl ThemeChoice {
    pub const ALL: [ThemeChoice; 3] = [ThemeChoice::System, ThemeChoice::Light, ThemeChoice::Dark];

    pub fn label(self) -> &'static str {
        match self {
            ThemeChoice::System => "Sync with OS",
            ThemeChoice::Light => "Light",
            ThemeChoice::Dark => "Dark",
        }
    }

    fn key(self) -> &'static str {
        match self {
            ThemeChoice::System => "system",
            ThemeChoice::Light => "light",
            ThemeChoice::Dark => "dark",
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Settings {
    pub theme: ThemeChoice,
    /// Base UI font size in pixels; other text sizes scale with it.
    pub font_size: u32,
    /// "3 minutes ago" rather than a date and time in the log.
    pub relative_dates: bool,
    /// The git executable; empty means `git` from the PATH.
    pub git_path: String,
    /// Update Project rebases instead of merging.
    pub update_rebase: bool,
    /// Fetch in the background this often; 0 turns it off.
    pub auto_fetch_minutes: u32,
    /// Show diffs unified instead of side by side.
    pub unified_diff: bool,
    /// How many commits the log loads at a time.
    pub log_page_size: usize,
    pub recent: Vec<PathBuf>,
    /// Commit messages, newest first.
    pub messages: Vec<String>,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            theme: ThemeChoice::default(),
            font_size: 16,
            relative_dates: true,
            git_path: String::new(),
            update_rebase: false,
            auto_fetch_minutes: 0,
            unified_diff: false,
            log_page_size: 5000,
            recent: Vec::new(),
            messages: Vec::new(),
        }
    }
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

static CURRENT: LazyLock<RwLock<Settings>> = LazyLock::new(|| {
    let text = file().and_then(|path| std::fs::read_to_string(path).ok()).unwrap_or_default();
    RwLock::new(Settings::parse(&text))
});

impl Settings {
    /// A copy of the current settings.
    pub fn get() -> Settings {
        CURRENT.read().unwrap().clone()
    }

    /// Read one setting without copying the rest, e.g. while rendering.
    pub fn read<R>(f: impl FnOnce(&Settings) -> R) -> R {
        f(&CURRENT.read().unwrap())
    }

    /// Change the settings and save them.
    pub fn update(f: impl FnOnce(&mut Settings)) {
        let text = {
            let mut settings = CURRENT.write().unwrap();
            f(&mut settings);
            settings.to_text()
        };
        let Some(path) = file() else { return };
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).ok();
        }
        std::fs::write(path, text).ok();
    }

    fn parse(text: &str) -> Settings {
        let mut settings = Settings::default();
        for line in text.lines() {
            let Some((key, value)) = line.split_once('=') else { continue };
            match key {
                "theme" => {
                    if let Some(theme) = ThemeChoice::ALL.into_iter().find(|t| t.key() == value) {
                        settings.theme = theme;
                    }
                }
                "font_size" => settings.font_size = value.parse().unwrap_or(settings.font_size),
                "dates" => settings.relative_dates = value != "absolute",
                "git" => settings.git_path = value.to_string(),
                "update" => settings.update_rebase = value == "rebase",
                "auto_fetch" => settings.auto_fetch_minutes = value.parse().unwrap_or(0),
                "diff" => settings.unified_diff = value == "unified",
                "log_page" => settings.log_page_size = value.parse().unwrap_or(settings.log_page_size),
                "recent" => settings.recent.push(PathBuf::from(value)),
                "message" => settings.messages.push(unescape(value)),
                _ => {}
            }
        }
        settings
    }

    fn to_text(&self) -> String {
        let mut text = format!(
            "theme={}\nfont_size={}\ndates={}\ngit={}\nupdate={}\nauto_fetch={}\ndiff={}\nlog_page={}\n",
            self.theme.key(),
            self.font_size,
            if self.relative_dates { "relative" } else { "absolute" },
            self.git_path,
            if self.update_rebase { "rebase" } else { "merge" },
            self.auto_fetch_minutes,
            if self.unified_diff { "unified" } else { "split" },
            self.log_page_size,
        );
        for repo in &self.recent {
            text.push_str(&format!("recent={}\n", repo.display()));
        }
        for message in &self.messages {
            text.push_str(&format!("message={}\n", escape(message)));
        }
        text
    }

    /// The git executable to run.
    pub fn git_program(&self) -> &str {
        match self.git_path.trim() {
            "" => "git",
            path => path,
        }
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
    use super::{Settings, ThemeChoice, escape, unescape};

    #[test]
    fn messages_round_trip() {
        for message in ["Subject", "Subject\n\nBody line\nC:\\path\\n", "trailing \\"] {
            let escaped = escape(message);
            assert!(!escaped.contains('\n'));
            assert_eq!(unescape(&escaped), message);
        }
    }

    #[test]
    fn settings_round_trip() {
        let settings = Settings {
            theme: ThemeChoice::Dark,
            font_size: 14,
            relative_dates: false,
            git_path: "/opt/homebrew/bin/git".into(),
            update_rebase: true,
            auto_fetch_minutes: 15,
            unified_diff: true,
            log_page_size: 2000,
            recent: vec!["/tmp/repo".into()],
            messages: vec!["Fix\n\nDetails".into()],
        };
        assert_eq!(Settings::parse(&settings.to_text()), settings);
        // Files written before the Settings dialog existed.
        let old = Settings::parse("theme=light\nupdate=merge\nrecent=/tmp/a\n");
        assert_eq!((old.theme, old.font_size, old.log_page_size), (ThemeChoice::Light, 16, 5000));
        assert_eq!(Settings::default().git_program(), "git");
    }
}
