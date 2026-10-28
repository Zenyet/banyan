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
    pub name: String,
    pub message: String,
}

impl Repo {
    pub fn open(path: &Path) -> Result<Self> {
        let out = run(path, &["rev-parse", "--show-toplevel"])
            .with_context(|| format!("{} is not a git repository", path.display()))?;
        Ok(Self {
            root: PathBuf::from(out.trim()),
        })
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
        self.git(args)
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

    pub fn stashes(&self) -> Result<Vec<Stash>> {
        let out = self.git(&["stash", "list", &format!("--format=%gd{FIELD}%s")])?;
        Ok(out
            .lines()
            .filter_map(|line| {
                let (name, message) = line.split_once(FIELD)?;
                Some(Stash {
                    name: name.to_string(),
                    message: message.to_string(),
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
        .env("LC_ALL", "C");
    cmd
}

fn run(dir: &Path, args: &[&str]) -> Result<String> {
    let output = command(dir, args)
        .output()
        .context("failed to run git; is it installed?")?;
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
}
