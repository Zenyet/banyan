//! Watches the repository so the views refresh when files, the index or refs
//! change outside the app, like JetBrains' VFS refresh.

use std::path::{Component, Path, PathBuf};
use std::sync::{Arc, Mutex};

use notify::{EventKind, RecommendedWatcher, RecursiveMode, Watcher as _};

/// Files and directories under `.git` whose changes matter to the views.
const GIT_ENTRIES: &[&str] = &[
    "HEAD",
    "index",
    "refs",
    "packed-refs",
    "MERGE_HEAD",
    "CHERRY_PICK_HEAD",
    "REVERT_HEAD",
    "rebase-merge",
    "rebase-apply",
];

/// Paths changed since the last [`take`](Changes::take), split by where they live.
#[derive(Default)]
pub struct Changes {
    pub git: bool,
    pub worktree: Vec<PathBuf>,
}

pub struct RepoWatcher {
    _watcher: RecommendedWatcher,
    pending: Arc<Mutex<Changes>>,
}

impl RepoWatcher {
    pub fn new(root: &Path, git_dir: &Path) -> notify::Result<Self> {
        let pending = Arc::new(Mutex::new(Changes::default()));
        let sink = pending.clone();
        let watched_git_dir = git_dir.to_path_buf();
        let mut watcher = notify::recommended_watcher(move |event: notify::Result<notify::Event>| {
            let Ok(event) = event else { return };
            if matches!(event.kind, EventKind::Access(_)) {
                return;
            }
            let mut pending = sink.lock().unwrap();
            for path in event.paths {
                match path.strip_prefix(&watched_git_dir) {
                    Ok(rel) => pending.git |= is_relevant_git_path(rel),
                    Err(_) => pending.worktree.push(path),
                }
            }
        })?;
        watcher.watch(root, RecursiveMode::Recursive)?;
        // A worktree or submodule may keep its git directory elsewhere.
        if !git_dir.starts_with(root) {
            watcher.watch(git_dir, RecursiveMode::Recursive)?;
        }
        Ok(Self {
            _watcher: watcher,
            pending,
        })
    }

    pub fn take(&self) -> Changes {
        std::mem::take(&mut *self.pending.lock().unwrap())
    }
}

fn is_relevant_git_path(rel: &Path) -> bool {
    if rel.extension().is_some_and(|ext| ext == "lock") {
        return false;
    }
    match rel.components().next() {
        Some(Component::Normal(first)) => GIT_ENTRIES.iter().any(|entry| first == *entry),
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn git_paths() {
        assert!(is_relevant_git_path(Path::new("HEAD")));
        assert!(is_relevant_git_path(Path::new("refs/heads/main")));
        assert!(is_relevant_git_path(Path::new("index")));
        assert!(!is_relevant_git_path(Path::new("index.lock")));
        assert!(!is_relevant_git_path(Path::new("objects/ab/cdef")));
        assert!(!is_relevant_git_path(Path::new("logs/HEAD")));
        assert!(!is_relevant_git_path(Path::new("FETCH_HEAD")));
    }

    fn wait_for(watcher: &RepoWatcher, done: impl Fn(&Changes) -> bool) -> bool {
        let mut seen = Changes::default();
        for _ in 0..50 {
            std::thread::sleep(std::time::Duration::from_millis(100));
            let changes = watcher.take();
            seen.git |= changes.git;
            seen.worktree.extend(changes.worktree);
            if done(&seen) {
                return true;
            }
        }
        false
    }

    #[test]
    fn reports_worktree_and_ref_changes() {
        let dir = std::env::temp_dir().join(format!("banyan-watch-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let dir = dir.canonicalize().unwrap();
        let git = |args: &[&str]| {
            std::process::Command::new("git").arg("-C").arg(&dir).args(args).output().unwrap()
        };
        git(&["init", "-q", "-b", "main"]);
        let watcher = RepoWatcher::new(&dir, &dir.join(".git")).unwrap();

        std::fs::write(dir.join("file.txt"), "hello").unwrap();
        assert!(wait_for(&watcher, |c| c.worktree.iter().any(|p| p.ends_with("file.txt"))));

        watcher.take();
        git(&["-c", "user.name=T", "-c", "user.email=t@e", "add", "file.txt"]);
        git(&["-c", "user.name=T", "-c", "user.email=t@e", "commit", "-q", "-m", "x"]);
        assert!(wait_for(&watcher, |c| c.git));

        std::fs::remove_dir_all(&dir).ok();
    }
}
