//! Tiny persisted settings: theme and recently opened repositories.
//! Stored as `key=value` lines in ~/Library/Application Support/GitBrains/settings.

use std::path::PathBuf;

const MAX_RECENT: usize = 10;

#[derive(Default)]
pub struct Settings {
    pub dark: bool,
    pub recent: Vec<PathBuf>,
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
                Some(("recent", value)) => settings.recent.push(PathBuf::from(value)),
                _ => {}
            }
        }
        settings
    }

    pub fn save(&self) {
        let Some(path) = file() else { return };
        let mut text = format!("theme={}\n", if self.dark { "dark" } else { "light" });
        for repo in &self.recent {
            text.push_str(&format!("recent={}\n", repo.display()));
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
}
