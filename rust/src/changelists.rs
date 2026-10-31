//! JetBrains-style changelists: local changes sorted into named lists, one
//! of them active, where new changes go. Git knows nothing of them; they are
//! kept in the repository's git directory, in `banyan/changelists`.

use std::collections::BTreeMap;
use std::path::Path;

use anyhow::{Result, bail};

/// The list every repository starts with.
pub const DEFAULT: &str = "Changes";

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Changelists {
    /// In display order; never empty.
    names: Vec<String>,
    active: String,
    /// The list of each changed file.
    files: BTreeMap<String, String>,
}

impl Default for Changelists {
    fn default() -> Self {
        Self {
            names: vec![DEFAULT.to_string()],
            active: DEFAULT.to_string(),
            files: BTreeMap::new(),
        }
    }
}

impl Changelists {
    pub fn names(&self) -> &[String] {
        &self.names
    }

    pub fn active(&self) -> &str {
        &self.active
    }

    /// The list `path` is in; files not yet sorted go to the active one.
    pub fn list_of(&self, path: &str) -> &str {
        self.files.get(path).map(String::as_str).unwrap_or(&self.active)
    }

    /// Put files that just changed into the active list, and forget files
    /// that no longer have changes. Returns whether anything moved.
    pub fn sync<'a>(&mut self, changed: impl IntoIterator<Item = &'a str>) -> bool {
        let changed: Vec<&str> = changed.into_iter().collect();
        let before = self.files.len();
        self.files.retain(|path, _| changed.contains(&path.as_str()));
        let mut moved = self.files.len() != before;
        for path in changed {
            if !self.files.contains_key(path) {
                self.files.insert(path.to_string(), self.active.clone());
                moved = true;
            }
        }
        moved
    }

    pub fn create(&mut self, name: &str) -> Result<()> {
        let name = self.check_new_name(name)?;
        self.names.push(name);
        Ok(())
    }

    pub fn rename(&mut self, old: &str, new: &str) -> Result<()> {
        if old == new.trim() {
            return Ok(());
        }
        let new = self.check_new_name(new)?;
        let Some(slot) = self.names.iter_mut().find(|n| *n == old) else {
            bail!("there is no changelist named {old}");
        };
        *slot = new.clone();
        if self.active == old {
            self.active = new.clone();
        }
        for list in self.files.values_mut().filter(|list| *list == old) {
            *list = new.clone();
        }
        Ok(())
    }

    /// Remove a list; its files go to the active list, or to the first
    /// other one when it was the active list.
    pub fn delete(&mut self, name: &str) -> Result<()> {
        if self.names.len() == 1 {
            bail!("the last changelist cannot be deleted");
        }
        self.names.retain(|n| n != name);
        if self.active == name {
            self.active = self.names[0].clone();
        }
        let target = self.active.clone();
        for list in self.files.values_mut().filter(|list| *list == name) {
            *list = target.clone();
        }
        Ok(())
    }

    pub fn set_active(&mut self, name: &str) -> Result<()> {
        if !self.names.iter().any(|n| n == name) {
            bail!("there is no changelist named {name}");
        }
        self.active = name.to_string();
        Ok(())
    }

    /// Move `paths` into list `to`, creating it if needed.
    pub fn move_to(&mut self, paths: &[String], to: &str) -> Result<()> {
        if !self.names.iter().any(|n| n == to) {
            self.create(to)?;
        }
        for path in paths {
            self.files.insert(path.clone(), to.to_string());
        }
        Ok(())
    }

    fn check_new_name(&self, name: &str) -> Result<String> {
        let name = name.trim();
        if name.is_empty() || name.contains(['\n', '\t']) {
            bail!("a changelist needs a name on one line");
        }
        if self.names.iter().any(|n| n == name) {
            bail!("there is already a changelist named {name}");
        }
        Ok(name.to_string())
    }

    fn parse(text: &str) -> Self {
        let mut this = Self { names: Vec::new(), active: String::new(), files: BTreeMap::new() };
        for line in text.lines() {
            match line.split_once('=') {
                Some(("list", name)) if !name.is_empty() && !this.names.iter().any(|n| n == name) => {
                    this.names.push(name.to_string())
                }
                Some(("active", name)) => this.active = name.to_string(),
                Some(("file", rest)) => {
                    if let Some((list, path)) = rest.split_once('\t') {
                        this.files.insert(path.to_string(), list.to_string());
                    }
                }
                _ => {}
            }
        }
        if this.names.is_empty() {
            this.names.push(DEFAULT.to_string());
        }
        if !this.names.contains(&this.active) {
            this.active = this.names[0].clone();
        }
        let names = this.names.clone();
        this.files.retain(|_, list| names.contains(list));
        this
    }

    fn to_text(&self) -> String {
        let mut text = String::new();
        for name in &self.names {
            text.push_str(&format!("list={name}\n"));
        }
        text.push_str(&format!("active={}\n", self.active));
        for (path, list) in &self.files {
            if !path.contains('\n') {
                text.push_str(&format!("file={list}\t{path}\n"));
            }
        }
        text
    }

    /// The changelists of the repository whose git directory is `git_dir`.
    pub fn load(git_dir: &Path) -> Self {
        std::fs::read_to_string(git_dir.join("banyan/changelists"))
            .map(|text| Self::parse(&text))
            .unwrap_or_default()
    }

    pub fn save(&self, git_dir: &Path) -> Result<()> {
        let dir = git_dir.join("banyan");
        std::fs::create_dir_all(&dir)?;
        std::fs::write(dir.join("changelists"), self.to_text())?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn paths(list: &[&str]) -> Vec<String> {
        list.iter().map(|p| p.to_string()).collect()
    }

    #[test]
    fn new_changes_go_to_the_active_list() {
        let mut lists = Changelists::default();
        lists.create("Refactor").unwrap();
        lists.sync(["a.rs"]);
        lists.set_active("Refactor").unwrap();
        lists.sync(["a.rs", "b.rs"]);
        assert_eq!(lists.list_of("a.rs"), DEFAULT);
        assert_eq!(lists.list_of("b.rs"), "Refactor");
        // Committed files are forgotten; when they change again they start over.
        lists.sync(["b.rs"]);
        lists.set_active(DEFAULT).unwrap();
        lists.sync(["a.rs", "b.rs"]);
        assert_eq!(lists.list_of("a.rs"), DEFAULT);
        assert_eq!(lists.list_of("b.rs"), "Refactor");
    }

    #[test]
    fn deleting_a_list_moves_its_files() {
        let mut lists = Changelists::default();
        lists.move_to(&paths(&["a.rs"]), "Debug").unwrap();
        lists.set_active("Debug").unwrap();
        lists.delete("Debug").unwrap();
        assert_eq!(lists.names(), &paths(&[DEFAULT]));
        assert_eq!(lists.active(), DEFAULT);
        assert_eq!(lists.list_of("a.rs"), DEFAULT);
        assert!(lists.delete(DEFAULT).is_err());
    }

    #[test]
    fn renaming_keeps_files_and_the_active_mark() {
        let mut lists = Changelists::default();
        lists.sync(["a.rs"]);
        lists.rename(DEFAULT, "Feature").unwrap();
        assert_eq!((lists.active(), lists.list_of("a.rs")), ("Feature", "Feature"));
        lists.create("Other").unwrap();
        assert!(lists.rename("Other", "Feature").is_err());
        assert!(lists.create(" ").is_err());
    }

    #[test]
    fn round_trips_through_text() {
        let mut lists = Changelists::default();
        lists.move_to(&paths(&["src/a b.rs", "c.rs"]), "Fix login").unwrap();
        lists.set_active("Fix login").unwrap();
        assert_eq!(Changelists::parse(&lists.to_text()), lists);
        // Files of lists that no longer exist fall back to the active list.
        let parsed = Changelists::parse("list=A\nactive=Gone\nfile=Gone\tx.rs\n");
        assert_eq!((parsed.active(), parsed.list_of("x.rs")), ("A", "A"));
    }
}
