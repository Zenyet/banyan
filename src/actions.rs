//! Context menus for branches and commits, shared by the toolbar branch popup,
//! the branches tree and the commit list.

use gpui_kit::component::menu::{PopupMenu, PopupMenuItem};
use gpui_kit::*;

use crate::git::{Branch, RefKind, ResetMode};
use crate::model::RepoModel;
use crate::ui::{confirm, prompt_text};
use crate::i18n::tr;

fn item(
    label: impl Into<SharedString>,
    model: &Entity<RepoModel>,
    handler: impl Fn(&Entity<RepoModel>, &mut Window, &mut App) + 'static,
) -> PopupMenuItem {
    let model = model.clone();
    PopupMenuItem::new(label).on_click(move |_, window, cx| handler(&model, window, cx))
}

pub fn new_branch(model: &Entity<RepoModel>, start: String, window: &mut Window, cx: &mut App) {
    let model = model.clone();
    let title = tr!("Create New Branch from {}", short(&start));
    prompt_text(
        title,
        tr!("Create"),
        "",
        move |name, _, cx| {
            let start = start.clone();
            model.update(cx, |model, cx| {
                model.run(
                    tr!("Create branch {}", name),
                    move |repo| repo.create_branch(&name, &start, true),
                    cx,
                )
            });
        },
        window,
        cx,
    );
}

pub fn branch_menu(mut menu: PopupMenu, model: &Entity<RepoModel>, branch: &Branch, cx: &App) -> PopupMenu {
    let current = model.read(cx).current_branch.clone();
    let name = branch.name.clone();
    let is_current = current.as_deref() == Some(name.as_str()) && branch.kind == RefKind::Local;
    let current_label = current.clone().unwrap_or_else(|| "HEAD".into());

    if !is_current {
        let n = name.clone();
        let kind = branch.kind.clone();
        menu = menu.item(item(tr!("Checkout"), model, move |model, _, cx| {
            let remote = kind == RefKind::Remote;
            model.update(cx, |m, cx| m.checkout(n.clone(), remote, cx))
        }));
    }
    let n = name.clone();
    menu = menu.item(item(tr!("New Branch from '{}'...", name), model, move |model, window, cx| {
        new_branch(model, n.clone(), window, cx)
    }));
    if branch.kind == RefKind::Local {
        let n = name.clone();
        menu = menu.item(item(tr!("Push '{}'", name), model, move |model, _, cx| {
            let n = n.clone();
            model.update(cx, |m, cx| m.run(tr!("Push {}", n), move |repo| repo.push_branch(&n), cx))
        }));
        if !is_current && branch.upstream.is_some() {
            let n = name.clone();
            menu = menu.item(item(tr!("Update '{}'", name), model, move |model, _, cx| {
                let n = n.clone();
                model.update(cx, |m, cx| m.run(tr!("Update {}", n), move |repo| repo.update_branch(&n), cx))
            }));
        }
        let n = name.clone();
        let upstream = branch.upstream.clone();
        menu = menu.item(item(tr!("Set Upstream..."), model, move |model, window, cx| {
            let (model, n) = (model.clone(), n.clone());
            let default = upstream.clone().unwrap_or_else(|| format!("origin/{n}"));
            prompt_text(
                tr!("Remote Branch '{}' Tracks", n),
                tr!("Set"),
                default,
                move |upstream, _, cx| {
                    let n = n.clone();
                    model.update(cx, |m, cx| {
                        m.run(tr!("Set upstream of {}", n), move |repo| repo.set_upstream(&n, Some(&upstream)), cx)
                    });
                },
                window,
                cx,
            );
        }));
    }
    if !is_current {
        let n = name.clone();
        let w = name.clone();
        menu = menu.item(item(tr!("Show Diff with Working Tree"), model, move |model, window, cx| {
            crate::worktree_diff::open(model.clone(), w.clone(), window, cx)
        }));
        menu = menu.item(item(
            tr!("Compare with '{}'", current_label),
            model,
            move |model, window, cx| crate::compare::open(model.clone(), n.clone(), window, cx),
        ));
        menu = menu.separator();
        let n = name.clone();
        menu = menu.item(item(
            tr!("Merge '{}' into '{}'", name, current_label),
            model,
            move |model, _, cx| {
                let n = n.clone();
                model.update(cx, |m, cx| m.run(tr!("Merge {}", n), move |repo| repo.merge(&n), cx))
            },
        ));
        let n = name.clone();
        menu = menu.item(item(
            tr!("Rebase '{}' onto '{}'", current_label, name),
            model,
            move |model, _, cx| {
                let n = n.clone();
                model.update(cx, |m, cx| m.run(tr!("Rebase onto {}", n), move |repo| repo.rebase(&n), cx))
            },
        ));
    }
    if branch.kind == RefKind::Local {
        menu = menu.separator();
        let n = name.clone();
        menu = menu.item(item(tr!("Rename..."), model, move |model, window, cx| {
            let model = model.clone();
            let old = n.clone();
            prompt_text(
                tr!("Rename Branch '{}'", old),
                tr!("Rename"),
                old.clone(),
                move |new, _, cx| {
                    let old = old.clone();
                    model.update(cx, |m, cx| {
                        m.run(tr!("Rename {}", old), move |repo| repo.rename_branch(&old, &new), cx)
                    });
                },
                window,
                cx,
            );
        }));
    }
    if !is_current && branch.kind != RefKind::Head {
        let n = name.clone();
        let kind = branch.kind.clone();
        menu = menu.item(item(tr!("Delete"), model, move |model, window, cx| {
            let model = model.clone();
            let n = n.clone();
            let kind = kind.clone();
            let (title, question) = match kind {
                RefKind::Remote => (tr!("Delete Remote Branch"), tr!("Delete remote branch '{}'? This cannot be undone easily.", n)),
                RefKind::Tag => (tr!("Delete Tag"), tr!("Delete tag '{}'? This cannot be undone easily.", n)),
                _ => (tr!("Delete Branch"), tr!("Delete branch '{}'? This cannot be undone easily.", n)),
            };
            confirm(
                title,
                question,
                tr!("Delete"),
                move |_, cx| {
                    let n = n.clone();
                    let kind = kind.clone();
                    model.update(cx, |m, cx| {
                        m.run(
                            tr!("Delete {}", n),
                            move |repo| match kind {
                                RefKind::Remote => repo.delete_remote_branch(&n),
                                RefKind::Tag => repo.delete_tag(&n),
                                _ => repo.delete_branch(&n, true),
                            },
                            cx,
                        )
                    });
                },
                window,
                cx,
            );
        }));
    }
    menu
}

/// The menu for one commit; `is_head` adds Undo Commit.
pub fn commit_menu(mut menu: PopupMenu, model: &Entity<RepoModel>, id: String, subject: String, is_head: bool) -> PopupMenu {
    let short_id = short(&id).to_string();
    let copy_id = id.clone();
    menu = menu.item(PopupMenuItem::new(tr!("Copy Revision Number")).on_click(move |_, _, cx| {
        cx.write_to_clipboard(ClipboardItem::new_string(copy_id.clone()))
    }));
    let copy_subject = subject.clone();
    menu = menu.item(PopupMenuItem::new(tr!("Copy Commit Subject")).on_click(move |_, _, cx| {
        cx.write_to_clipboard(ClipboardItem::new_string(copy_subject.clone()))
    }));
    menu = menu.separator();

    let i = id.clone();
    menu = menu.item(item(tr!("Checkout Revision"), model, move |model, _, cx| {
        let i = i.clone();
        model.update(cx, |m, cx| m.checkout(i, false, cx))
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("New Branch..."), model, move |model, window, cx| {
        new_branch(model, i.clone(), window, cx)
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("New Tag..."), model, move |model, window, cx| {
        let model = model.clone();
        let i = i.clone();
        prompt_text(
            tr!("Create Tag on {}", short(&i)),
            tr!("Create"),
            "",
            move |name, _, cx| {
                let i = i.clone();
                model.update(cx, |m, cx| m.run(tr!("Create tag {}", name), move |repo| repo.create_tag(&name, &i), cx));
            },
            window,
            cx,
        );
    }));
    menu = menu.separator();

    let i = id.clone();
    menu = menu.item(item(tr!("Cherry-Pick"), model, move |model, _, cx| {
        let i = i.clone();
        model.update(cx, |m, cx| m.run(tr!("Cherry-pick {}", short(&i)), move |repo| repo.cherry_pick(&[i]), cx))
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("Revert Commit"), model, move |model, _, cx| {
        let i = i.clone();
        model.update(cx, |m, cx| m.run(tr!("Revert {}", short(&i)), move |repo| repo.revert(&[i]), cx))
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("Show Diff with Working Tree"), model, move |model, window, cx| {
        crate::worktree_diff::open(model.clone(), i.clone(), window, cx)
    }));
    let (i, s) = (id.clone(), subject.clone());
    menu = menu.item(item(tr!("Create Patch..."), model, move |model, _, cx| {
        save_patch(model, vec![i.clone()], &s, cx)
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("Interactively Rebase from Here..."), model, move |model, window, cx| {
        crate::rebase_dialog::open(model.clone(), i.clone(), window, cx)
    }));
    menu = menu.separator();

    let i = id.clone();
    menu = menu.item(item(tr!("Edit Commit Message..."), model, move |model, window, cx| {
        crate::edit_history::reword(model, i.clone(), window, cx)
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("Fixup into Parent"), model, move |model, window, cx| {
        crate::edit_history::fixup_into_parent(model, i.clone(), window, cx)
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("Squash into Parent..."), model, move |model, window, cx| {
        crate::edit_history::squash_into_parent(model, i.clone(), window, cx)
    }));
    let i = id.clone();
    menu = menu.item(item(tr!("Drop Commit"), model, move |model, window, cx| {
        crate::edit_history::drop(model, vec![i.clone()], window, cx)
    }));
    if is_head {
        let i = id.clone();
        menu = menu.item(item(tr!("Undo Commit"), model, move |model, window, cx| {
            crate::edit_history::undo_commit(model, i.clone(), window, cx)
        }));
    }
    menu = menu.separator();

    for (label, mode) in [
        (tr!("Reset Current Branch to Here (Soft)"), ResetMode::Soft),
        (tr!("Reset Current Branch to Here (Mixed)"), ResetMode::Mixed),
        (tr!("Reset Current Branch to Here (Hard)"), ResetMode::Hard),
    ] {
        let i = id.clone();
        let short_id = short_id.clone();
        menu = menu.item(item(label, model, move |model, window, cx| {
            let i = i.clone();
            let run = {
                let model = model.clone();
                let i = i.clone();
                move |_: &mut Window, cx: &mut App| {
                    let i = i.clone();
                    model.update(cx, |m, cx| m.run(tr!("Reset to {}", short(&i)), move |repo| repo.reset(&i, mode), cx));
                }
            };
            if matches!(mode, ResetMode::Hard) {
                confirm(
                    tr!("Hard Reset"),
                    tr!("Reset the current branch to {} and discard all local changes?", short_id),
                    tr!("Reset"),
                    run,
                    window,
                    cx,
                );
            } else {
                run(window, cx);
            }
        }));
    }
    menu
}

/// The menu for several selected commits, `ids` newest first.
pub fn commits_menu(mut menu: PopupMenu, model: &Entity<RepoModel>, ids: Vec<String>) -> PopupMenu {
    let count = ids.len();
    let copy_ids = ids.join("\n");
    menu = menu.item(PopupMenuItem::new(tr!("Copy Revision Numbers")).on_click(move |_, _, cx| {
        cx.write_to_clipboard(ClipboardItem::new_string(copy_ids.clone()))
    }));
    menu = menu.separator();
    let oldest_first: Vec<String> = ids.iter().rev().cloned().collect();
    menu = menu.item(item(tr!("Cherry-Pick {} Commits", count), model, move |model, _, cx| {
        let ids = oldest_first.clone();
        model.update(cx, |m, cx| m.run(tr!("Cherry-pick {} commits", count), move |repo| repo.cherry_pick(&ids), cx))
    }));
    let revert_ids = ids.clone();
    menu = menu.item(item(tr!("Revert {} Commits", count), model, move |model, _, cx| {
        let ids = revert_ids.clone();
        model.update(cx, |m, cx| m.run(tr!("Revert {} commits", count), move |repo| repo.revert(&ids), cx))
    }));
    let patch_ids: Vec<String> = ids.iter().rev().cloned().collect();
    menu = menu.item(item(tr!("Create Patch from {} Commits...", count), model, move |model, _, cx| {
        save_patch(model, patch_ids.clone(), &format!("{count} commits"), cx)
    }));
    if let [newer, older] = ids.as_slice() {
        let (newer, older) = (newer.clone(), older.clone());
        menu = menu.item(item(tr!("Compare Commits"), model, move |model, window, cx| {
            crate::worktree_diff::open_between(model.clone(), older.clone(), Some(newer.clone()), None, window, cx)
        }));
    }
    menu = menu.separator();
    let squash_ids = ids.clone();
    menu = menu.item(item(tr!("Squash {} Commits...", count), model, move |model, window, cx| {
        crate::edit_history::squash(model, squash_ids.clone(), window, cx)
    }));
    menu.item(item(tr!("Drop {} Commits", count), model, move |model, window, cx| {
        crate::edit_history::drop(model, ids.clone(), window, cx)
    }))
}

/// Ask where to save `ids` (oldest first) as a patch, named after `title`.
fn save_patch(model: &Entity<RepoModel>, ids: Vec<String>, title: &str, cx: &mut App) {
    let root = model.read(cx).repo.root().to_path_buf();
    let name: String = title
        .chars()
        .map(|c| if c.is_alphanumeric() { c.to_ascii_lowercase() } else { '-' })
        .collect::<String>()
        .split('-')
        .filter(|part| !part.is_empty())
        .take(8)
        .collect::<Vec<_>>()
        .join("-");
    let dest = cx.prompt_for_new_path(&root, Some(&format!("{name}.patch")));
    let model = model.clone();
    cx.spawn(async move |cx| {
        let Ok(Ok(Some(dest))) = dest.await else { return };
        cx.update(|cx| {
            model.update(cx, |m, cx| {
                m.run(tr!("Create patch {}", dest.display()), move |repo| repo.create_patch(&ids, &dest), cx)
            })
        });
    })
    .detach();
}

/// Ask for a patch file and apply it to the work tree.
pub fn apply_patch(model: &Entity<RepoModel>, cx: &mut App) {
    let paths = cx.prompt_for_paths(PathPromptOptions {
        files: true,
        directories: false,
        multiple: false,
        prompt: Some(tr!("Apply Patch").into()),
    });
    let model = model.clone();
    cx.spawn(async move |cx| {
        let Ok(Ok(Some(paths))) = paths.await else { return };
        let Some(file) = paths.into_iter().next() else { return };
        cx.update(|cx| {
            model.update(cx, |m, cx| m.run(tr!("Apply patch"), move |repo| repo.apply_patch(&file), cx))
        });
    })
    .detach();
}

pub fn short(rev: &str) -> &str {
    if rev.len() == 40 && rev.chars().all(|c| c.is_ascii_hexdigit()) {
        &rev[..8]
    } else {
        rev
    }
}
