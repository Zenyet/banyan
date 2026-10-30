//! Persisted settings: the preferences in the Settings dialog, recently
//! opened repositories and recent commit messages.
//! Stored as `key=value` lines in Banyan/settings under the user's application data folder,
//! and kept in memory so that views can read them while rendering.

use std::path::{Path, PathBuf};
use std::sync::{LazyLock, RwLock};

use crate::git::UpdateMethod;

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
            ThemeChoice::System => crate::i18n::tr!("Sync with OS"),
            ThemeChoice::Light => crate::i18n::tr!("Light"),
            ThemeChoice::Dark => crate::i18n::tr!("Dark"),
        }
    }

    pub fn key(self) -> &'static str {
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
    /// How Update Project brings in the tracked branch, by default.
    pub update_method: UpdateMethod,
    /// Fetch in the background this often; 0 turns it off.
    pub auto_fetch_minutes: u32,
    /// Show diffs unified instead of side by side.
    pub unified_diff: bool,
    /// Leave whitespace-only changes out of diffs (`git diff -w`).
    pub ignore_whitespace: bool,
    /// Interface language: `en` or `zh`; empty until chosen, when the
    /// system's language decides.
    pub language: String,
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
            update_method: UpdateMethod::Merge,
            auto_fetch_minutes: 0,
            unified_diff: false,
            ignore_whitespace: false,
            language: String::new(),
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

/// `Banyan/settings` in the platform's per-user application data folder.
fn file() -> Option<PathBuf> {
    let dir = if cfg!(target_os = "macos") {
        std::env::home_dir()?.join("Library/Application Support")
    } else if cfg!(windows) {
        PathBuf::from(std::env::var_os("APPDATA")?)
    } else {
        std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .filter(|dir| dir.is_absolute())
            .or_else(|| Some(std::env::home_dir()?.join(".config")))?
    };
    Some(dir.join("Banyan/settings"))
}

/// The app was called GitBrains; read its settings if Banyan has none yet.
fn read_or_migrate(path: &Path) -> Option<String> {
    if let Ok(text) = std::fs::read_to_string(path) {
        return Some(text);
    }
    let old = path.parent()?.parent()?.join("GitBrains/settings");
    let text = std::fs::read_to_string(old).ok()?;
    std::fs::create_dir_all(path.parent()?).ok()?;
    std::fs::write(path, &text).ok()?;
    Some(text)
}

static CURRENT: LazyLock<RwLock<Settings>> = LazyLock::new(|| {
    let text = file().and_then(|path| read_or_migrate(&path)).unwrap_or_default();
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
                "update" => settings.update_method = UpdateMethod::from_key(value),
                "auto_fetch" => settings.auto_fetch_minutes = value.parse().unwrap_or(0),
                "diff" => settings.unified_diff = value == "unified",
                "whitespace" => settings.ignore_whitespace = value == "ignore",
                "language" => settings.language = value.to_string(),
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
            "theme={}\nfont_size={}\ndates={}\ngit={}\nupdate={}\nauto_fetch={}\ndiff={}\nwhitespace={}\nlanguage={}\nlog_page={}\n",
            self.theme.key(),
            self.font_size,
            if self.relative_dates { "relative" } else { "absolute" },
            self.git_path,
            self.update_method.key(),
            self.auto_fetch_minutes,
            if self.unified_diff { "unified" } else { "split" },
            if self.ignore_whitespace { "ignore" } else { "show" },
            self.language,
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

    pub fn remove_recent(repo: &Path) {
        Settings::update(|settings| settings.recent.retain(|path| path != repo));
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
            update_method: crate::git::UpdateMethod::FastForwardOnly,
            auto_fetch_minutes: 15,
            unified_diff: true,
            ignore_whitespace: true,
            language: "zh".into(),
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
