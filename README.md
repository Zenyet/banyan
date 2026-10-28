# GitBrains

A JetBrains-style Git desktop client built with [GPUI](https://gpui.rs) and [GPUI Kit](https://gpui-kit.com).

```sh
cargo run -- /path/to/repo   # or run inside a repository; with no repository a welcome window opens
```

## Features

- **Log tab**: branches tree (HEAD / Local / Remote / Tags), commit graph with colored lanes and ref labels,
  text/hash, branch and author filters, commit details (message, author, "in N branches"), changed files tree,
  and a side-by-side / unified diff preview.
- **Commit context menu**: copy hash, checkout revision, new branch / tag, cherry-pick, revert, reset (soft / mixed / hard).
- **Branch popup** (toolbar) and branches-tree context menu: checkout, new branch from, merge into current,
  rebase current onto, rename, delete (local, remote, tag).
- **Commit tab**: changelist-style checkboxes (Changes / Unversioned Files / Merge Conflicts), commit, amend,
  commit and push, rollback, stash; accept yours / theirs for conflicts; continue / abort merge, rebase, cherry-pick.
- **Merge tool**: resolve conflicts per hunk (accept yours / theirs / both), apply and mark resolved;
  the merge message is pre-filled from `MERGE_MSG`.
- **Push dialog**: review outgoing commits and their files, see the target branch (new or tracked), optional force push with lease.
- **Interactive rebase** from any commit: reorder, pick / reword / squash / fixup / drop; conflicts stop the rebase
  and show up in the Commit tab with Continue / Abort.
- **File history** and **Annotate** (git blame with age shading), from the changed-files tree or the Commit tab.
- **Compare with Current** for any branch, and a **stash viewer** with each stash's files and diff.
- **Git console** tab: every git command run by your actions, with output and timing.
- **Git Authentication** dialog for HTTPS credentials and ssh passphrases (GitBrains acts as `GIT_ASKPASS` / `SSH_ASKPASS`).
- **Clone Repository…** and **Manage Remotes…** from the repository menu.
- Refreshes automatically when files, the index or refs change (git-ignored files are skipped).
- Fetch / Pull / Push, stash pop / apply / drop, light / dark theme.
- Shortcuts: ⌘K commit (⌘↩ in the message), ⇧⌘K push, ⌘T update project, ⌘9 / ⌘0 Log / Commit, ⌘R refresh.
- Log loads 5000 commits at a time ("Load more" at the bottom); Home / End / PageUp / PageDown in the commit list.
- Remembers the theme and recently opened repositories (`~/Library/Application Support/GitBrains/settings`).

## Layout

| File | Purpose |
| --- | --- |
| `src/git.rs` | `git` CLI backend (log, status, diff, branch ops…) |
| `src/graph.rs` | Commit graph lane layout |
| `src/model.rs` | Shared repo state; runs ops in the background and broadcasts refreshes |
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
| `src/settings.rs` | Persisted theme and recent repositories |
| `src/actions.rs` | Branch / commit context menus |
| `src/workspace.rs` | Main window, toolbar, tabs |

## Tests

```sh
cargo test   # graph layout, ref parsing, and git operations against throwaway repositories
```
