//! Thin wrapper over the `git` CLI, the same backend JetBrains IDEs use.

use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context as _, Result, bail};

const FIELD: char = '\x1f';
const RECORD: char = '\x1e';

#[derive(Clone, Debug)]
pub struct Repo {
    root: PathBuf,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RefKind {
    Head,
    Local,
    Remote,
    Tag,
}

#[derive(Clone, Debug)]
pub struct RefLabel {
    pub name: String,
    pub kind: RefKind,
}

#[derive(Clone, Debug)]
pub struct Commit {
    pub id: String,
    pub parents: Vec<String>,
    pub author: String,
    pub email: String,
    pub time: i64,
    pub subject: String,
    pub refs: Vec<RefLabel>,
}

impl Commit {
    pub fn short_id(&self) -> &str {
        &self.id[..self.id.len().min(8)]
    }
}

/// A commit that touched a file, with the file's path at that commit.
#[derive(Clone, Debug)]
pub struct FileRevision {
    pub commit: Commit,
    pub path: String,
}

#[derive(Clone, Debug)]
pub struct BlameLine {
    pub id: String,
    pub author: String,
    pub time: i64,
    pub summary: String,
    pub text: String,
}

#[derive(Clone, Debug)]
pub struct CommitDetails {
    pub id: String,
    pub parents: Vec<String>,
    pub author: String,
    pub email: String,
    pub author_time: i64,
    pub committer: String,
    pub commit_time: i64,
    pub message: String,
    pub files: Vec<FileChange>,
    pub branches: Vec<String>,
}

#[derive(Clone, Debug)]
pub struct FileChange {
    /// `A`, `M`, `D`, `R`, `C`, `T`.
    pub status: char,
    pub path: String,
    pub orig_path: Option<String>,
}

#[derive(Clone, Debug)]
pub struct Branch {
    /// Short name, e.g. `main` or `origin/main`.
    pub name: String,
    pub kind: RefKind,
    pub target: String,
    pub upstream: Option<String>,
    /// e.g. `ahead 1, behind 2`.
    pub track: Option<String>,
    pub is_head: bool,
}

#[derive(Clone, Debug)]
pub struct StatusEntry {
    pub path: String,
    pub orig_path: Option<String>,
    pub index: char,
    pub worktree: char,
}

impl StatusEntry {
    pub fn is_untracked(&self) -> bool {
        self.index == '?'
    }

    pub fn is_conflicted(&self) -> bool {
        matches!(
            (self.index, self.worktree),
            ('U', _) | (_, 'U') | ('A', 'A') | ('D', 'D')
        )
    }

    /// One status letter as JetBrains shows it: the combined state against HEAD.
    pub fn display_status(&self) -> char {
        if self.is_untracked() {
            '?'
        } else if self.is_conflicted() {
            'U'
        } else if self.index == 'A' {
            'A'
        } else if self.index == 'D' || self.worktree == 'D' {
            'D'
        } else if self.index == 'R' {
            'R'
        } else {
            'M'
        }
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum RepoState {
    #[default]
    Clean,
    Merging,
    Rebasing,
    CherryPicking,
    Reverting,
}

#[derive(Clone, Copy, Debug)]
pub enum ResetMode {
    Soft,
    Mixed,
    Hard,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RebaseAction {
    Pick,
    /// Pick with a new message.
    Reword(String),
    Squash,
    Fixup,
    Drop,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Remote {
    pub name: String,
    pub fetch_url: String,
    pub push_url: String,
}

#[derive(Clone, Debug)]
pub struct PushTarget {
    pub branch: String,
    pub remote: String,
    pub remote_branch: String,
    /// The remote branch does not exist yet; the push sets it as upstream.
    pub is_new: bool,
}

#[derive(Clone, Debug)]
pub struct Stash {
    /// e.g. `stash@{0}`.
    pub name: String,
    pub message: String,
    /// The stash commit; its first parent is the commit it was made on.
    pub commit: Commit,
}

impl Repo {
    pub fn open(path: &Path) -> Result<Self> {
        let out = run(path, &["rev-parse", "--show-toplevel"])
            .with_context(|| format!("{} is not a git repository", path.display()))?;
        Ok(Self {
            root: PathBuf::from(out.trim()),
        })
    }

    /// Clone `url` into `dest`, which must not exist or be empty.
    pub fn clone_from(url: &str, dest: &Path) -> Result<Self> {
        let parent = dest.parent().context("invalid destination")?;
        std::fs::create_dir_all(parent)?;
        crate::console::recording(|| run(parent, &["clone", "--", url, &dest.to_string_lossy()]))?;
        Repo::open(dest)
    }

    /// The directory name `git clone` would pick for `url`.
    pub fn clone_dir_name(url: &str) -> String {
        let trimmed = url.trim().trim_end_matches('/');
        let last = trimmed.rsplit(['/', ':']).next().unwrap_or(trimmed);
        last.strip_suffix(".git").unwrap_or(last).to_string()
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    pub fn name(&self) -> String {
        self.root
            .file_name()
            .map(|name| name.to_string_lossy().into_owned())
            .unwrap_or_else(|| self.root.display().to_string())
    }

    pub fn git(&self, args: &[&str]) -> Result<String> {
        run(&self.root, args)
    }

    pub fn log(&self, refs: Option<&str>, limit: usize) -> Result<Vec<Commit>> {
        match refs {
            Some(refs) => self.log_revs(&[refs], limit),
            None => self.log_revs(&["--branches", "--remotes", "--tags", "HEAD"], limit),
        }
    }

    /// `git log` over arbitrary revision arguments, newest first.
    pub fn log_revs(&self, revs: &[&str], limit: usize) -> Result<Vec<Commit>> {
        let format = format!("--format=%H{FIELD}%P{FIELD}%an{FIELD}%ae{FIELD}%at{FIELD}%D{FIELD}%s{RECORD}");
        let limit = format!("-n{limit}");
        let mut args = vec!["log", "--date-order", "--decorate=full", &format, &limit];
        args.extend(revs);
        args.push("--");
        let out = match self.git(&args) {
            Ok(out) => out,
            // A fresh repository has no HEAD yet.
            Err(_) if !self.has_head() => return Ok(Vec::new()),
            Err(err) => return Err(err),
        };
        Ok(out
            .split(RECORD)
            .filter_map(|record| {
                let record = record.trim_start_matches('\n');
                let mut fields = record.split(FIELD);
                let id = fields.next()?.to_string();
                if id.is_empty() {
                    return None;
                }
                let parents = fields.next()?.split_whitespace().map(str::to_string).collect();
                let author = fields.next()?.to_string();
                let email = fields.next()?.to_string();
                let time = fields.next()?.parse().unwrap_or(0);
                let refs = parse_decorations(fields.next()?);
                let subject = fields.next().unwrap_or_default().to_string();
                Some(Commit {
                    id,
                    parents,
                    author,
                    email,
                    time,
                    subject,
                    refs,
                })
            })
            .collect())
    }

    /// Commits that changed `path`, following renames.
    pub fn file_history(&self, path: &str) -> Result<Vec<FileRevision>> {
        let format = format!("--format={RECORD}%H{FIELD}%P{FIELD}%an{FIELD}%ae{FIELD}%at{FIELD}%s");
        let out = self.git(&["log", "--follow", "--name-only", &format, "--", path])?;
        Ok(out
            .split(RECORD)
            .filter_map(|record| {
                let (header, names) = record.split_once('\n').unwrap_or((record, ""));
                let mut fields = header.split(FIELD);
                let id = fields.next()?.to_string();
                if id.is_empty() {
                    return None;
                }
                let parents = fields.next()?.split_whitespace().map(str::to_string).collect();
                let author = fields.next()?.to_string();
                let email = fields.next()?.to_string();
                let time = fields.next()?.parse().unwrap_or(0);
                let subject = fields.next().unwrap_or_default().to_string();
                let path = names
                    .lines()
                    .find(|line| !line.trim().is_empty())
                    .unwrap_or(path)
                    .to_string();
                Some(FileRevision {
                    commit: Commit {
                        id,
                        parents,
                        author,
                        email,
                        time,
                        subject,
                        refs: Vec::new(),
                    },
                    path,
                })
            })
            .collect())
    }

    /// Patch of `path` introduced by commit `id` (against its first parent).
    pub fn revision_diff(&self, id: &str, path: &str) -> Result<String> {
        self.git(&["show", "--format=", "-M", "--first-parent", id, "--", path])
    }

    /// Line-by-line blame of `path` at `rev`.
    pub fn blame(&self, rev: &str, path: &str) -> Result<Vec<BlameLine>> {
        let out = self.git(&["blame", "--porcelain", rev, "--", path])?;
        let mut infos: std::collections::HashMap<String, (String, i64, String)> = Default::default();
        let mut lines = Vec::new();
        let mut current = String::new();
        let mut author = String::new();
        let mut time = 0;
        let mut summary = String::new();
        for line in out.lines() {
            if let Some(text) = line.strip_prefix('\t') {
                let info = infos
                    .entry(current.clone())
                    .or_insert_with(|| (author.clone(), time, summary.clone()))
                    .clone();
                lines.push(BlameLine {
                    id: current.clone(),
                    author: info.0,
                    time: info.1,
                    summary: info.2,
                    text: text.to_string(),
                });
            } else if let Some(value) = line.strip_prefix("author ") {
                author = value.to_string();
            } else if let Some(value) = line.strip_prefix("author-time ") {
                time = value.parse().unwrap_or(0);
            } else if let Some(value) = line.strip_prefix("summary ") {
                summary = value.to_string();
            } else {
                let id = line.split(' ').next().unwrap_or_default();
                if id.len() == 40 && id.chars().all(|c| c.is_ascii_hexdigit()) {
                    current = id.to_string();
                }
            }
        }
        Ok(lines)
    }

    pub fn git_dir(&self) -> Result<PathBuf> {
        Ok(PathBuf::from(self.git(&["rev-parse", "--absolute-git-dir"])?.trim()))
    }

    pub fn head(&self) -> Option<String> {
        self.git(&["rev-parse", "-q", "--verify", "HEAD"])
            .ok()
            .map(|out| out.trim().to_string())
    }

    /// The subset of `paths` (inside the work tree) that git does not ignore.
    pub fn not_ignored(&self, paths: &[PathBuf]) -> Result<Vec<PathBuf>> {
        use std::io::Write as _;
        if paths.is_empty() {
            return Ok(Vec::new());
        }
        let mut child = command(&self.root, &["check-ignore", "--stdin", "-z"])
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::null())
            .spawn()?;
        {
            let mut stdin = child.stdin.take().context("no stdin")?;
            for path in paths {
                stdin.write_all(path.to_string_lossy().as_bytes())?;
                stdin.write_all(b"\0")?;
            }
        }
        let output = child.wait_with_output()?;
        let ignored: std::collections::HashSet<PathBuf> = String::from_utf8_lossy(&output.stdout)
            .split('\0')
            .filter(|p| !p.is_empty())
            .map(PathBuf::from)
            .collect();
        Ok(paths.iter().filter(|p| !ignored.contains(*p)).cloned().collect())
    }

    pub fn has_head(&self) -> bool {
        self.git(&["rev-parse", "--verify", "-q", "HEAD"]).is_ok()
    }

    pub fn commit_details(&self, id: &str) -> Result<CommitDetails> {
        let format = format!("--format=%H{FIELD}%P{FIELD}%an{FIELD}%ae{FIELD}%at{FIELD}%cn{FIELD}%ct{FIELD}%B");
        let out = self.git(&["show", "-s", &format, id])?;
        let mut fields = out.split(FIELD);
        let mut next = || fields.next().unwrap_or_default().to_string();
        let id = next();
        let parents: Vec<String> = next().split_whitespace().map(str::to_string).collect();
        let author = next();
        let email = next();
        let author_time = next().parse().unwrap_or(0);
        let committer = next();
        let commit_time = next().parse().unwrap_or(0);
        let message = next().trim_end().to_string();
        let files = self.commit_files(&id, parents.first().map(String::as_str))?;
        let branches = self
            .git(&["branch", "-a", "--format=%(refname:short)", "--contains", &id])
            .map(|out| out.lines().map(str::to_string).collect())
            .unwrap_or_default();
        Ok(CommitDetails {
            id,
            parents,
            author,
            email,
            author_time,
            committer,
            commit_time,
            message,
            files,
            branches,
        })
    }

    /// Files changed by `id` against its first parent.
    fn commit_files(&self, id: &str, parent: Option<&str>) -> Result<Vec<FileChange>> {
        let out = match parent {
            Some(parent) => self.git(&["diff-tree", "-r", "-M", "--name-status", "-z", parent, id])?,
            None => self.git(&["diff-tree", "-r", "-M", "--name-status", "-z", "--root", id])?,
        };
        let mut parts = out.split('\0').filter(|part| !part.is_empty());
        let mut files = Vec::new();
        // With --root the first token is the commit id itself.
        while let Some(status) = parts.next() {
            let Some(letter) = status.chars().next() else { continue };
            if status.len() >= 40 && status.chars().all(|c| c.is_ascii_hexdigit()) {
                continue;
            }
            if matches!(letter, 'R' | 'C') {
                let orig = parts.next().unwrap_or_default().to_string();
                let path = parts.next().unwrap_or_default().to_string();
                files.push(FileChange {
                    status: letter,
                    path,
                    orig_path: Some(orig),
                });
            } else {
                let path = parts.next().unwrap_or_default().to_string();
                files.push(FileChange {
                    status: letter,
                    path,
                    orig_path: None,
                });
            }
        }
        Ok(files)
    }

    /// Patch of one file in a commit, against its first parent.
    pub fn commit_file_diff(&self, details: &CommitDetails, file: &FileChange) -> Result<String> {
        let mut paths = vec![file.path.as_str()];
        if let Some(orig) = &file.orig_path {
            paths.push(orig);
        }
        let mut args: Vec<&str> = Vec::new();
        match details.parents.first() {
            Some(parent) => args.extend(["diff", "-M", parent.as_str(), details.id.as_str()]),
            None => args.extend(["show", "--format=", "-M", details.id.as_str()]),
        }
        args.push("--");
        args.extend(paths);
        self.git(&args)
    }

    pub fn current_branch(&self) -> Option<String> {
        self.git(&["symbolic-ref", "--short", "-q", "HEAD"])
            .ok()
            .map(|out| out.trim().to_string())
            .filter(|name| !name.is_empty())
    }

    pub fn branches(&self) -> Result<Vec<Branch>> {
        let format = format!(
            "--format=%(refname){FIELD}%(objectname){FIELD}%(upstream:short){FIELD}%(upstream:track,nobracket){FIELD}%(HEAD)"
        );
        let out = self.git(&["for-each-ref", &format, "refs/heads", "refs/remotes", "refs/tags"])?;
        let mut branches = Vec::new();
        for line in out.lines() {
            let mut fields = line.split(FIELD);
            let refname = fields.next().unwrap_or_default();
            let target = fields.next().unwrap_or_default().to_string();
            let upstream = fields.next().filter(|s| !s.is_empty()).map(str::to_string);
            let track = fields.next().filter(|s| !s.is_empty()).map(str::to_string);
            let is_head = fields.next() == Some("*");
            let (kind, name) = if let Some(name) = refname.strip_prefix("refs/heads/") {
                (RefKind::Local, name)
            } else if let Some(name) = refname.strip_prefix("refs/remotes/") {
                if name.ends_with("/HEAD") {
                    continue;
                }
                (RefKind::Remote, name)
            } else if let Some(name) = refname.strip_prefix("refs/tags/") {
                (RefKind::Tag, name)
            } else {
                continue;
            };
            branches.push(Branch {
                name: name.to_string(),
                kind,
                target,
                upstream,
                track,
                is_head,
            });
        }
        Ok(branches)
    }

    pub fn status(&self) -> Result<Vec<StatusEntry>> {
        let out = self.git(&["status", "--porcelain=v1", "-z", "--untracked-files=all"])?;
        let mut parts = out.split('\0').filter(|part| !part.is_empty());
        let mut entries = Vec::new();
        while let Some(part) = parts.next() {
            if part.len() < 4 {
                continue;
            }
            let mut chars = part.chars();
            let index = chars.next().unwrap_or(' ');
            let worktree = chars.next().unwrap_or(' ');
            let path = part[3..].to_string();
            let orig_path = if matches!(index, 'R' | 'C') {
                parts.next().map(str::to_string)
            } else {
                None
            };
            entries.push(StatusEntry {
                path,
                orig_path,
                index,
                worktree,
            });
        }
        entries.sort_by(|a, b| a.path.cmp(&b.path));
        Ok(entries)
    }

    /// Patch of a working tree file against HEAD (staged and unstaged together).
    pub fn worktree_diff(&self, entry: &StatusEntry) -> Result<String> {
        if entry.is_untracked() {
            return run_allow_diff(&self.root, &["diff", "--no-index", "--", "/dev/null", &entry.path]);
        }
        let mut args = vec!["diff", "-M"];
        if self.has_head() {
            args.push("HEAD");
        } else {
            args.push("--cached");
        }
        args.push("--");
        args.push(&entry.path);
        if let Some(orig) = &entry.orig_path {
            args.push(orig);
        }
        self.git(&args)
    }

    pub fn repo_state(&self) -> RepoState {
        let Ok(git_dir) = self.git(&["rev-parse", "--absolute-git-dir"]) else {
            return RepoState::Clean;
        };
        let git_dir = PathBuf::from(git_dir.trim());
        if git_dir.join("rebase-merge").exists() || git_dir.join("rebase-apply").exists() {
            RepoState::Rebasing
        } else if git_dir.join("MERGE_HEAD").exists() {
            RepoState::Merging
        } else if git_dir.join("CHERRY_PICK_HEAD").exists() {
            RepoState::CherryPicking
        } else if git_dir.join("REVERT_HEAD").exists() {
            RepoState::Reverting
        } else {
            RepoState::Clean
        }
    }

    /// The prepared message of an in-progress merge, cherry-pick or revert.
    pub fn merge_message(&self) -> Option<String> {
        let git_dir = self.git(&["rev-parse", "--absolute-git-dir"]).ok()?;
        let text = std::fs::read_to_string(PathBuf::from(git_dir.trim()).join("MERGE_MSG")).ok()?;
        let message: Vec<&str> = text.lines().filter(|line| !line.starts_with('#')).collect();
        Some(message.join("\n").trim().to_string()).filter(|m| !m.is_empty())
    }

    pub fn last_commit_message(&self) -> Result<String> {
        Ok(self.git(&["log", "-1", "--format=%B"])?.trim_end().to_string())
    }

    /// Commit exactly `paths` with their working tree content, like a JetBrains changelist.
    pub fn commit(&self, message: &str, paths: &[String], amend: bool) -> Result<String> {
        if !paths.is_empty() {
            let mut add = vec!["add", "-A", "--"];
            add.extend(paths.iter().map(String::as_str));
            self.git(&add)?;
        }
        let mut args = vec!["commit", "-m", message];
        if amend {
            args.push("--amend");
        }
        if !paths.is_empty() && self.has_head() && self.repo_state() == RepoState::Clean {
            args.push("--only");
            args.push("--");
            args.extend(paths.iter().map(String::as_str));
        }
        self.git(&args)
    }

    pub fn rollback(&self, entries: &[StatusEntry]) -> Result<()> {
        for entry in entries {
            if entry.is_untracked() {
                continue;
            }
            if entry.index == 'A' {
                self.git(&["rm", "-q", "--cached", "-f", "--", &entry.path])?;
            } else {
                let mut args = vec!["restore", "--source=HEAD", "--staged", "--worktree", "--"];
                args.push(&entry.path);
                if let Some(orig) = &entry.orig_path {
                    args.push(orig);
                }
                self.git(&args)?;
            }
        }
        Ok(())
    }

    pub fn checkout(&self, name: &str) -> Result<String> {
        self.git(&["checkout", name])
    }

    /// Check out a remote branch, creating a local tracking branch for it.
    pub fn checkout_remote(&self, remote_branch: &str) -> Result<String> {
        let local = remote_branch.split_once('/').map(|(_, name)| name).unwrap_or(remote_branch);
        let exists = self
            .git(&["rev-parse", "--verify", "-q", &format!("refs/heads/{local}")])
            .is_ok();
        if exists {
            self.git(&["checkout", local])
        } else {
            self.git(&["checkout", "-b", local, "--track", remote_branch])
        }
    }

    pub fn create_branch(&self, name: &str, start: &str, checkout: bool) -> Result<String> {
        if checkout {
            self.git(&["checkout", "-b", name, start])
        } else {
            self.git(&["branch", name, start])
        }
    }

    pub fn rename_branch(&self, old: &str, new: &str) -> Result<String> {
        self.git(&["branch", "-m", old, new])
    }

    pub fn delete_branch(&self, name: &str, force: bool) -> Result<String> {
        self.git(&["branch", if force { "-D" } else { "-d" }, name])
    }

    pub fn delete_remote_branch(&self, remote_branch: &str) -> Result<String> {
        let Some((remote, name)) = remote_branch.split_once('/') else {
            bail!("not a remote branch: {remote_branch}");
        };
        self.git(&["push", remote, "--delete", name])
    }

    pub fn create_tag(&self, name: &str, target: &str) -> Result<String> {
        self.git(&["tag", name, target])
    }

    pub fn delete_tag(&self, name: &str) -> Result<String> {
        self.git(&["tag", "-d", name])
    }

    pub fn merge(&self, name: &str) -> Result<String> {
        self.git(&["merge", "--no-edit", name])
    }

    pub fn rebase(&self, onto: &str) -> Result<String> {
        self.git(&["rebase", onto])
    }

    pub fn cherry_pick(&self, id: &str) -> Result<String> {
        self.git(&["cherry-pick", id])
    }

    pub fn revert(&self, id: &str) -> Result<String> {
        self.git(&["revert", "--no-edit", id])
    }

    pub fn reset(&self, id: &str, mode: ResetMode) -> Result<String> {
        let mode = match mode {
            ResetMode::Soft => "--soft",
            ResetMode::Mixed => "--mixed",
            ResetMode::Hard => "--hard",
        };
        self.git(&["reset", mode, id])
    }

    pub fn abort(&self, state: RepoState) -> Result<String> {
        let result = self.abort_op(state);
        self.cleanup_rebase_files();
        result
    }

    /// Reworded messages of an interactive rebase that is no longer running.
    fn cleanup_rebase_files(&self) {
        if self.repo_state() == RepoState::Clean
            && let Ok(dir) = self.rebase_files_dir()
        {
            std::fs::remove_dir_all(dir).ok();
        }
    }

    /// Kept inside the repository's git directory so concurrent rebases of
    /// different repositories never share it.
    fn rebase_files_dir(&self) -> Result<PathBuf> {
        Ok(self.git_dir()?.join("gitbrains-rebase"))
    }

    fn abort_op(&self, state: RepoState) -> Result<String> {
        match state {
            RepoState::Clean => Ok(String::new()),
            RepoState::Merging => self.git(&["merge", "--abort"]),
            RepoState::Rebasing => self.git(&["rebase", "--abort"]),
            RepoState::CherryPicking => self.git(&["cherry-pick", "--abort"]),
            RepoState::Reverting => self.git(&["revert", "--abort"]),
        }
    }

    pub fn continue_op(&self, state: RepoState) -> Result<String> {
        let args: &[&str] = match state {
            RepoState::Clean => return Ok(String::new()),
            RepoState::Merging => &["-c", "core.editor=true", "commit", "--no-edit"],
            RepoState::Rebasing => &["-c", "core.editor=true", "rebase", "--continue"],
            RepoState::CherryPicking => &["-c", "core.editor=true", "cherry-pick", "--continue"],
            RepoState::Reverting => &["-c", "core.editor=true", "revert", "--continue"],
        };
        let result = self.git(args);
        self.cleanup_rebase_files();
        result
    }

    pub fn resolve_conflict(&self, path: &str, ours: Option<bool>) -> Result<String> {
        if let Some(ours) = ours {
            self.git(&["checkout", if ours { "--ours" } else { "--theirs" }, "--", path])?;
        }
        self.git(&["add", "--", path])
    }

    pub fn fetch(&self) -> Result<String> {
        self.git(&["fetch", "--all", "--prune"])
    }

    pub fn pull(&self) -> Result<String> {
        self.git(&["pull", "--no-edit"])
    }

    /// Where the current branch pushes to: its upstream, or a new branch of
    /// the same name on the first remote.
    pub fn push_target(&self) -> Result<PushTarget> {
        let branch = self.current_branch().context("cannot push a detached HEAD")?;
        let config = |key: String| {
            self.git(&["config", "--get", &key])
                .ok()
                .map(|value| value.trim().to_string())
                .filter(|value| !value.is_empty())
        };
        let upstream_remote = config(format!("branch.{branch}.remote"));
        let upstream_branch = config(format!("branch.{branch}.merge"))
            .map(|merge| merge.trim_start_matches("refs/heads/").to_string());
        if let (Some(remote), Some(remote_branch)) = (upstream_remote, upstream_branch) {
            return Ok(PushTarget {
                branch,
                remote,
                remote_branch,
                is_new: false,
            });
        }
        let remote = self
            .git(&["remote"])?
            .lines()
            .next()
            .map(str::to_string)
            .context("no remote configured")?;
        Ok(PushTarget {
            remote_branch: branch.clone(),
            branch,
            remote,
            is_new: true,
        })
    }

    /// Commits that a push of the current branch would send.
    pub fn outgoing(&self, target: &PushTarget) -> Result<Vec<Commit>> {
        if target.is_new {
            self.log_revs(&["HEAD", "--not", "--remotes"], 1000)
        } else {
            let range = format!("{}/{}..HEAD", target.remote, target.remote_branch);
            self.log_revs(&[&range], 1000)
        }
    }

    pub fn push_to(&self, target: &PushTarget, force: bool) -> Result<String> {
        let refspec = format!("{}:{}", target.branch, target.remote_branch);
        let mut args = vec!["push"];
        if force {
            args.push("--force-with-lease");
        }
        if target.is_new {
            args.push("-u");
        }
        args.push(&target.remote);
        args.push(&refspec);
        self.git(&args)
    }

    pub fn push(&self) -> Result<String> {
        self.push_to(&self.push_target()?, false)
    }

    /// Commits from `from` up to HEAD, oldest first, for an interactive rebase.
    pub fn rebase_commits(&self, from: &str) -> Result<Vec<Commit>> {
        if self
            .git(&["merge-base", "--is-ancestor", from, "HEAD"])
            .is_err()
        {
            bail!("the selected commit is not an ancestor of HEAD");
        }
        let is_root = self.git(&["rev-parse", "--verify", "-q", &format!("{from}^")]).is_err();
        let range = format!("{from}^..HEAD");
        let mut commits = if is_root {
            self.log_revs(&["HEAD"], 100_000)?
        } else {
            self.log_revs(&["--topo-order", &range], 100_000)?
        };
        if commits.iter().any(|c| c.parents.len() > 1) {
            bail!("interactive rebase over merge commits is not supported");
        }
        commits.reverse();
        Ok(commits)
    }

    /// Run `git rebase -i` with a prepared todo list instead of an editor.
    pub fn interactive_rebase(&self, from: &str, plan: &[(String, RebaseAction)]) -> Result<String> {
        if matches!(plan.first().map(|(_, a)| a), Some(RebaseAction::Squash | RebaseAction::Fixup)) {
            bail!("the first commit cannot be squashed or fixed up");
        }
        let dir = self.rebase_files_dir()?;
        std::fs::create_dir_all(&dir)?;
        let mut todo = String::new();
        for (ix, (id, action)) in plan.iter().enumerate() {
            match action {
                RebaseAction::Pick => todo.push_str(&format!("pick {id}\n")),
                RebaseAction::Squash => todo.push_str(&format!("squash {id}\n")),
                RebaseAction::Fixup => todo.push_str(&format!("fixup {id}\n")),
                RebaseAction::Drop => todo.push_str(&format!("drop {id}\n")),
                RebaseAction::Reword(message) => {
                    let file = dir.join(format!("message-{ix}"));
                    std::fs::write(&file, message)?;
                    todo.push_str(&format!("pick {id}\n"));
                    todo.push_str(&format!(
                        "exec git commit --amend --allow-empty -q -F '{}'\n",
                        file.display()
                    ));
                }
            }
        }
        let todo_file = dir.join("todo");
        std::fs::write(&todo_file, todo)?;
        let sequence_editor = format!("sequence.editor=cp '{}'", todo_file.display());
        let is_root = self.git(&["rev-parse", "--verify", "-q", &format!("{from}^")]).is_err();
        let base = format!("{from}^");
        let mut args = vec!["-c", &sequence_editor, "-c", "core.editor=true", "rebase", "-i"];
        if is_root {
            args.push("--root");
        } else {
            args.push(&base);
        }
        let result = self.git(&args);
        // A rebase stopped on a conflict still needs the reworded messages.
        if self.repo_state() == RepoState::Clean {
            std::fs::remove_dir_all(&dir).ok();
        }
        result
    }

    /// Commits in `branch` that HEAD lacks, and commits in HEAD that `branch` lacks.
    pub fn compare_with_head(&self, branch: &str) -> Result<(Vec<Commit>, Vec<Commit>)> {
        let incoming = self.log_revs(&[&format!("HEAD..{branch}")], 10_000)?;
        let outgoing = self.log_revs(&[&format!("{branch}..HEAD")], 10_000)?;
        Ok((incoming, outgoing))
    }

    pub fn remotes(&self) -> Result<Vec<Remote>> {
        let mut remotes: Vec<Remote> = Vec::new();
        for line in self.git(&["remote", "-v"])?.lines() {
            // e.g. "origin\thttps://example.com/repo.git (fetch)"
            let Some((name, rest)) = line.split_once('\t') else { continue };
            let Some((url, kind)) = rest.rsplit_once(' ') else { continue };
            let remote = match remotes.iter_mut().find(|r| r.name == name) {
                Some(remote) => remote,
                None => {
                    remotes.push(Remote {
                        name: name.to_string(),
                        fetch_url: String::new(),
                        push_url: String::new(),
                    });
                    remotes.last_mut().unwrap()
                }
            };
            match kind {
                "(fetch)" => remote.fetch_url = url.to_string(),
                "(push)" => remote.push_url = url.to_string(),
                _ => {}
            }
        }
        Ok(remotes)
    }

    pub fn add_remote(&self, name: &str, url: &str) -> Result<String> {
        self.git(&["remote", "add", "--", name, url])
    }

    pub fn set_remote_url(&self, name: &str, url: &str) -> Result<String> {
        self.git(&["remote", "set-url", "--", name, url])
    }

    pub fn rename_remote(&self, old: &str, new: &str) -> Result<String> {
        self.git(&["remote", "rename", "--", old, new])
    }

    pub fn remove_remote(&self, name: &str) -> Result<String> {
        self.git(&["remote", "remove", "--", name])
    }

    pub fn stashes(&self) -> Result<Vec<Stash>> {
        let format = format!("--format=%gd{FIELD}%H{FIELD}%P{FIELD}%an{FIELD}%ae{FIELD}%at{FIELD}%s");
        let out = self.git(&["stash", "list", &format])?;
        Ok(out
            .lines()
            .filter_map(|line| {
                let mut fields = line.split(FIELD);
                let name = fields.next()?.to_string();
                let id = fields.next()?.to_string();
                let parents = fields.next()?.split_whitespace().map(str::to_string).collect();
                let author = fields.next()?.to_string();
                let email = fields.next()?.to_string();
                let time = fields.next()?.parse().unwrap_or(0);
                let message = fields.next().unwrap_or_default().to_string();
                Some(Stash {
                    name,
                    message: message.clone(),
                    commit: Commit {
                        id,
                        parents,
                        author,
                        email,
                        time,
                        subject: message,
                        refs: Vec::new(),
                    },
                })
            })
            .collect())
    }

    pub fn stash(&self, message: &str) -> Result<String> {
        if message.is_empty() {
            self.git(&["stash", "push", "--include-untracked"])
        } else {
            self.git(&["stash", "push", "--include-untracked", "-m", message])
        }
    }

    pub fn stash_pop(&self, name: &str) -> Result<String> {
        self.git(&["stash", "pop", name])
    }

    pub fn stash_apply(&self, name: &str) -> Result<String> {
        self.git(&["stash", "apply", name])
    }

    pub fn stash_drop(&self, name: &str) -> Result<String> {
        self.git(&["stash", "drop", name])
    }
}

fn command(dir: &Path, args: &[&str]) -> Command {
    let mut cmd = Command::new("git");
    cmd.arg("-C")
        .arg(dir)
        .args(["-c", "color.ui=false", "-c", "core.quotepath=false"])
        .args(args)
        .env("GIT_TERMINAL_PROMPT", "0")
        // Never touch the index behind the user's back (and don't wake our own
        // file watcher with it).
        .env("GIT_OPTIONAL_LOCKS", "0")
        .env("LC_ALL", "C");
    // Credentials and ssh passphrases are asked for in the app.
    if let Some(env) = crate::askpass::env() {
        cmd.envs(env);
    }
    cmd
}

fn run(dir: &Path, args: &[&str]) -> Result<String> {
    let started = std::time::Instant::now();
    let output = command(dir, args)
        .output()
        .context("failed to run git; is it installed?")?;
    if crate::console::is_recording() {
        crate::console::record(
            dir,
            args,
            &String::from_utf8_lossy(&output.stdout),
            &String::from_utf8_lossy(&output.stderr),
            output.status.success(),
            started.elapsed().as_millis(),
        );
    }
    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        let stdout = String::from_utf8_lossy(&output.stdout);
        let message = if stderr.trim().is_empty() { stdout } else { stderr };
        bail!("git {}: {}", args.join(" "), message.trim());
    }
    Ok(String::from_utf8_lossy(&output.stdout).into_owned())
}

/// `git diff --no-index` exits with 1 when the files differ.
fn run_allow_diff(dir: &Path, args: &[&str]) -> Result<String> {
    let output = command(dir, args).output().context("failed to run git")?;
    match output.status.code() {
        Some(0 | 1) => Ok(String::from_utf8_lossy(&output.stdout).into_owned()),
        _ => bail!("git {}: {}", args.join(" "), String::from_utf8_lossy(&output.stderr).trim()),
    }
}

fn parse_decorations(decorations: &str) -> Vec<RefLabel> {
    let mut labels = Vec::new();
    for decoration in decorations.split(", ").filter(|d| !d.is_empty()) {
        let (decoration, is_head) = match decoration.strip_prefix("HEAD -> ") {
            Some(rest) => (rest, true),
            None => (decoration, false),
        };
        if is_head || decoration == "HEAD" {
            labels.push(RefLabel {
                name: "HEAD".into(),
                kind: RefKind::Head,
            });
            if decoration == "HEAD" {
                continue;
            }
        }
        let decoration = decoration.strip_prefix("tag: ").unwrap_or(decoration);
        let label = if let Some(name) = decoration.strip_prefix("refs/heads/") {
            RefLabel {
                name: name.into(),
                kind: RefKind::Local,
            }
        } else if let Some(name) = decoration.strip_prefix("refs/remotes/") {
            if name.ends_with("/HEAD") {
                continue;
            }
            RefLabel {
                name: name.into(),
                kind: RefKind::Remote,
            }
        } else if let Some(name) = decoration.strip_prefix("refs/tags/") {
            RefLabel {
                name: name.into(),
                kind: RefKind::Tag,
            }
        } else {
            continue;
        };
        labels.push(label);
    }
    labels
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn decorations() {
        let labels = parse_decorations(
            "HEAD -> refs/heads/main, refs/remotes/origin/main, refs/remotes/origin/HEAD, tag: refs/tags/v1.0",
        );
        let names: Vec<_> = labels.iter().map(|l| (l.name.as_str(), l.kind.clone())).collect();
        assert_eq!(
            names,
            vec![
                ("HEAD", RefKind::Head),
                ("main", RefKind::Local),
                ("origin/main", RefKind::Remote),
                ("v1.0", RefKind::Tag),
            ]
        );
    }

    /// A throwaway repository under the system temp dir.
    struct TempRepo {
        repo: Repo,
        dir: PathBuf,
    }

    impl TempRepo {
        fn new(name: &str) -> Self {
            let dir = std::env::temp_dir().join(format!(
                "gitbrains-test-{name}-{}-{:?}",
                std::process::id(),
                std::time::SystemTime::now()
            ));
            std::fs::create_dir_all(&dir).unwrap();
            run(&dir, &["init", "-q", "-b", "main"]).unwrap();
            run(&dir, &["config", "user.name", "Test"]).unwrap();
            run(&dir, &["config", "user.email", "t@example.com"]).unwrap();
            let repo = Repo::open(&dir).unwrap();
            Self { repo, dir }
        }

        fn write(&self, path: &str, text: &str) {
            let path = self.dir.join(path);
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(path, text).unwrap();
        }

        fn commit(&self, path: &str, text: &str, message: &str) -> String {
            self.write(path, text);
            self.repo.git(&["add", "-A"]).unwrap();
            self.repo.git(&["commit", "-q", "-m", message]).unwrap();
            self.repo.git(&["rev-parse", "HEAD"]).unwrap().trim().to_string()
        }

        fn subjects(&self) -> Vec<String> {
            self.repo
                .git(&["log", "--format=%s"])
                .unwrap()
                .lines()
                .map(str::to_string)
                .collect()
        }
    }

    impl Drop for TempRepo {
        fn drop(&mut self) {
            std::fs::remove_dir_all(&self.dir).ok();
        }
    }

    #[test]
    fn interactive_rebase_squashes_rewords_and_drops() {
        let t = TempRepo::new("rebase");
        t.commit("a.txt", "0\n", "Base");
        let one = t.commit("a.txt", "0\n1\n", "One");
        let two = t.commit("b.txt", "2\n", "Two");
        let three = t.commit("c.txt", "3\n", "Three");
        let four = t.commit("d.txt", "4\n", "Four");

        let commits = t.repo.rebase_commits(&one).unwrap();
        let ids: Vec<_> = commits.iter().map(|c| c.id.clone()).collect();
        assert_eq!(ids, vec![one.clone(), two.clone(), three.clone(), four.clone()]);

        t.repo
            .interactive_rebase(
                &one,
                &[
                    (one.clone(), RebaseAction::Pick),
                    (two, RebaseAction::Fixup),
                    (three, RebaseAction::Drop),
                    (four, RebaseAction::Reword("Four, reworded".into())),
                ],
            )
            .unwrap();
        assert_eq!(t.subjects(), vec!["Four, reworded", "One", "Base"]);
        assert!(t.dir.join("b.txt").exists());
        assert!(!t.dir.join("c.txt").exists());
        assert_eq!(t.repo.repo_state(), RepoState::Clean);
    }

    #[test]
    fn reordering_conflicting_commits_stops_and_aborts() {
        let t = TempRepo::new("rebase-conflict");
        t.commit("a.txt", "0\n", "Base");
        let one = t.commit("a.txt", "0\n1\n", "One");
        let two = t.commit("a.txt", "0\n1\n2\n", "Two");
        let result = t
            .repo
            .interactive_rebase(&one, &[(two, RebaseAction::Pick), (one.clone(), RebaseAction::Pick)]);
        assert!(result.is_err());
        assert_eq!(t.repo.repo_state(), RepoState::Rebasing);
        assert!(t.repo.status().unwrap().iter().any(|e| e.is_conflicted()));
        t.repo.abort(RepoState::Rebasing).unwrap();
        assert_eq!(t.repo.repo_state(), RepoState::Clean);
        assert_eq!(t.subjects(), vec!["Two", "One", "Base"]);
    }

    #[test]
    fn first_commit_cannot_be_squashed() {
        let t = TempRepo::new("rebase-invalid");
        t.commit("a.txt", "0\n", "Base");
        let one = t.commit("a.txt", "1\n", "One");
        assert!(t.repo.interactive_rebase(&one, &[(one.clone(), RebaseAction::Squash)]).is_err());
    }

    #[test]
    fn push_sets_upstream_then_reports_nothing_outgoing() {
        let t = TempRepo::new("push");
        let remote = TempRepo::new("push-remote");
        run(&remote.dir, &["config", "receive.denyCurrentBranch", "ignore"]).unwrap();
        t.repo.git(&["remote", "add", "origin", &remote.dir.display().to_string()]).unwrap();
        t.commit("a.txt", "a\n", "First");
        t.commit("a.txt", "b\n", "Second");

        let target = t.repo.push_target().unwrap();
        assert!(target.is_new);
        assert_eq!((target.remote.as_str(), target.remote_branch.as_str()), ("origin", "main"));
        assert_eq!(t.repo.outgoing(&target).unwrap().len(), 2);

        t.repo.push_to(&target, false).unwrap();
        let target = t.repo.push_target().unwrap();
        assert!(!target.is_new);
        assert!(t.repo.outgoing(&target).unwrap().is_empty());

        t.commit("a.txt", "c\n", "Third");
        let outgoing = t.repo.outgoing(&target).unwrap();
        assert_eq!(outgoing.len(), 1);
        assert_eq!(outgoing[0].subject, "Third");
    }

    #[test]
    fn history_follows_renames_and_blame_attributes_lines() {
        let t = TempRepo::new("history");
        t.commit("old.txt", "one\n", "Create");
        t.repo.git(&["mv", "old.txt", "new.txt"]).unwrap();
        t.repo.git(&["commit", "-q", "-m", "Rename"]).unwrap();
        t.commit("new.txt", "one\ntwo\n", "Extend");

        let history = t.repo.file_history("new.txt").unwrap();
        let subjects: Vec<_> = history.iter().map(|r| r.commit.subject.as_str()).collect();
        assert_eq!(subjects, vec!["Extend", "Rename", "Create"]);
        assert_eq!(history.last().unwrap().path, "old.txt");

        let blame = t.repo.blame("HEAD", "new.txt").unwrap();
        assert_eq!(blame.len(), 2);
        assert_eq!(blame[0].summary, "Create");
        assert_eq!(blame[1].summary, "Extend");
        assert_eq!(blame[1].text, "two");
    }

    #[test]
    fn commit_only_selected_paths() {
        let t = TempRepo::new("commit");
        t.commit("a.txt", "a\n", "Base");
        t.write("a.txt", "changed\n");
        t.write("new.txt", "new\n");
        t.write("skip.txt", "skip\n");
        let status = t.repo.status().unwrap();
        assert_eq!(status.len(), 3);
        assert!(status.iter().any(|e| e.path == "new.txt" && e.is_untracked()));

        t.repo
            .commit("Selected", &["a.txt".into(), "new.txt".into()], false)
            .unwrap();
        let remaining: Vec<_> = t.repo.status().unwrap().into_iter().map(|e| e.path).collect();
        assert_eq!(remaining, vec!["skip.txt"]);
        let details = t.repo.commit_details("HEAD").unwrap();
        let mut files: Vec<_> = details.files.iter().map(|f| (f.status, f.path.as_str())).collect();
        files.sort();
        assert_eq!(files, vec![('A', "new.txt"), ('M', "a.txt")]);
    }

    #[test]
    fn compare_lists_commits_on_each_side() {
        let t = TempRepo::new("compare");
        t.commit("a.txt", "a\n", "Base");
        t.repo.git(&["checkout", "-q", "-b", "topic"]).unwrap();
        t.commit("b.txt", "b\n", "Topic one");
        t.commit("c.txt", "c\n", "Topic two");
        t.repo.git(&["checkout", "-q", "main"]).unwrap();
        t.commit("d.txt", "d\n", "Main only");

        let (incoming, outgoing) = t.repo.compare_with_head("topic").unwrap();
        let subjects = |commits: &[Commit]| commits.iter().map(|c| c.subject.clone()).collect::<Vec<_>>();
        assert_eq!(subjects(&incoming), vec!["Topic two", "Topic one"]);
        assert_eq!(subjects(&outgoing), vec!["Main only"]);
    }

    #[test]
    fn stashes_expose_their_commit_and_files() {
        let t = TempRepo::new("stash");
        let base = t.commit("a.txt", "a\n", "Base");
        t.write("a.txt", "changed\n");
        t.repo.stash("Work in progress").unwrap();

        let stashes = t.repo.stashes().unwrap();
        assert_eq!(stashes.len(), 1);
        assert_eq!(stashes[0].name, "stash@{0}");
        assert!(stashes[0].message.contains("Work in progress"));
        assert_eq!(stashes[0].commit.parents.first(), Some(&base));

        let details = t.repo.commit_details(&stashes[0].commit.id).unwrap();
        let files: Vec<_> = details.files.iter().map(|f| (f.status, f.path.as_str())).collect();
        assert_eq!(files, vec![('M', "a.txt")]);
    }

    #[test]
    fn not_ignored_filters_gitignored_paths() {
        let t = TempRepo::new("ignore");
        t.commit(".gitignore", "target/\n*.log\n", "Ignore");
        let paths = vec![
            t.dir.join("src/main.rs"),
            t.dir.join("target/debug/app"),
            t.dir.join("run.log"),
        ];
        assert_eq!(t.repo.not_ignored(&paths).unwrap(), vec![t.dir.join("src/main.rs")]);
    }

    #[test]
    fn clone_dir_names() {
        assert_eq!(Repo::clone_dir_name("https://github.com/zed-industries/zed.git"), "zed");
        assert_eq!(Repo::clone_dir_name("git@github.com:longbridge/gpui-kit.git"), "gpui-kit");
        assert_eq!(Repo::clone_dir_name("/srv/repos/project/"), "project");
    }

    #[test]
    fn clone_from_local_repository() {
        let source = TempRepo::new("clone-source");
        source.commit("a.txt", "a\n", "First");
        let dest = source.dir.with_extension("clone");
        let repo = Repo::clone_from(&source.dir.display().to_string(), &dest).unwrap();
        assert_eq!(repo.current_branch().as_deref(), Some("main"));
        assert!(dest.join("a.txt").exists());
        std::fs::remove_dir_all(&dest).ok();
    }

    #[test]
    fn manage_remotes() {
        let t = TempRepo::new("remotes");
        assert!(t.repo.remotes().unwrap().is_empty());
        t.repo.add_remote("origin", "https://example.com/a.git").unwrap();
        t.repo.add_remote("upstream", "git@example.com:b.git").unwrap();
        t.repo.set_remote_url("origin", "https://example.com/c.git").unwrap();
        t.repo.rename_remote("upstream", "fork").unwrap();
        let remotes = t.repo.remotes().unwrap();
        let summary: Vec<_> = remotes.iter().map(|r| (r.name.as_str(), r.fetch_url.as_str())).collect();
        assert_eq!(summary, vec![("fork", "git@example.com:b.git"), ("origin", "https://example.com/c.git")]);
        assert_eq!(remotes[1].push_url, "https://example.com/c.git");
        t.repo.remove_remote("fork").unwrap();
        assert_eq!(t.repo.remotes().unwrap().len(), 1);
    }
}
