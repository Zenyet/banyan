<div align="center">

<img src="docs/icon.png" width="128" alt="Banyan icon">

# Banyan

**A fast, native Git client for macOS, in the spirit of JetBrains' Git tooling.**

Built in Rust with [GPUI](https://gpui.rs) and [GPUI Kit](https://gpui-kit.com).

![macOS 11+](https://img.shields.io/badge/macOS-11%2B-black?logo=apple)
![Rust](https://img.shields.io/badge/Rust-2024_edition-orange?logo=rust)
![GPUI](https://img.shields.io/badge/UI-GPUI-blue)

</div>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/log-dark.png">
  <img src="docs/screenshots/log.png" alt="Banyan's Log tab: branches tree, commit graph, changed files and a side-by-side diff">
</picture>

## About

Banyan brings the Git workflow of JetBrains IDEs (the Log, the Commit tab, the merge tool, interactive rebase)
to a standalone app that opens instantly and does not need an IDE or a project.
It drives the `git` you already have installed, so it works with your existing config, hooks and credentials.

The name comes from the banyan tree, whose branches drop roots that grow into new trunks,
the way git branches grow apart and merge back.

## Features

### Log

- Branches tree (HEAD / Local / Remote / Tags) next to a commit graph with colored lanes and ref labels.
- Filter by text or hash, branch, author, date, and file or folder.
- Commit details ("in N branches"), a changed-files tree, and a side-by-side or unified diff
  with syntax highlighting.
- Commit context menu: copy hash, checkout, new branch / tag, cherry-pick, revert,
  reset (soft / mixed / hard), and "Show Diff with Working Tree".
  Cmd-click or Shift-click selects several commits to cherry-pick or revert together.
- Loads 5000 commits at a time (configurable), and more when you scroll to the end.

### Commit

![Banyan's Commit tab: changelist checkboxes, per-hunk selection and the commit message box](docs/screenshots/commit.png)

- Changelist-style checkboxes for Changes, Unversioned Files and Merge Conflicts.
- Commit whole files, single hunks, or individual lines.
- Commit, amend, commit and push, rollback, and stash. Recent commit messages are remembered.
- Add to `.gitignore`, Copy Path, and Reveal in Finder for local changes.

### Branches and history

- Branch popup in the toolbar: checkout (with Smart Checkout when local changes are in the way),
  new branch from, merge into current, rebase current onto, rename, and delete local, remote and tag refs.
- **Interactive rebase** from any commit: reorder, pick, reword, squash, fixup, drop.
- **Conflict handling**: when a merge, rebase, cherry-pick, revert or pull stops on conflicts, a **Conflicts**
  dialog lists the files with what each side did, offering Accept Yours / Accept Theirs / Merge…
  A banner above the tabs stays until the operation is finished, with **Resolve Conflicts…**, then
  **Commit Merge** / **Continue Rebase**, and **Abort** at any time, also for operations started outside Banyan.
- **Merge tool**: yours, the result and theirs side by side. Changes that merge cleanly are applied already;
  each conflict takes yours (»), theirs («), both, or text you type, and shows the common ancestor until resolved.
  Files deleted on one side are resolved by accepting a side.
- **File history** and **Annotate** (git blame shaded by age).
- **Compare with Current** for any branch, and a **stash viewer** showing each stash's files and diff.

### Remotes

- Fetch, Update Project (merge or rebase), and a **Push dialog** that lists outgoing commits and their files,
  shows the target branch, and offers push tags and force push with lease.
- **Clone Repository…** and **Manage Remotes…**.
- **Git Authentication** dialog for HTTPS credentials and SSH passphrases
  (Banyan acts as `GIT_ASKPASS` / `SSH_ASKPASS`). Optional background fetch.

### And more

- A **Git console** tab showing every git command your actions run, with output and timing.
- Refreshes automatically when files, the index or refs change. Git-ignored files are skipped.
- Light and dark themes, or follow macOS.

## Installation

Banyan is not distributed as a prebuilt binary yet. To build it from source you need:

- macOS 11 or later, with the Xcode command line tools (`xcode-select --install`)
- [Rust](https://rustup.rs) 1.85 or later
- `git`

```sh
git clone https://github.com/Zenyet/banyan.git
cd banyan
scripts/bundle.sh            # builds target/release/Banyan.app
cp -r target/release/Banyan.app /Applications/
```

The app is about 27 MB. Diffs are highlighted for common languages by default. To also highlight Kotlin, Swift,
C#, Scala, SQL, PHP, Ruby and Elixir (about 25 MB more), build with
`cargo build --release --features all-languages`.

## Usage

```sh
cargo run -- /path/to/repo   # open a repository
cargo run                    # open the repository you're in
```

Launched from Finder, Banyan reopens the last repository you used. If there isn't one, a welcome window
offers **Open Repository…** and **Clone Repository…**. Recent repositories are listed in the repository
menu in the toolbar.

### Keyboard shortcuts

| Shortcut | Action |
| --- | --- |
| <kbd>⌘</kbd> <kbd>K</kbd> | Commit (<kbd>⌘</kbd> <kbd>↩</kbd> in the message box) |
| <kbd>⇧</kbd> <kbd>⌘</kbd> <kbd>K</kbd> | Push |
| <kbd>⌘</kbd> <kbd>T</kbd> | Update Project |
| <kbd>⌘</kbd> <kbd>9</kbd> / <kbd>⌘</kbd> <kbd>0</kbd> | Log / Commit tab |
| <kbd>⌘</kbd> <kbd>R</kbd> | Refresh |
| <kbd>⌘</kbd> <kbd>,</kbd> | Settings |
| <kbd>Home</kbd> <kbd>End</kbd> <kbd>PgUp</kbd> <kbd>PgDn</kbd> | Move through the commit list |

### Settings

Under <kbd>⌘</kbd> <kbd>,</kbd> you can set the theme (light, dark, or follow macOS), font size,
relative or absolute dates, the git executable, the default Update Project method, background fetch,
side-by-side or unified diffs, and how many commits the log loads at a time.

Settings, recent repositories and recent commit messages are stored in
`~/Library/Application Support/Banyan/settings`. Settings from GitBrains, Banyan's earlier name,
are carried over on first launch.

## Development

```sh
cargo run -- .     # run a debug build on this repository
cargo test         # graph layout, ref parsing, and git operations against throwaway repositories
```

### Project layout

| File | Purpose |
| --- | --- |
| `src/git.rs` | `git` CLI backend (log, status, diff, branch ops…) |
| `src/graph.rs` | Commit graph lane layout |
| `src/model.rs` | Shared repo state; runs ops in the background and broadcasts refreshes |
| `src/workspace.rs` | Main window, toolbar, tabs |
| `src/log_view.rs` | Log tab |
| `src/changes_view.rs` | Commit tab |
| `src/diff_pane.rs` | Diff preview |
| `src/merge_tool.rs` | Conflict resolution dialog |
| `src/history.rs` | File history and annotate dialogs |
| `src/push_dialog.rs` | Push dialog |
| `src/rebase_dialog.rs` | Interactive rebase editor |
| `src/commit_browser.rs` | Commit list + files + diff, reused by compare and stashes |
| `src/compare.rs` / `src/stash_viewer.rs` | Branch comparison and stash dialogs |
| `src/console.rs` | Git console |
| `src/askpass.rs` | Credential prompts for git and ssh |
| `src/clone_dialog.rs` / `src/remotes_dialog.rs` | Clone and remote management |
| `src/watcher.rs` | File system watcher for auto-refresh |
| `src/settings.rs` | Persisted settings, recent repositories and commit messages |
| `src/settings_window.rs` | Settings window |
| `src/actions.rs` | Branch / commit context menus |
| `src/assets.rs` | Icons: GPUI Kit's default bundle plus `assets/icons` |
| `scripts/bundle.sh` | Builds `Banyan.app` (icon, `Info.plist`, ad-hoc signature) |
| `scripts/make_icon.swift` | Draws the app icon |

## Acknowledgements

- [GPUI](https://gpui.rs), the GPU-accelerated UI framework from the [Zed](https://zed.dev) editor
- [GPUI Kit](https://gpui-kit.com) for components and syntax highlighting
- [Lucide](https://lucide.dev) icons (ISC license)
- The Git integration in JetBrains IDEs, which inspired the workflow
