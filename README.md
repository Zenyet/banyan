# GitBrains

A JetBrains-style Git desktop client built with [GPUI](https://gpui.rs) and [GPUI Kit](https://gpui-kit.com).

```sh
cargo run -- /path/to/repo   # or run inside a repository; with no repository a welcome window opens
```

## Features

- **Log tab**: branches tree (HEAD / Local / Remote / Tags), commit graph with colored lanes and ref labels,
  text/hash filter, branch filter, commit details (message, author, "in N branches"), changed files tree,
  and a side-by-side / unified diff preview.
- **Commit context menu**: copy hash, checkout revision, new branch / tag, cherry-pick, revert, reset (soft / mixed / hard).
- **Branch popup** (toolbar) and branches-tree context menu: checkout, new branch from, merge into current,
  rebase current onto, rename, delete (local, remote, tag).
- **Commit tab**: changelist-style checkboxes (Changes / Unversioned Files / Merge Conflicts), commit, amend,
  commit and push, rollback, stash; accept yours / theirs for conflicts; continue / abort merge, rebase, cherry-pick.
- Fetch / Pull / Push, stash pop / apply / drop, light / dark theme, auto-refresh when the window regains focus.

## Layout

| File | Purpose |
| --- | --- |
| `src/git.rs` | `git` CLI backend (log, status, diff, branch ops…) |
| `src/graph.rs` | Commit graph lane layout |
| `src/model.rs` | Shared repo state; runs ops in the background and broadcasts refreshes |
| `src/log_view.rs` | Log tab |
| `src/changes_view.rs` | Commit tab |
| `src/diff_pane.rs` | Diff preview |
| `src/actions.rs` | Branch / commit context menus |
| `src/workspace.rs` | Main window, toolbar, tabs |
