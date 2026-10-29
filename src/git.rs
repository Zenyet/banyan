//! Thin wrapper over the `git` CLI, the same backend JetBrains IDEs use.

use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context as _, Result, anyhow, bail};

use crate::i18n::tr;

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

/// Parse `--name-status -z` output.
fn parse_name_status(out: &str) -> Vec<FileChange> {
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
    files
}

/// Whether a failed checkout was refused because of local changes.
pub fn is_blocked_by_local_changes(err: &anyhow::Error) -> bool {
    let message = format!("{err:#}");
    message.contains("would be overwritten by checkout") || message.contains("would be removed by checkout")
}

/// The files git lists in a refused checkout's message.
pub fn blocked_files(message: &str) -> Vec<String> {
    message
        .lines()
        .skip_while(|line| !line.trim_end().ends_with("by checkout:"))
        .skip(1)
        .map(str::trim)
        .take_while(|line| !line.is_empty() && !line.starts_with("Please") && !line.starts_with("Aborting"))
        .map(str::to_string)
        .collect()
}

/// `-w` while the settings leave whitespace-only changes out of diffs.
fn whitespace_args() -> &'static [&'static str] {
    if crate::settings::Settings::read(|s| s.ignore_whitespace) { &["-w"] } else { &[] }
}

/// The branch or commit a merge message names: `feature` in
/// "Merge branch 'feature' into main" or "Merge remote-tracking branch 'origin/x'".
fn merged_name(message: &str) -> Option<String> {
    let first = message.lines().next()?;
    let rest = ["Merge branch '", "Merge remote-tracking branch '", "Merge tag '", "Merge commit '"]
        .iter()
        .find_map(|prefix| first.strip_prefix(prefix))?;
    rest.split('\'').next().filter(|name| !name.is_empty()).map(str::to_string)
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

    /// What each side of a conflict did to the file: (yours, theirs).
    pub fn conflict_sides(&self) -> Option<(ConflictSide, ConflictSide)> {
        use ConflictSide::*;
        Some(match (self.index, self.worktree) {
            ('U', 'U') => (Modified, Modified),
            ('A', 'A') => (Added, Added),
            ('D', 'U') => (Deleted, Modified),
            ('U', 'D') => (Modified, Deleted),
            ('A', 'U') => (Added, Absent),
            ('U', 'A') => (Absent, Added),
            ('D', 'D') => (Deleted, Deleted),
            _ => return None,
        })
    }

    /// Whether both sides have content to merge line by line.
    pub fn can_merge_text(&self) -> bool {
        self.conflict_sides()
            .is_some_and(|(ours, theirs)| ours.has_content() && theirs.has_content())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ConflictSide {
    Modified,
    Added,
    Deleted,
    /// The side never had the file.
    Absent,
}

impl ConflictSide {
    pub fn label(self) -> &'static str {
        match self {
            ConflictSide::Modified => "Modified",
            ConflictSide::Added => "Added",
            ConflictSide::Deleted => "Deleted",
            ConflictSide::Absent => "—",
        }
    }

    fn has_content(self) -> bool {
        matches!(self, ConflictSide::Modified | ConflictSide::Added)
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

impl RepoState {
    /// The operation's name for buttons, e.g. "Abort Merge".
    pub fn noun(self) -> &'static str {
        match self {
            RepoState::Clean => "",
            RepoState::Merging => tr!("Merge"),
            RepoState::Rebasing => tr!("Rebase"),
            RepoState::CherryPicking => tr!("Cherry-Pick"),
            RepoState::Reverting => tr!("Revert"),
        }
    }

    /// The label of the button that finishes the operation.
    pub fn continue_label(self) -> String {
        match self {
            RepoState::Merging => tr!("Commit Merge").into(),
            state => tr!("Continue {}", state.noun()),
        }
    }
}

/// A merge, rebase, cherry-pick or revert that stopped, as the operation
/// banner and the merge tool describe it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Operation {
    pub state: RepoState,
    /// e.g. "Merging feature into main".
    pub title: String,
    /// Names of git's "ours" and "theirs" sides of a conflict.
    pub ours: String,
    pub theirs: String,
}

/// Conflict markers longer than git's usual seven, so that markers already
/// in a file's content are not mistaken for ours.
pub const MARKER_SIZE: usize = 12;

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

/// A working tree of the repository, from `git worktree list`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Worktree {
    pub path: PathBuf,
    pub head: String,
    /// The branch checked out there; `None` when detached.
    pub branch: Option<String>,
    /// The repository's first working tree, which cannot be removed.
    pub is_main: bool,
    /// The working tree this window shows.
    pub is_current: bool,
    pub locked: bool,
    /// Its folder is gone; pruning forgets it.
    pub prunable: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SubmoduleState {
    /// Not cloned yet.
    Uninitialized,
    UpToDate,
    /// Checked out at another commit than the one recorded.
    OutOfDate,
    Conflict,
}

/// A submodule, from `git submodule status`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Submodule {
    pub path: String,
    /// The commit checked out, or recorded when not initialized.
    pub commit: String,
    pub state: SubmoduleState,
}

/// Changes put aside with Shelve, kept as a patch in `banyan/shelf/<id>` of
/// the git directory, like JetBrains' shelf.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Shelf {
    pub id: String,
    pub name: String,
    /// The changelist the changes came from, which unshelving restores.
    pub changelist: String,
    pub time: i64,
    pub files: Vec<String>,
}

impl Shelf {
    fn read(dir: &Path) -> Option<Self> {
        let info = std::fs::read_to_string(dir.join("info")).ok()?;
        let mut shelf = Shelf {
            id: dir.file_name()?.to_string_lossy().into_owned(),
            name: String::new(),
            changelist: crate::changelists::DEFAULT.to_string(),
            time: 0,
            files: Vec::new(),
        };
        for line in info.lines() {
            match line.split_once('=') {
                Some(("name", name)) => shelf.name = name.to_string(),
                Some(("changelist", list)) => shelf.changelist = list.to_string(),
                Some(("time", time)) => shelf.time = time.parse().unwrap_or(0),
                Some(("file", path)) => shelf.files.push(path.to_string()),
                _ => {}
            }
        }
        Some(shelf)
    }

    fn write(&self, dir: &Path) -> Result<()> {
        let one_line = |text: &str| text.replace(['\n', '\r'], " ");
        let mut info = format!(
            "name={}\nchangelist={}\ntime={}\n",
            one_line(&self.name),
            one_line(&self.changelist),
            self.time
        );
        for file in &self.files {
            info.push_str(&format!("file={file}\n"));
        }
        std::fs::write(dir.join("info"), info)?;
        Ok(())
    }
}

/// The per-file parts of a patch, each starting at its `diff --git` line.
fn patch_blocks(patch: &str) -> Vec<&str> {
    let mut starts: Vec<usize> = patch.match_indices("diff --git ").map(|(ix, _)| ix).filter(|&ix| ix == 0 || patch.as_bytes()[ix - 1] == b'\n').collect();
    starts.push(patch.len());
    starts.windows(2).map(|w| &patch[w[0]..w[1]]).collect()
}

/// Whether a patch block is the one for `path` (its new name).
fn block_is_for(block: &str, path: &str) -> bool {
    block.lines().next().is_some_and(|header| header.ends_with(&format!(" b/{path}")))
}

/// `path` as a literal pattern for `git apply --include`.
fn glob_literal(path: &str) -> String {
    let mut out = String::new();
    for c in path.chars() {
        if matches!(c, '*' | '?' | '[' | ']' | '\\') {
            out.push('\\');
        }
        out.push(c);
    }
    out
}

/// A change to the current branch's history, as the Log's commit menu offers.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Rewrite {
    Reword { id: String, message: String },
    /// Meld `ids` into the oldest of them, with `message`.
    Squash { ids: Vec<String>, message: String },
    /// Meld a commit into its parent, keeping the parent's message.
    FixupIntoParent { id: String },
    Drop { ids: Vec<String> },
}

impl Rewrite {
    /// The oldest commit whose content or message changes.
    fn ids(&self) -> Vec<String> {
        match self {
            Rewrite::Reword { id, .. } | Rewrite::FixupIntoParent { id } => vec![id.clone()],
            Rewrite::Squash { ids, .. } | Rewrite::Drop { ids } => ids.clone(),
        }
    }
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

    /// Create a repository in `dir`.
    pub fn init(dir: &Path) -> Result<Self> {
        run(dir, &["init"])?;
        Repo::open(dir)
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

    /// Commits of `refs` (all branches, remotes and tags by default), newest
    /// first, optionally only those committed since a git date such as
    /// `7.days.ago` or touching `path`. Unlike `--since`, the date filter
    /// looks past old commits that sit above newer ones.
    pub fn log(&self, refs: Option<&str>, since: Option<&str>, path: Option<&str>, limit: usize) -> Result<Vec<Commit>> {
        let since = since.map(|since| format!("--since-as-filter={since}"));
        let mut revs: Vec<&str> = match refs {
            Some(refs) => vec![refs],
            None => vec!["--branches", "--remotes", "--tags", "HEAD"],
        };
        revs.extend(since.as_deref());
        self.log_paths(&revs, path.as_slice(), limit)
    }

    /// `git log` over arbitrary revision arguments, newest first.
    pub fn log_revs(&self, revs: &[&str], limit: usize) -> Result<Vec<Commit>> {
        self.log_paths(revs, &[], limit)
    }

    fn log_paths(&self, revs: &[&str], paths: &[&str], limit: usize) -> Result<Vec<Commit>> {
        let format = format!("--format=%H{FIELD}%P{FIELD}%an{FIELD}%ae{FIELD}%at{FIELD}%D{FIELD}%s{RECORD}");
        let limit = format!("-n{limit}");
        let mut args = vec!["log", "--date-order", "--decorate=full", &format, &limit];
        args.extend(revs);
        args.push("--");
        args.extend(paths);
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
        let mut args = vec!["show", "--format=", "-M", "--first-parent"];
        args.extend(whitespace_args());
        args.extend([id, "--", path]);
        self.git(&args)
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
        Ok(parse_name_status(&out))
    }

    /// Files that differ from `from` to `to`, or to the work tree when `to` is `None`.
    pub fn changes_between(&self, from: &str, to: Option<&str>) -> Result<Vec<FileChange>> {
        let mut args = vec!["diff", "-M", "--name-status", "-z", from];
        args.extend(to);
        args.push("--");
        Ok(parse_name_status(&self.git(&args)?))
    }

    /// Patch of one file from `from` to `to`, or to the work tree when `to` is `None`.
    pub fn diff_between(&self, from: &str, to: Option<&str>, file: &FileChange) -> Result<String> {
        let mut args = vec!["diff", "-M"];
        args.extend(whitespace_args());
        args.push(from);
        args.extend(to);
        args.extend(["--", file.path.as_str()]);
        if let Some(orig) = &file.orig_path {
            args.push(orig);
        }
        self.git(&args)
    }

    /// Patch of one file in a commit, against its first parent.
    pub fn commit_file_diff(&self, details: &CommitDetails, file: &FileChange) -> Result<String> {
        let mut paths = vec![file.path.as_str()];
        if let Some(orig) = &file.orig_path {
            paths.push(orig);
        }
        let mut args: Vec<&str> = Vec::new();
        match details.parents.first() {
            Some(parent) => {
                args.extend(["diff", "-M"]);
                args.extend(whitespace_args());
                args.extend([parent.as_str(), details.id.as_str()]);
            }
            None => {
                args.extend(["show", "--format=", "-M"]);
                args.extend(whitespace_args());
                args.push(details.id.as_str());
            }
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
    pub fn worktree_diff(&self, entry: &StatusEntry, ignore_whitespace: bool) -> Result<String> {
        if entry.is_untracked() {
            return run_allow_diff(&self.root, &["diff", "--no-index", "--", "/dev/null", &entry.path]);
        }
        let mut args = vec!["diff", "-M"];
        if ignore_whitespace {
            args.push("-w");
        }
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

    /// The full message of a commit.
    pub fn commit_message(&self, id: &str) -> Result<String> {
        Ok(self.git(&["log", "-1", "--format=%B", id])?.trim_end().to_string())
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

    /// Commit whole files plus only some changed lines of others. `partial`
    /// pairs a modified file with the lines to leave out.
    ///
    /// The index is rebuilt from HEAD: staging done outside the app is not
    /// kept, matching the changelist model of the Commit tab.
    pub fn commit_partial(
        &self,
        message: &str,
        full: &[String],
        partial: &[(String, Vec<crate::patch::ChangedLine>)],
        amend: bool,
    ) -> Result<String> {
        if self.repo_state() != RepoState::Clean {
            bail!("partial commits are not possible while a merge or rebase is in progress");
        }
        self.git(&["reset", "-q"])?;
        if !full.is_empty() {
            let mut add = vec!["add", "-A", "--"];
            add.extend(full.iter().map(String::as_str));
            self.git(&add)?;
        }
        for (path, excluded) in partial {
            let diff = self.git(&["diff", "HEAD", "--", path])?;
            let file = crate::patch::parse(&diff);
            let keep = |hunk: &crate::patch::Hunk, ix: usize| {
                !excluded.iter().any(|(header, line)| *header == hunk.header && *line == ix)
            };
            if let Some(patch) = crate::patch::select(&file, keep) {
                self.git_with_input(&["apply", "--cached", "--recount", "-"], &patch)?;
            }
        }
        let mut args = vec!["commit", "-m", message];
        if amend {
            args.push("--amend");
        }
        self.git(&args)
    }

    /// Run git with `input` on stdin.
    pub fn git_with_input(&self, args: &[&str], input: &str) -> Result<String> {
        use std::io::Write as _;
        let mut child = command(&self.root, args)
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .context("failed to run git")?;
        child.stdin.take().context("no stdin")?.write_all(input.as_bytes())?;
        let output = child.wait_with_output()?;
        if crate::console::is_recording() {
            crate::console::record(
                &self.root,
                args,
                &String::from_utf8_lossy(&output.stdout),
                &String::from_utf8_lossy(&output.stderr),
                output.status.success(),
                0,
            );
        }
        if !output.status.success() {
            bail!("git {}: {}", args.join(" "), String::from_utf8_lossy(&output.stderr).trim());
        }
        Ok(String::from_utf8_lossy(&output.stdout).into_owned())
    }

    /// Append `path` to the top-level `.gitignore`, anchored to the root.
    pub fn add_to_gitignore(&self, path: &str) -> Result<()> {
        let file = self.root.join(".gitignore");
        let mut text = std::fs::read_to_string(&file).unwrap_or_default();
        if !text.is_empty() && !text.ends_with('\n') {
            text.push('\n');
        }
        text.push_str(&format!("/{}\n", path.trim_end_matches('/')));
        std::fs::write(&file, text).with_context(|| format!("failed to write {}", file.display()))
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

    /// Check out a local branch, a remote branch (tracking it locally) or a revision.
    pub fn checkout_any(&self, target: &str, remote: bool) -> Result<String> {
        if remote { self.checkout_remote(target) } else { self.checkout(target) }
    }

    /// Stash local changes, check out, and bring the changes back, like
    /// JetBrains' "Smart Checkout".
    pub fn smart_checkout(&self, target: &str, remote: bool) -> Result<String> {
        self.git(&["stash", "push", "--include-untracked", "-m", &format!("Banyan: smart checkout of {target}")])?;
        if let Err(err) = self.checkout_any(target, remote) {
            // Put things back the way they were.
            self.git(&["stash", "pop", "--index"]).ok();
            return Err(err);
        }
        self.git(&["stash", "pop"]).map_err(|err| {
            anyhow::anyhow!(
                "Checked out {target}, but restoring your changes conflicted. Resolve the conflicts \
                 in the Commit tab; your changes are also kept in the top stash.\n{err:#}"
            )
        })
    }

    /// Check out and discard local changes that would be overwritten.
    pub fn force_checkout(&self, target: &str, remote: bool) -> Result<String> {
        if remote {
            let local = target.split_once('/').map(|(_, name)| name).unwrap_or(target);
            if self.git(&["rev-parse", "--verify", "-q", &format!("refs/heads/{local}")]).is_ok() {
                return self.git(&["checkout", "-f", local]);
            }
            return self.git(&["checkout", "-f", "-b", local, "--track", target]);
        }
        self.git(&["checkout", "-f", target])
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

    /// Apply `ids` in order, oldest first.
    pub fn cherry_pick(&self, ids: &[String]) -> Result<String> {
        let mut args = vec!["cherry-pick"];
        args.extend(ids.iter().map(String::as_str));
        self.git(&args)
    }

    /// Revert `ids` in order, newest first.
    pub fn revert(&self, ids: &[String]) -> Result<String> {
        let mut args = vec!["revert", "--no-edit"];
        args.extend(ids.iter().map(String::as_str));
        self.git(&args)
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
        Ok(self.git_dir()?.join("banyan-rebase"))
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

    /// Take one side of a conflicted file, or with `None` mark it resolved as it is.
    /// Taking the side that deleted the file deletes it.
    pub fn resolve_conflict(&self, path: &str, ours: Option<bool>) -> Result<String> {
        if let Some(ours) = ours {
            if !self.conflict_stages(path)?.contains(&if ours { 2 } else { 3 }) {
                return self.git(&["rm", "--quiet", "--", path]);
            }
            self.git(&["checkout", if ours { "--ours" } else { "--theirs" }, "--", path])?;
        }
        self.git(&["add", "-A", "--", path])
    }

    /// Which index stages a conflicted file has: 1 base, 2 ours, 3 theirs.
    fn conflict_stages(&self, path: &str) -> Result<Vec<u8>> {
        Ok(self
            .git(&["ls-files", "-u", "--", path])?
            .lines()
            .filter_map(|line| line.split('\t').next()?.rsplit(' ').next()?.parse().ok())
            .collect())
    }

    /// git's three-way merge of a conflicted file's yours, base and theirs
    /// versions, with diff3-style markers of [`MARKER_SIZE`] around what
    /// still conflicts. Changes that merged cleanly are already applied.
    pub fn merge_conflict_text(&self, path: &str) -> Result<String> {
        /// git knows the empty blob in every repository.
        const EMPTY_BLOB: &str = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391";
        let stages = self.conflict_stages(path)?;
        if !stages.contains(&2) || !stages.contains(&3) {
            bail!("{path} was deleted on one side; choose Accept Yours or Accept Theirs");
        }
        let ours = format!(":2:{path}");
        let theirs = format!(":3:{path}");
        let base = if stages.contains(&1) { format!(":1:{path}") } else { EMPTY_BLOB.to_string() };
        let marker_size = MARKER_SIZE.to_string();
        let args = [
            "merge-file", "-p", "--object-id", "--zdiff3", "--marker-size", &marker_size,
            "-L", "yours", "-L", "base", "-L", "theirs", &ours, &base, &theirs,
        ];
        let output = command(&self.root, &args).output().context("failed to run git")?;
        // The exit code is the number of conflicts; errors are negative.
        if !output.status.code().is_some_and(|code| (0..128).contains(&code)) {
            bail!("Cannot merge {path}: {}", String::from_utf8_lossy(&output.stderr).trim());
        }
        let text = String::from_utf8(output.stdout).map_err(|_| anyhow!("{path} is not UTF-8 text"))?;
        if text.contains('\0') {
            bail!("{path} is a binary file; choose Accept Yours or Accept Theirs");
        }
        Ok(text)
    }

    /// The stopped merge, rebase, cherry-pick or revert, if any.
    pub fn operation(&self) -> Option<Operation> {
        let state = self.repo_state();
        if state == RepoState::Clean {
            return None;
        }
        let git_dir = self.git_dir().ok()?;
        let read = |name: &str| {
            std::fs::read_to_string(git_dir.join(name))
                .ok()
                .map(|text| text.trim().to_string())
                .filter(|text| !text.is_empty())
        };
        let head = self
            .current_branch()
            .or_else(|| self.head().map(|id| id[..id.len().min(8)].to_string()))
            .unwrap_or_else(|| "HEAD".into());
        let operation = |title: String, ours: String, theirs: String| Operation { state, title, ours, theirs };
        Some(match state {
            RepoState::Clean => return None,
            RepoState::Merging => {
                let theirs = read("MERGE_MSG")
                    .and_then(|message| merged_name(&message))
                    .or_else(|| read("MERGE_HEAD").map(|id| self.describe(&id)))
                    .unwrap_or_else(|| tr!("the other branch").into());
                operation(tr!("Merging {} into {}", theirs, head), head, theirs)
            }
            RepoState::Rebasing => {
                let dir = if git_dir.join("rebase-merge").exists() { "rebase-merge" } else { "rebase-apply" };
                let branch = read(&format!("{dir}/head-name"))
                    .map(|name| name.trim_start_matches("refs/heads/").to_string())
                    .unwrap_or_else(|| tr!("detached HEAD").into());
                let onto = read(&format!("{dir}/onto"))
                    .map(|id| self.describe(&id))
                    .unwrap_or_else(|| tr!("upstream").into());
                let (step, total) = if dir == "rebase-merge" { ("msgnum", "end") } else { ("next", "last") };
                let progress = match (read(&format!("{dir}/{step}")), read(&format!("{dir}/{total}"))) {
                    (Some(step), Some(total)) => tr!(" (commit {} of {})", step, total),
                    _ => String::new(),
                };
                let applying = read("REBASE_HEAD")
                    .map(|id| self.commit_label(&id))
                    .unwrap_or_else(|| branch.clone());
                operation(tr!("Rebasing {} onto {}{}", branch, onto, progress), onto, applying)
            }
            RepoState::CherryPicking => {
                let commit = read("CHERRY_PICK_HEAD").map(|id| self.commit_label(&id)).unwrap_or_default();
                operation(tr!("Cherry-picking {}", commit), head, commit)
            }
            RepoState::Reverting => {
                let commit = read("REVERT_HEAD").map(|id| self.commit_label(&id)).unwrap_or_default();
                operation(tr!("Reverting {}", commit), head, tr!("revert of {}", commit))
            }
        })
    }

    /// A branch-relative name for a commit, like `feature` or `main~2`.
    fn describe(&self, id: &str) -> String {
        self.git(&["name-rev", "--name-only", "--no-undefined", "--exclude=refs/tags/*", id])
            .map(|name| name.trim().trim_start_matches("remotes/").to_string())
            .unwrap_or_else(|_| id[..id.len().min(8)].to_string())
    }

    /// "abc1234 Subject" of a commit.
    fn commit_label(&self, id: &str) -> String {
        self.git(&["log", "-1", "--format=%h %s", id])
            .map(|label| label.trim().to_string())
            .unwrap_or_else(|_| id[..id.len().min(8)].to_string())
    }

    pub fn fetch(&self) -> Result<String> {
        self.git(&["fetch", "--all", "--prune"])
    }

    /// Fetch in the background: never asks for credentials, as a prompt
    /// nobody asked for would be confusing. Stored credentials still work.
    pub fn fetch_quietly(&self) -> Result<()> {
        let output = command(&self.root, &["fetch", "--all", "--prune", "--quiet"])
            .env_remove("GIT_ASKPASS")
            .env_remove("SSH_ASKPASS")
            .env_remove("SSH_ASKPASS_REQUIRE")
            .output()
            .context("failed to run git")?;
        if !output.status.success() {
            bail!("git fetch: {}", String::from_utf8_lossy(&output.stderr).trim());
        }
        Ok(())
    }

    pub fn pull(&self) -> Result<String> {
        self.git(&["pull", "--no-edit"])
    }

    /// JetBrains' "Update Project": pull the tracked branch by merging or
    /// rebasing, stashing local changes around it.
    pub fn update_project(&self, rebase: bool) -> Result<String> {
        let branch = self.current_branch().context("cannot update a detached HEAD")?;
        if self.git(&["config", "--get", &format!("branch.{branch}.merge")]).is_err() {
            bail!("{branch} has no tracked remote branch; push it first or set an upstream");
        }
        self.git(&[
            "pull",
            "--autostash",
            "--no-edit",
            if rebase { "--rebase" } else { "--no-rebase" },
        ])
    }

    /// Where the current branch pushes to: its upstream, or a new branch of
    /// the same name on the first remote.
    pub fn push_target(&self) -> Result<PushTarget> {
        let branch = self.current_branch().context("cannot push a detached HEAD")?;
        self.push_target_for(&branch)
    }

    /// Where local branch `branch` pushes to, as [`Self::push_target`] says.
    pub fn push_target_for(&self, branch: &str) -> Result<PushTarget> {
        let branch = branch.to_string();
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

    /// Push the branch, and with `tags` every local tag too.
    pub fn push_to(&self, target: &PushTarget, force: bool, tags: bool) -> Result<String> {
        let refspec = format!("{}:{}", target.branch, target.remote_branch);
        let mut args = vec!["push"];
        if force {
            args.push("--force-with-lease");
        }
        if tags {
            args.push("--tags");
        }
        if target.is_new {
            args.push("-u");
        }
        args.push(&target.remote);
        args.push(&refspec);
        self.git(&args)
    }

    pub fn push(&self) -> Result<String> {
        self.push_to(&self.push_target()?, false, false)
    }

    /// Push local branch `branch`, current or not.
    pub fn push_branch(&self, branch: &str) -> Result<String> {
        self.push_to(&self.push_target_for(branch)?, false, false)
    }

    /// Fast-forward a local branch that is not checked out to its upstream.
    pub fn update_branch(&self, branch: &str) -> Result<String> {
        if self.current_branch().as_deref() == Some(branch) {
            bail!("{branch} is checked out; use Update Project");
        }
        let target = self.push_target_for(branch)?;
        if target.is_new {
            bail!("{branch} has no tracked remote branch");
        }
        // Without a leading +, fetch refuses anything but a fast-forward.
        let refspec = format!("{}:{}", target.remote_branch, branch);
        self.git(&["fetch", &target.remote, &refspec])
    }

    /// Make `branch` track remote branch `upstream` (e.g. `origin/main`), or
    /// track nothing with `None`.
    pub fn set_upstream(&self, branch: &str, upstream: Option<&str>) -> Result<String> {
        match upstream {
            Some(upstream) => self.git(&["branch", &format!("--set-upstream-to={upstream}"), branch]),
            None => self.git(&["branch", "--unset-upstream", branch]),
        }
    }

    /// `ids` (oldest first) as one patch file, with their messages and
    /// authors, as `git format-patch` writes them.
    pub fn create_patch(&self, ids: &[String], dest: &Path) -> Result<()> {
        let mut patch = String::new();
        for id in ids {
            patch.push_str(&self.git(&["format-patch", "--stdout", "--binary", "-1", id])?);
        }
        std::fs::write(dest, patch).with_context(|| format!("cannot write {}", dest.display()))
    }

    /// Apply a patch file to the work tree; hunks that no longer fit are
    /// merged three-way and left with conflict markers.
    pub fn apply_patch(&self, file: &Path) -> Result<String> {
        let file = file.to_string_lossy().into_owned();
        if let Ok(out) = self.git(&["apply", "--binary", &file]) {
            return Ok(out);
        }
        match self.git(&["apply", "--3way", "--binary", &file]) {
            Err(err) if format!("{err:#}").contains("with conflicts") => Ok(String::new()),
            result => result,
        }
    }

    /// Replace `path` in the work tree with its version in `rev`; a file
    /// `rev` doesn't have is deleted.
    pub fn get_from_revision(&self, rev: &str, path: &str) -> Result<String> {
        if self.git(&["cat-file", "-e", &format!("{rev}:{path}")]).is_ok() {
            self.git(&["checkout", rev, "--", path])
        } else {
            let out = self.git(&["rm", "-q", "-f", "--ignore-unmatch", "--", path])?;
            std::fs::remove_file(self.root.join(path)).ok();
            Ok(out)
        }
    }

    /// Commits from `from` up to HEAD, oldest first, for an interactive rebase.
    pub fn rebase_commits(&self, from: &str) -> Result<Vec<Commit>> {
        if self
            .git(&["merge-base", "--is-ancestor", from, "HEAD"])
            .is_err()
        {
            bail!("the commit is not on the current branch");
        }
        let is_root = self.git(&["rev-parse", "--verify", "-q", &format!("{from}^")]).is_err();
        let range = format!("{from}^..HEAD");
        let mut commits = if is_root {
            self.log_revs(&["HEAD"], 100_000)?
        } else {
            self.log_revs(&["--topo-order", &range], 100_000)?
        };
        if commits.iter().any(|c| c.parents.len() > 1) {
            bail!("there are merge commits between this commit and HEAD; editing history across merges is not supported");
        }
        commits.reverse();
        Ok(commits)
    }

    /// The oldest of `ids`, which must all be on the current branch with no
    /// merge commits between them and HEAD; history before it stays.
    fn oldest_of(&self, ids: &[String]) -> Result<String> {
        let mut args = vec!["merge-base", "--octopus"];
        args.extend(ids.iter().map(String::as_str));
        let base = self.git(&args).context("the commits are not on the current branch")?;
        let base = base.trim().to_string();
        if !ids.contains(&base) {
            bail!("the commits are not on one line of history");
        }
        Ok(base)
    }

    /// Where `rewrite` starts: the commit rebased from. Checks that the
    /// rewrite is possible, before anything changes.
    pub fn rewrite_base(&self, rewrite: &Rewrite) -> Result<String> {
        if self.repo_state() != RepoState::Clean {
            bail!("finish or abort the current merge, rebase or cherry-pick first");
        }
        let ids = rewrite.ids();
        if ids.is_empty() {
            bail!("no commits selected");
        }
        let oldest = self.oldest_of(&ids)?;
        let base = match rewrite {
            Rewrite::FixupIntoParent { id } => {
                let parent = self.git(&["rev-parse", "--verify", "-q", &format!("{id}^")]);
                parent.map(|p| p.trim().to_string()).context("the first commit has no parent to meld into")?
            }
            _ => oldest,
        };
        // Checks ancestry and merges in the range.
        self.rebase_commits(&base)?;
        Ok(base)
    }

    /// The upstream branch that already has `id`, if any: rewriting it then
    /// needs a force push.
    pub fn pushed_to(&self, id: &str) -> Option<String> {
        let upstream = self.git(&["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"]).ok()?;
        self.git(&["merge-base", "--is-ancestor", id, "@{u}"])
            .ok()
            .map(|_| upstream.trim().to_string())
    }

    /// Change the current branch's history; see [`Rewrite`].
    pub fn rewrite(&self, rewrite: &Rewrite) -> Result<String> {
        let base = self.rewrite_base(rewrite)?;
        if let Rewrite::Reword { id, message } = rewrite
            && self.head().as_deref() == Some(id.as_str())
        {
            // Only the message changes: no rebase, and staged changes stay staged.
            return self.git_with_input(&["commit", "--amend", "--only", "--allow-empty", "-q", "-F", "-"], message);
        }
        let commits = self.rebase_commits(&base)?;
        let plan: Vec<(String, RebaseAction)> = match rewrite {
            Rewrite::Reword { id, message } => commits
                .iter()
                .map(|c| {
                    let action = if &c.id == id { RebaseAction::Reword(message.clone()) } else { RebaseAction::Pick };
                    (c.id.clone(), action)
                })
                .collect(),
            Rewrite::Drop { ids } => {
                if commits.iter().all(|c| ids.contains(&c.id)) {
                    bail!("dropping every commit of the branch is not possible this way; delete the branch instead");
                }
                commits
                    .iter()
                    .map(|c| {
                        let action = if ids.contains(&c.id) { RebaseAction::Drop } else { RebaseAction::Pick };
                        (c.id.clone(), action)
                    })
                    .collect()
            }
            Rewrite::FixupIntoParent { id } => commits
                .iter()
                .map(|c| {
                    let action = if &c.id == id { RebaseAction::Fixup } else { RebaseAction::Pick };
                    (c.id.clone(), action)
                })
                .collect(),
            Rewrite::Squash { ids, message } => {
                // The oldest takes the new message; the others follow it as
                // fixups, keeping that message, and leave their old places.
                let mut plan = vec![(base.clone(), RebaseAction::Reword(message.clone()))];
                plan.extend(
                    commits
                        .iter()
                        .filter(|c| c.id != base && ids.contains(&c.id))
                        .map(|c| (c.id.clone(), RebaseAction::Fixup)),
                );
                plan.extend(
                    commits
                        .iter()
                        .filter(|c| !ids.contains(&c.id))
                        .map(|c| (c.id.clone(), RebaseAction::Pick)),
                );
                plan
            }
        };
        self.interactive_rebase(&base, &plan)
    }

    /// Undo the last commit, keeping its changes staged; returns its message
    /// for the next commit.
    pub fn undo_commit(&self) -> Result<String> {
        if self.repo_state() != RepoState::Clean {
            bail!("finish or abort the current merge, rebase or cherry-pick first");
        }
        let message = self.commit_message("HEAD")?;
        if self.git(&["rev-parse", "--verify", "-q", "HEAD^"]).is_ok() {
            self.git(&["reset", "--soft", "HEAD^"])?;
        } else {
            // The first commit: the branch goes back to having none.
            self.git(&["update-ref", "-d", "HEAD"])?;
        }
        Ok(message)
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
        let mut args = vec!["-c", &sequence_editor, "-c", "core.editor=true", "rebase", "-i", "--autostash"];
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

    /// The repository's working trees, the main one first.
    pub fn worktrees(&self) -> Result<Vec<Worktree>> {
        let current = self.root.canonicalize().unwrap_or_else(|_| self.root.clone());
        let mut trees: Vec<Worktree> = Vec::new();
        for block in self.git(&["worktree", "list", "--porcelain"])?.split("\n\n") {
            let mut tree: Option<Worktree> = None;
            for line in block.lines() {
                let (key, value) = line.split_once(' ').unwrap_or((line, ""));
                match (key, tree.as_mut()) {
                    ("worktree", _) => {
                        let path = PathBuf::from(value);
                        let is_current = path.canonicalize().unwrap_or_else(|_| path.clone()) == current;
                        tree = Some(Worktree {
                            path,
                            head: String::new(),
                            branch: None,
                            is_main: trees.is_empty(),
                            is_current,
                            locked: false,
                            prunable: false,
                        });
                    }
                    ("HEAD", Some(tree)) => tree.head = value.to_string(),
                    ("branch", Some(tree)) => tree.branch = Some(value.trim_start_matches("refs/heads/").to_string()),
                    ("locked", Some(tree)) => tree.locked = true,
                    ("prunable", Some(tree)) => tree.prunable = true,
                    _ => {}
                }
            }
            trees.extend(tree);
        }
        Ok(trees)
    }

    /// A new working tree at `path` with `branch` checked out: an existing
    /// local branch, a new one tracking the remote branch of that name, or
    /// else a new branch from HEAD.
    pub fn add_worktree(&self, path: &Path, branch: &str) -> Result<Repo> {
        let path_arg = path.to_string_lossy().into_owned();
        let exists = |rev: String| self.git(&["rev-parse", "--verify", "-q", &rev]).is_ok();
        let has_remote = self.git(&["for-each-ref", "--format=%(refname)", &format!("refs/remotes/*/{branch}")])
            .is_ok_and(|out| !out.trim().is_empty());
        if exists(format!("refs/heads/{branch}")) || has_remote {
            // git creates a tracking branch from a remote branch of that name.
            self.git(&["worktree", "add", &path_arg, branch])?;
        } else {
            self.git(&["worktree", "add", "-b", branch, &path_arg])?;
        }
        Repo::open(path)
    }

    /// Whether the working tree at `path` has local changes.
    pub fn worktree_is_dirty(path: &Path) -> bool {
        run(path, &["status", "--porcelain"]).is_ok_and(|out| !out.trim().is_empty())
    }

    /// Remove a working tree; with `force` even one with local changes.
    pub fn remove_worktree(&self, path: &Path, force: bool) -> Result<String> {
        let path = path.to_string_lossy().into_owned();
        let mut args = vec!["worktree", "remove"];
        if force {
            args.push("--force");
        }
        args.push(&path);
        self.git(&args)
    }

    /// Forget working trees whose folders are gone.
    pub fn prune_worktrees(&self) -> Result<String> {
        self.git(&["worktree", "prune"])
    }

    pub fn submodules(&self) -> Result<Vec<Submodule>> {
        Ok(self
            .git(&["submodule", "status"])?
            .lines()
            .filter_map(|line| {
                let mut chars = line.chars();
                let state = match chars.next()? {
                    '-' => SubmoduleState::Uninitialized,
                    '+' => SubmoduleState::OutOfDate,
                    'U' => SubmoduleState::Conflict,
                    _ => SubmoduleState::UpToDate,
                };
                let mut fields = line[1..].split_whitespace();
                let commit = fields.next()?.to_string();
                let path = fields.next()?.to_string();
                Some(Submodule { path, commit, state })
            })
            .collect())
    }

    /// Clone missing submodules and check all of them (or `paths`) out at
    /// their recorded commits, nested ones too.
    pub fn update_submodules(&self, paths: &[String]) -> Result<String> {
        let mut args = vec!["submodule", "update", "--init", "--recursive", "--"];
        args.extend(paths.iter().map(String::as_str));
        self.git(&args)
    }

    fn shelf_root(&self) -> Result<PathBuf> {
        Ok(self.git_dir()?.join("banyan/shelf"))
    }

    /// Shelved changes, newest first.
    pub fn shelves(&self) -> Vec<Shelf> {
        let Ok(root) = self.shelf_root() else { return Vec::new() };
        let Ok(entries) = std::fs::read_dir(root) else { return Vec::new() };
        let mut shelves: Vec<Shelf> = entries.flatten().filter_map(|entry| Shelf::read(&entry.path())).collect();
        shelves.sort_by(|a, b| b.time.cmp(&a.time).then_with(|| b.id.cmp(&a.id)));
        shelves
    }

    /// Put the changes of `paths` aside as shelf `name` and take them out of
    /// the work tree, new files included. `changelist` is where unshelving
    /// puts them back.
    pub fn shelve(&self, name: &str, changelist: &str, paths: &[String]) -> Result<Shelf> {
        if !self.has_head() {
            bail!("shelving compares with the last commit; commit something first");
        }
        if self.repo_state() != RepoState::Clean {
            bail!("finish or abort the current merge, rebase or cherry-pick first");
        }
        let entries: Vec<StatusEntry> = self.status()?.into_iter().filter(|e| paths.contains(&e.path)).collect();
        if entries.is_empty() {
            bail!("no changes to shelve");
        }
        if entries.iter().any(|e| e.is_conflicted()) {
            bail!("resolve the conflicts before shelving");
        }
        let untracked: Vec<&str> = entries.iter().filter(|e| e.is_untracked()).map(|e| e.path.as_str()).collect();
        let mut pathspec: Vec<&str> = entries.iter().map(|e| e.path.as_str()).collect();
        pathspec.extend(entries.iter().filter_map(|e| e.orig_path.as_deref()));
        let undo_intent = || {
            if !untracked.is_empty() {
                let mut args = vec!["rm", "-q", "--cached", "--ignore-unmatch", "--"];
                args.extend(&untracked);
                self.git(&args).ok();
            }
        };
        if !untracked.is_empty() {
            // Intent-to-add makes new files part of the diff.
            let mut args = vec!["add", "-N", "--"];
            args.extend(&untracked);
            self.git(&args)?;
        }
        let mut args = vec!["diff", "HEAD", "--binary", "--full-index", "-M", "--"];
        args.extend(&pathspec);
        let patch = match self.git(&args) {
            Ok(patch) if !patch.trim().is_empty() => patch,
            Ok(_) => {
                undo_intent();
                bail!("no changes to shelve");
            }
            Err(err) => {
                undo_intent();
                return Err(err);
            }
        };

        let time = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default();
        let shelf = Shelf {
            id: format!("{}-{:09}", time.as_secs(), time.subsec_nanos()),
            name: name.trim().to_string(),
            changelist: changelist.to_string(),
            time: time.as_secs() as i64,
            files: {
                let mut files: Vec<String> = entries.iter().map(|e| e.path.clone()).collect();
                files.sort();
                files
            },
        };
        let dir = self.shelf_root()?.join(&shelf.id);
        std::fs::create_dir_all(&dir)?;
        let patch_file = dir.join("patch.diff");
        std::fs::write(&patch_file, &patch)?;
        shelf.write(&dir)?;
        // Only touch the work tree once the patch is known to undo cleanly.
        let patch_arg = patch_file.to_string_lossy().into_owned();
        if let Err(err) = self.git(&["apply", "--check", "-R", "--binary", &patch_arg]) {
            std::fs::remove_dir_all(&dir).ok();
            undo_intent();
            return Err(err.context("the shelf patch does not undo cleanly; nothing was changed"));
        }

        let mut args = vec!["ls-tree", "-r", "-z", "--name-only", "HEAD", "--"];
        args.extend(&pathspec);
        let tree = self.git(&args)?;
        let in_head: Vec<&str> = tree.split('\0').filter(|p| !p.is_empty()).collect();
        let (restore, remove): (Vec<&str>, Vec<&str>) = pathspec.iter().partition(|p| in_head.contains(p));
        if !restore.is_empty() {
            let mut args = vec!["checkout", "HEAD", "--"];
            args.extend(&restore);
            self.git(&args)?;
        }
        if !remove.is_empty() {
            let mut args = vec!["rm", "-q", "--cached", "-f", "--ignore-unmatch", "--"];
            args.extend(&remove);
            self.git(&args)?;
            for path in remove {
                std::fs::remove_file(self.root.join(path)).ok();
            }
        }
        Ok(shelf)
    }

    /// Apply shelved changes again: all of shelf `id`, or only `paths` of it.
    /// Applied files leave the shelf and an emptied shelf goes. Returns the
    /// shelf as it was and the files restored. Hunks that no longer fit are
    /// merged three-way and left with conflict markers.
    pub fn unshelve(&self, id: &str, paths: Option<&[String]>) -> Result<(Shelf, Vec<String>)> {
        let dir = self.shelf_root()?.join(id);
        let shelf = Shelf::read(&dir).context("this shelf no longer exists")?;
        let files: Vec<String> = match paths {
            Some(paths) => shelf.files.iter().filter(|f| paths.contains(f)).cloned().collect(),
            None => shelf.files.clone(),
        };
        if files.is_empty() {
            bail!("nothing to unshelve");
        }
        let patch_file = dir.join("patch.diff");
        let patch = std::fs::read_to_string(&patch_file)?;
        let includes: Vec<String> = files.iter().map(|f| format!("--include={}", glob_literal(f))).collect();
        let patch_arg = patch_file.to_string_lossy().into_owned();
        let mut args = vec!["apply", "--binary"];
        args.extend(includes.iter().map(String::as_str));
        args.push(&patch_arg);
        if self.git(&args).is_err() {
            args.insert(1, "--3way");
            match self.git(&args) {
                // Applied, leaving conflicts to resolve.
                Err(err) if format!("{err:#}").contains("with conflicts") => {}
                result => {
                    result?;
                }
            }
        }
        // Files the patch adds show up as new files, not unversioned ones.
        let added: Vec<&str> = patch_blocks(&patch)
            .into_iter()
            .filter(|block| block.contains("\nnew file mode"))
            .filter_map(|block| files.iter().find(|f| block_is_for(block, f)))
            .map(String::as_str)
            .filter(|f| self.root.join(f).exists())
            .collect();
        if !added.is_empty() {
            let mut args = vec!["add", "-N", "--"];
            args.extend(&added);
            self.git(&args).ok();
        }

        let remaining: Vec<String> = shelf.files.iter().filter(|f| !files.contains(f)).cloned().collect();
        if remaining.is_empty() {
            std::fs::remove_dir_all(&dir)?;
        } else {
            let kept: String = patch_blocks(&patch)
                .into_iter()
                .filter(|block| remaining.iter().any(|f| block_is_for(block, f)))
                .collect();
            std::fs::write(&patch_file, kept)?;
            Shelf { files: remaining, ..shelf.clone() }.write(&dir)?;
        }
        Ok((shelf, files))
    }

    /// The part of shelf `id`'s patch for `path`.
    pub fn shelf_file_patch(&self, id: &str, path: &str) -> Result<String> {
        let patch = std::fs::read_to_string(self.shelf_root()?.join(id).join("patch.diff"))?;
        Ok(patch_blocks(&patch)
            .into_iter()
            .find(|block| block_is_for(block, path))
            .unwrap_or_default()
            .to_string())
    }

    pub fn rename_shelf(&self, id: &str, name: &str) -> Result<()> {
        let dir = self.shelf_root()?.join(id);
        let shelf = Shelf::read(&dir).context("this shelf no longer exists")?;
        Shelf { name: name.trim().to_string(), ..shelf }.write(&dir)
    }

    pub fn delete_shelf(&self, id: &str) -> Result<()> {
        std::fs::remove_dir_all(self.shelf_root()?.join(id)).context("cannot delete the shelf")
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

/// The version of the git at `program`, e.g. "git version 2.50.1".
pub fn git_version(program: &str) -> Result<String> {
    let output = Command::new(program)
        .arg("--version")
        .output()
        .with_context(|| format!("cannot run {program}"))?;
    if !output.status.success() {
        bail!("{program} --version failed");
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

fn command(dir: &Path, args: &[&str]) -> Command {
    let mut cmd = Command::new(crate::settings::Settings::read(|s| s.git_program().to_string()));
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
        .context("failed to run git; is it installed? (Settings › Git)")?;
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
                "banyan-test-{name}-{}-{:?}",
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

    /// `main` and `feature` both change the one line of a.txt.
    fn conflicting_branches(name: &str) -> TempRepo {
        let t = TempRepo::new(name);
        t.commit("a.txt", "start\nline\nend\n", "Base");
        t.repo.git(&["checkout", "-q", "-b", "feature"]).unwrap();
        t.commit("a.txt", "start\ntheirs\nend\n", "Theirs");
        t.repo.git(&["checkout", "-q", "main"]).unwrap();
        t.commit("a.txt", "start\nours\nend\n", "Ours");
        t
    }

    #[test]
    fn stopped_merge_is_described_and_merged_from_the_index() {
        let t = conflicting_branches("merge-conflict");
        assert!(t.repo.merge("feature").is_err());
        let operation = t.repo.operation().unwrap();
        assert_eq!(operation.state, RepoState::Merging);
        assert_eq!(operation.title, "Merging feature into main");
        assert_eq!((operation.ours.as_str(), operation.theirs.as_str()), ("main", "feature"));

        // Edits to the working file don't change what the merge tool starts from.
        t.write("a.txt", "garbage\n");
        let segments = crate::conflict::parse(&t.repo.merge_conflict_text("a.txt").unwrap());
        assert_eq!(
            segments,
            vec![
                crate::conflict::Segment::Clean(vec!["start\n".into()]),
                crate::conflict::Segment::Conflict(crate::conflict::Conflict {
                    ours: vec!["ours\n".into()],
                    base: vec!["line\n".into()],
                    theirs: vec!["theirs\n".into()],
                }),
                crate::conflict::Segment::Clean(vec!["end\n".into()]),
            ]
        );

        t.repo.resolve_conflict("a.txt", Some(false)).unwrap();
        assert!(!t.repo.status().unwrap().iter().any(|e| e.is_conflicted()));
        t.repo.continue_op(RepoState::Merging).unwrap();
        assert_eq!(t.repo.operation(), None);
        assert_eq!(std::fs::read_to_string(t.dir.join("a.txt")).unwrap(), "start\ntheirs\nend\n");
    }

    #[test]
    fn accepting_the_side_that_deleted_a_file_deletes_it() {
        let t = TempRepo::new("delete-conflict");
        t.commit("a.txt", "one\n", "Base");
        t.repo.git(&["checkout", "-q", "-b", "feature"]).unwrap();
        t.repo.git(&["rm", "-q", "a.txt"]).unwrap();
        t.repo.git(&["commit", "-q", "-m", "Delete"]).unwrap();
        t.repo.git(&["checkout", "-q", "main"]).unwrap();
        t.commit("a.txt", "two\n", "Change");
        assert!(t.repo.merge("feature").is_err());
        let entry = t.repo.status().unwrap().into_iter().find(|e| e.is_conflicted()).unwrap();
        assert_eq!(entry.conflict_sides(), Some((ConflictSide::Modified, ConflictSide::Deleted)));
        assert!(!entry.can_merge_text());
        assert!(t.repo.merge_conflict_text("a.txt").is_err());
        t.repo.resolve_conflict("a.txt", Some(false)).unwrap();
        assert!(!t.dir.join("a.txt").exists());
        assert!(t.repo.status().unwrap().iter().all(|e| !e.is_conflicted()));
    }

    #[test]
    fn aborting_a_stopped_merge_restores_the_branch() {
        let t = conflicting_branches("merge-abort");
        assert!(t.repo.merge("feature").is_err());
        t.repo.abort(RepoState::Merging).unwrap();
        assert_eq!(t.repo.operation(), None);
        assert_eq!(std::fs::read_to_string(t.dir.join("a.txt")).unwrap(), "start\nours\nend\n");
    }

    #[test]
    fn stopped_rebase_names_the_branches_and_progress() {
        let t = conflicting_branches("rebase-describe");
        assert!(t.repo.rebase("feature").is_err());
        let operation = t.repo.operation().unwrap();
        assert_eq!(operation.title, "Rebasing main onto feature (commit 1 of 1)");
        assert_eq!(operation.ours, "feature");
        assert!(operation.theirs.ends_with(" Ours"), "{}", operation.theirs);
    }

    /// Base, One, Two, Three on main, each adding a file.
    fn four_commits(name: &str) -> (TempRepo, Vec<String>) {
        let t = TempRepo::new(name);
        let ids = ["Base", "One", "Two", "Three"]
            .iter()
            .map(|subject| t.commit(&format!("{subject}.txt"), subject, subject))
            .collect();
        (t, ids)
    }

    #[test]
    fn reword_changes_only_the_message() {
        let (t, ids) = four_commits("reword");
        // HEAD, with a staged change that must stay out of the commit.
        t.write("staged.txt", "x");
        t.repo.git(&["add", "staged.txt"]).unwrap();
        t.repo.rewrite(&Rewrite::Reword { id: ids[3].clone(), message: "Three, reworded".into() }).unwrap();
        assert_eq!(t.subjects()[0], "Three, reworded");
        assert!(t.repo.status().unwrap().iter().any(|e| e.path == "staged.txt"));
        // An older commit, through a rebase.
        t.repo.rewrite(&Rewrite::Reword { id: ids[1].clone(), message: "One, reworded\n\nBody".into() }).unwrap();
        assert_eq!(t.subjects(), vec!["Three, reworded", "Two", "One, reworded", "Base"]);
        assert!(t.repo.status().unwrap().iter().any(|e| e.path == "staged.txt"));
    }

    #[test]
    fn squash_melds_commits_into_the_oldest() {
        let (t, ids) = four_commits("squash");
        // Not next to each other: Three moves down onto One.
        t.repo
            .rewrite(&Rewrite::Squash { ids: vec![ids[3].clone(), ids[1].clone()], message: "One and Three".into() })
            .unwrap();
        assert_eq!(t.subjects(), vec!["Two", "One and Three", "Base"]);
        let files = t.repo.git(&["show", "--name-only", "--format=", "HEAD~1"]).unwrap();
        assert_eq!(files.lines().collect::<Vec<_>>(), vec!["One.txt", "Three.txt"]);
    }

    #[test]
    fn fixup_into_parent_keeps_the_parent_message() {
        let (t, ids) = four_commits("fixup");
        t.repo.rewrite(&Rewrite::FixupIntoParent { id: ids[2].clone() }).unwrap();
        assert_eq!(t.subjects(), vec!["Three", "One", "Base"]);
        assert!(t.dir.join("Two.txt").exists());
        assert!(t.repo.rewrite_base(&Rewrite::FixupIntoParent { id: ids[0].clone() }).is_err());
    }

    #[test]
    fn drop_removes_commits_and_keeps_local_changes() {
        let (t, ids) = four_commits("drop");
        t.write("Base.txt", "edited");
        t.repo.rewrite(&Rewrite::Drop { ids: vec![ids[1].clone(), ids[3].clone()] }).unwrap();
        assert_eq!(t.subjects(), vec!["Two", "Base"]);
        assert!(!t.dir.join("One.txt").exists());
        assert_eq!(std::fs::read_to_string(t.dir.join("Base.txt")).unwrap(), "edited");
    }

    #[test]
    fn rewrites_stay_on_the_current_branch() {
        let (t, ids) = four_commits("rewrite-branch");
        t.repo.git(&["checkout", "-q", "-b", "side", &ids[1]]).unwrap();
        let side = t.commit("side.txt", "side", "Side");
        t.repo.git(&["checkout", "-q", "main"]).unwrap();
        assert!(t.repo.rewrite_base(&Rewrite::Drop { ids: vec![side] }).is_err());
        assert!(t.repo.rewrite_base(&Rewrite::Drop { ids: ids.clone() }).is_ok());
        assert!(t.repo.rewrite(&Rewrite::Drop { ids }).is_err());
    }

    #[test]
    fn undo_commit_keeps_the_changes_and_returns_the_message() {
        let (t, _) = four_commits("undo");
        assert_eq!(t.repo.undo_commit().unwrap(), "Three");
        assert_eq!(t.subjects(), vec!["Two", "One", "Base"]);
        let staged = t.repo.status().unwrap();
        assert!(staged.iter().any(|e| e.path == "Three.txt" && e.index == 'A'));
    }

    #[test]
    fn pushed_commits_are_reported() {
        let (t, ids) = four_commits("pushed");
        let remote = TempRepo::new("pushed-remote");
        run(&remote.dir, &["config", "receive.denyCurrentBranch", "ignore"]).unwrap();
        t.repo.git(&["remote", "add", "origin", &remote.dir.display().to_string()]).unwrap();
        t.repo.git(&["push", "-q", "origin", &format!("{}:refs/heads/main", ids[1])]).unwrap();
        t.repo.git(&["fetch", "-q", "origin"]).unwrap();
        t.repo.git(&["branch", "-q", "--set-upstream-to=origin/main"]).unwrap();
        assert_eq!(t.repo.pushed_to(&ids[1]).as_deref(), Some("origin/main"));
        assert_eq!(t.repo.pushed_to(&ids[2]), None);
    }

    #[test]
    fn compares_two_revisions() {
        let (t, ids) = four_commits("compare-revisions");
        let files = t.repo.changes_between(&ids[1], Some(&ids[3])).unwrap();
        let paths: Vec<&str> = files.iter().map(|f| f.path.as_str()).collect();
        assert_eq!(paths, vec!["Three.txt", "Two.txt"]);
        let patch = t.repo.diff_between(&ids[1], Some(&ids[3]), &files[1]).unwrap();
        assert!(patch.contains("+Two"), "{patch}");
    }

    #[test]
    fn shelving_takes_changes_out_and_unshelving_brings_them_back() {
        let t = TempRepo::new("shelve");
        t.commit("keep.txt", "keep\n", "Base");
        t.commit("gone.txt", "gone\n", "Second");
        t.write("keep.txt", "keep\nmore\n");
        std::fs::remove_file(t.dir.join("gone.txt")).unwrap();
        t.write("new dir/new.txt", "new\n");
        t.write("other.txt", "not shelved\n");
        let paths = vec!["gone.txt".to_string(), "keep.txt".to_string(), "new dir/new.txt".to_string()];

        let shelf = t.repo.shelve("Work in progress", "Feature", &paths).unwrap();
        assert_eq!(shelf.files, paths);
        let status: Vec<String> = t.repo.status().unwrap().into_iter().map(|e| e.path).collect();
        assert_eq!(status, vec!["other.txt"]);
        assert_eq!(std::fs::read_to_string(t.dir.join("keep.txt")).unwrap(), "keep\n");
        assert!(t.dir.join("gone.txt").exists());
        assert!(!t.dir.join("new dir/new.txt").exists());
        assert_eq!(t.repo.shelves(), vec![shelf.clone()]);
        assert!(t.repo.shelf_file_patch(&shelf.id, "keep.txt").unwrap().contains("+more"));

        // One file first; the rest stays shelved.
        let (_, restored) = t.repo.unshelve(&shelf.id, Some(&["new dir/new.txt".to_string()])).unwrap();
        assert_eq!(restored, vec!["new dir/new.txt"]);
        assert_eq!(std::fs::read_to_string(t.dir.join("new dir/new.txt")).unwrap(), "new\n");
        let entry = t.repo.status().unwrap().into_iter().find(|e| e.path == "new dir/new.txt").unwrap();
        assert!(!entry.is_untracked());
        assert_eq!(t.repo.shelves()[0].files, vec!["gone.txt", "keep.txt"]);

        let (back, _) = t.repo.unshelve(&shelf.id, None).unwrap();
        assert_eq!(back.changelist, "Feature");
        assert!(t.repo.shelves().is_empty());
        assert_eq!(std::fs::read_to_string(t.dir.join("keep.txt")).unwrap(), "keep\nmore\n");
        assert!(!t.dir.join("gone.txt").exists());
    }

    #[test]
    fn unshelving_onto_changed_files_merges_three_way() {
        let t = TempRepo::new("unshelve-conflict");
        t.commit("a.txt", "one\n", "Base");
        t.write("a.txt", "shelved\n");
        let shelf = t.repo.shelve("Mine", "Changes", &["a.txt".to_string()]).unwrap();
        t.commit("a.txt", "committed meanwhile\n", "Other");
        t.repo.unshelve(&shelf.id, None).unwrap();
        let text = std::fs::read_to_string(t.dir.join("a.txt")).unwrap();
        assert!(text.contains("shelved") && text.contains("committed meanwhile"), "{text}");
        assert!(t.repo.status().unwrap().iter().any(|e| e.is_conflicted()));
    }

    #[test]
    fn patches_round_trip_and_files_come_back_from_revisions() {
        let (t, ids) = four_commits("patches");
        let patch = t.dir.join("../banyan-test-patch-{}.patch".replace("{}", &std::process::id().to_string()));
        t.repo.create_patch(&ids[2..], &patch).unwrap();
        let text = std::fs::read_to_string(&patch).unwrap();
        assert!(text.contains("Subject: [PATCH] Two") && text.contains("Subject: [PATCH] Three"), "{text}");

        t.repo.reset(&ids[1], ResetMode::Hard).unwrap();
        t.repo.apply_patch(&patch).unwrap();
        assert!(t.dir.join("Two.txt").exists() && t.dir.join("Three.txt").exists());
        std::fs::remove_file(&patch).ok();

        t.write("One.txt", "changed");
        t.repo.get_from_revision(&ids[1], "One.txt").unwrap();
        assert_eq!(std::fs::read_to_string(t.dir.join("One.txt")).unwrap(), "One");
        t.repo.get_from_revision(&ids[0], "One.txt").unwrap();
        assert!(!t.dir.join("One.txt").exists());
    }

    #[test]
    fn other_branches_push_update_and_track() {
        let t = TempRepo::new("branch-ops");
        let remote = TempRepo::new("branch-ops-remote");
        run(&remote.dir, &["config", "receive.denyCurrentBranch", "ignore"]).unwrap();
        t.repo.git(&["remote", "add", "origin", &remote.dir.display().to_string()]).unwrap();
        t.commit("a.txt", "a\n", "First");
        t.repo.git(&["branch", "side"]).unwrap();
        t.repo.push_branch("side").unwrap();
        assert_eq!(t.repo.push_target_for("side").unwrap().is_new, false);

        // Someone else moves the remote side branch on.
        let other = TempRepo::new("branch-ops-other");
        other.repo.git(&["fetch", "-q", &remote.dir.display().to_string(), "side:side"]).unwrap();
        other.repo.git(&["checkout", "-q", "side"]).unwrap();
        other.commit("b.txt", "b\n", "On side");
        other.repo.git(&["push", "-q", &remote.dir.display().to_string(), "side:side"]).unwrap();

        t.repo.update_branch("side").unwrap();
        assert_eq!(t.repo.git(&["log", "-1", "--format=%s", "side"]).unwrap().trim(), "On side");
        assert!(t.repo.update_branch("main").is_err());

        t.repo.set_upstream("side", None).unwrap();
        assert!(t.repo.push_target_for("side").unwrap().is_new);
        t.repo.set_upstream("side", Some("origin/side")).unwrap();
        assert!(!t.repo.push_target_for("side").unwrap().is_new);
    }

    #[test]
    fn worktrees_are_listed_added_and_removed() {
        let t = TempRepo::new("worktrees");
        t.commit("a.txt", "a\n", "First");
        t.repo.git(&["branch", "existing"]).unwrap();
        let new_path = t.dir.with_extension("new");
        let existing_path = t.dir.with_extension("existing");
        let added = t.repo.add_worktree(&new_path, "fresh").unwrap();
        assert_eq!(added.current_branch().as_deref(), Some("fresh"));
        t.repo.add_worktree(&existing_path, "existing").unwrap();

        let trees = t.repo.worktrees().unwrap();
        assert_eq!(trees.len(), 3);
        assert!(trees[0].is_main && trees[0].is_current);
        assert_eq!(trees[0].branch.as_deref(), Some("main"));
        let fresh = trees.iter().find(|w| w.branch.as_deref() == Some("fresh")).unwrap();
        assert!(!fresh.is_current && !fresh.is_main);
        // The other tree sees itself as current.
        let seen = added.worktrees().unwrap();
        assert!(seen.iter().find(|w| w.branch.as_deref() == Some("fresh")).unwrap().is_current);

        std::fs::write(new_path.join("dirty.txt"), "x").unwrap();
        assert!(Repo::worktree_is_dirty(&new_path));
        assert!(t.repo.remove_worktree(&new_path, false).is_err());
        t.repo.remove_worktree(&new_path, true).unwrap();
        std::fs::remove_dir_all(&existing_path).unwrap();
        assert!(t.repo.worktrees().unwrap().iter().any(|w| w.prunable));
        t.repo.prune_worktrees().unwrap();
        assert_eq!(t.repo.worktrees().unwrap().len(), 1);
    }

    #[test]
    fn submodules_report_their_state_and_update() {
        let lib = TempRepo::new("submodule-lib");
        lib.commit("lib.txt", "v1\n", "Lib");
        let t = TempRepo::new("submodule-host");
        t.commit("a.txt", "a\n", "First");
        t.repo
            .git(&["-c", "protocol.file.allow=always", "submodule", "add", "-q", &lib.dir.display().to_string(), "lib"])
            .unwrap();
        t.repo.git(&["commit", "-q", "-m", "Add lib"]).unwrap();
        let subs = t.repo.submodules().unwrap();
        assert_eq!((subs[0].path.as_str(), subs[0].state), ("lib", SubmoduleState::UpToDate));

        // A fresh clone has it uninitialized until updated.
        let clone_dir = t.dir.with_extension("clone");
        run(t.dir.parent().unwrap(), &["clone", "-q", &t.dir.display().to_string(), &clone_dir.display().to_string()]).unwrap();
        let clone = Repo::open(&clone_dir).unwrap();
        assert_eq!(clone.submodules().unwrap()[0].state, SubmoduleState::Uninitialized);
        // Recent git refuses local file submodules unless told otherwise.
        clone.git(&["-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive"]).unwrap();
        assert_eq!(clone.submodules().unwrap()[0].state, SubmoduleState::UpToDate);
        assert!(clone_dir.join("lib/lib.txt").exists());
    }

    #[test]
    fn init_makes_a_folder_a_repository() {
        let dir = std::env::temp_dir().join(format!("banyan-test-init-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("sub")).unwrap();
        let repo = Repo::init(&dir).unwrap();
        assert_eq!(repo.root().canonicalize().unwrap(), dir.canonicalize().unwrap());
        // Opening a folder inside it finds the repository.
        assert_eq!(Repo::open(&dir.join("sub")).unwrap().root(), repo.root());
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn merge_messages_name_the_merged_branch() {
        assert_eq!(merged_name("Merge branch 'feature'\n\n# Conflicts:"), Some("feature".into()));
        assert_eq!(merged_name("Merge remote-tracking branch 'origin/x' into main"), Some("origin/x".into()));
        assert_eq!(merged_name("Squashed commit"), None);
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

        t.repo.push_to(&target, false, false).unwrap();
        let target = t.repo.push_target().unwrap();
        assert!(!target.is_new);
        assert!(t.repo.outgoing(&target).unwrap().is_empty());

        t.repo.create_tag("v1", "HEAD").unwrap();
        t.repo.push_to(&target, false, true).unwrap();
        assert!(run(&remote.dir, &["rev-parse", "--verify", "refs/tags/v1"]).is_ok());

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

    #[test]
    fn commit_partial_keeps_excluded_hunks_in_the_work_tree() {
        let t = TempRepo::new("partial");
        let original: String = (1..=20).map(|n| format!("line {n}\n")).collect();
        t.commit("f.txt", &original, "Base");
        t.commit("other.txt", "x\n", "Other");
        let changed = original
            .replace("line 2\n", "line two\n")
            .replace("line 18\n", "line eighteen\n");
        t.write("f.txt", &changed);
        t.write("other.txt", "y\n");

        let diff = t.repo.git(&["diff", "HEAD", "--", "f.txt"]).unwrap();
        let hunks = crate::patch::parse(&diff).hunks;
        assert_eq!(hunks.len(), 2);

        // Commit other.txt whole and only the second hunk of f.txt.
        t.repo
            .commit_partial("Partial", &["other.txt".into()], &[("f.txt".into(), excluded_hunk(&hunks[0]))], false)
            .unwrap();
        let committed = t.repo.git(&["show", "HEAD:f.txt"]).unwrap();
        assert!(committed.contains("line 2\n") && committed.contains("line eighteen\n"));
        assert_eq!(t.repo.git(&["show", "HEAD:other.txt"]).unwrap(), "y\n");
        assert_eq!(std::fs::read_to_string(t.dir.join("f.txt")).unwrap(), changed);
        let status: Vec<_> = t.repo.status().unwrap().into_iter().map(|e| e.path).collect();
        assert_eq!(status, vec!["f.txt"]);
    }

    fn excluded_hunk(hunk: &crate::patch::Hunk) -> Vec<crate::patch::ChangedLine> {
        hunk.changes().map(|ix| (hunk.header.clone(), ix)).collect()
    }

    #[test]
    fn commit_partial_commits_single_lines() {
        let t = TempRepo::new("partial-lines");
        t.commit("f.txt", "a\nb\nc\n", "Base");
        let changed = "a\nB\nc\nd\n";
        t.write("f.txt", changed);

        let diff = t.repo.git(&["diff", "HEAD", "--", "f.txt"]).unwrap();
        let hunk = crate::patch::parse(&diff).hunks.remove(0);
        // -b +B +d: leave out the removal of b and the addition of d.
        let excluded: Vec<_> = hunk
            .changes()
            .filter(|&ix| hunk.lines[ix] != "+B")
            .map(|ix| (hunk.header.clone(), ix))
            .collect();
        t.repo.commit_partial("Add B", &[], &[("f.txt".into(), excluded)], false).unwrap();
        assert_eq!(t.repo.git(&["show", "HEAD:f.txt"]).unwrap(), "a\nb\nB\nc\n");
        assert_eq!(std::fs::read_to_string(t.dir.join("f.txt")).unwrap(), changed);
    }

    #[test]
    fn cherry_picks_and_reverts_several_commits() {
        let t = TempRepo::new("multi-pick");
        t.commit("base.txt", "base\n", "Base");
        t.repo.git(&["checkout", "-q", "-b", "topic"]).unwrap();
        let one = t.commit("one.txt", "1\n", "One");
        let two = t.commit("two.txt", "2\n", "Two");
        t.repo.git(&["checkout", "-q", "main"]).unwrap();

        t.repo.cherry_pick(&[one, two]).unwrap();
        let subjects = t.repo.git(&["log", "--format=%s"]).unwrap();
        assert_eq!(subjects, "Two\nOne\nBase\n");

        let head = t.repo.git(&["rev-parse", "HEAD", "HEAD~1"]).unwrap();
        let ids: Vec<String> = head.lines().map(str::to_string).collect();
        t.repo.revert(&ids).unwrap();
        assert!(!t.dir.join("one.txt").exists() && !t.dir.join("two.txt").exists());
    }

    #[test]
    fn log_filters_by_path_and_date() {
        let t = TempRepo::new("log-filters");
        t.commit("a.txt", "a\n", "Add a");
        t.commit("dir/b.txt", "b\n", "Add b");
        t.commit("a.txt", "a2\n", "Change a");
        let subjects = |commits: Vec<Commit>| commits.into_iter().map(|c| c.subject).collect::<Vec<_>>();
        assert_eq!(subjects(t.repo.log(None, None, Some("a.txt"), 10).unwrap()), vec!["Change a", "Add a"]);
        assert_eq!(subjects(t.repo.log(None, None, Some("dir"), 10).unwrap()), vec!["Add b"]);
        // An old commit on top must not hide the newer ones below it.
        let old = std::process::Command::new("git")
            .current_dir(&t.dir)
            .env("GIT_COMMITTER_DATE", "2020-01-01T00:00:00")
            .args(["commit", "-q", "--allow-empty", "-m", "Old"])
            .status()
            .unwrap();
        assert!(old.success());
        assert_eq!(t.repo.log(None, Some("1.day.ago"), None, 10).unwrap().len(), 3);
        assert!(t.repo.log(None, Some("2099-01-01"), None, 10).unwrap().is_empty());
    }

    #[test]
    fn adds_untracked_files_to_gitignore() {
        let t = TempRepo::new("gitignore");
        t.commit(".gitignore", "*.log", "Ignore logs");
        t.write("build/out.bin", "x");
        t.write("notes.txt", "x");
        t.repo.add_to_gitignore("build/").unwrap();
        t.repo.add_to_gitignore("notes.txt").unwrap();
        assert_eq!(std::fs::read_to_string(t.dir.join(".gitignore")).unwrap(), "*.log\n/build\n/notes.txt\n");
        let status: Vec<_> = t.repo.status().unwrap().into_iter().map(|e| e.path).collect();
        assert_eq!(status, vec![".gitignore"]);
    }

    #[test]
    fn smart_checkout_carries_local_changes() {
        let t = TempRepo::new("smart-checkout");
        let lines: String = (1..=10).map(|n| format!("{n}\n")).collect();
        t.commit("a.txt", &lines, "Base");
        t.repo.git(&["checkout", "-q", "-b", "topic"]).unwrap();
        t.commit("a.txt", &lines.replacen("1\n", "one\n", 1), "Topic edits line 1");
        t.repo.git(&["checkout", "-q", "main"]).unwrap();
        // A local edit of line 10 blocks a plain checkout.
        let local = lines.replace("10\n", "ten\n");
        t.write("a.txt", &local);

        let err = t.repo.checkout_any("topic", false).unwrap_err();
        assert!(is_blocked_by_local_changes(&err), "{err:#}");
        assert_eq!(blocked_files(&format!("{err:#}")), vec!["a.txt"]);

        t.repo.smart_checkout("topic", false).unwrap();
        assert_eq!(t.repo.current_branch().as_deref(), Some("topic"));
        let content = std::fs::read_to_string(t.dir.join("a.txt")).unwrap();
        assert!(content.starts_with("one\n") && content.ends_with("ten\n"), "{content}");
        assert!(t.repo.stashes().unwrap().is_empty());
    }

    #[test]
    fn force_checkout_discards_local_changes() {
        let t = TempRepo::new("force-checkout");
        t.commit("a.txt", "base\n", "Base");
        t.repo.git(&["checkout", "-q", "-b", "topic"]).unwrap();
        t.commit("a.txt", "topic\n", "Topic");
        t.repo.git(&["checkout", "-q", "main"]).unwrap();
        t.write("a.txt", "local\n");
        t.repo.force_checkout("topic", false).unwrap();
        assert_eq!(std::fs::read_to_string(t.dir.join("a.txt")).unwrap(), "topic\n");
    }

    #[test]
    fn update_project_rebases_onto_incoming_and_keeps_local_changes() {
        let origin = TempRepo::new("update-origin");
        origin.commit("a.txt", "a\n", "Base");
        run(&origin.dir, &["config", "receive.denyCurrentBranch", "ignore"]).unwrap();
        let local = TempRepo::new("update-local");
        local.repo.git(&["remote", "add", "origin", &origin.dir.display().to_string()]).unwrap();
        local.repo.git(&["fetch", "-q", "origin"]).unwrap();
        local.repo.git(&["reset", "-q", "--hard", "origin/main"]).unwrap();
        local.repo.git(&["branch", "-q", "--set-upstream-to=origin/main"]).unwrap();

        origin.commit("b.txt", "b\n", "Incoming");
        local.commit("c.txt", "c\n", "Local commit");
        local.write("a.txt", "uncommitted\n");

        local.repo.update_project(true).unwrap();
        assert_eq!(local.subjects(), vec!["Local commit", "Incoming", "Base"]);
        assert!(local.dir.join("b.txt").exists());
        assert_eq!(std::fs::read_to_string(local.dir.join("a.txt")).unwrap(), "uncommitted\n");
    }

    #[test]
    fn update_project_needs_an_upstream() {
        let t = TempRepo::new("update-no-upstream");
        t.commit("a.txt", "a\n", "Base");
        let err = t.repo.update_project(false).unwrap_err();
        assert!(format!("{err:#}").contains("no tracked remote branch"));
    }

    #[test]
    fn diff_with_worktree_since_a_revision() {
        let t = TempRepo::new("worktree-diff");
        let base = t.commit("a.txt", "a\n", "Base");
        t.commit("b.txt", "b\n", "Add b");
        t.write("a.txt", "changed\n");
        let files = t.repo.changes_between(&base, None).unwrap();
        let summary: Vec<_> = files.iter().map(|f| (f.status, f.path.as_str())).collect();
        assert_eq!(summary, vec![('M', "a.txt"), ('A', "b.txt")]);
        let patch = t.repo.diff_between(&base, None, &files[0]).unwrap();
        assert!(patch.contains("+changed"));
    }
}
