//! Context menus for branches and commits, shared by the toolbar branch popup,
//! the branches tree and the commit list.

use gpui_kit::component::menu::{PopupMenu, PopupMenuItem};
use gpui_kit::*;

use crate::git::{Branch, RefKind, ResetMode};
use crate::model::RepoModel;
use crate::ui::{confirm, prompt_text};

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
    let title = format!("Create New Branch from {}", short(&start));
    prompt_text(
        title,
        "Create",
        "",
        move |name, _, cx| {
            let start = start.clone();
            model.update(cx, |model, cx| {
                model.run(
                    format!("Create branch {name}"),
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
        menu = menu.item(item("Checkout", model, move |model, _, cx| {
            let remote = kind == RefKind::Remote;
            model.update(cx, |m, cx| m.checkout(n.clone(), remote, cx))
        }));
    }
    let n = name.clone();
    menu = menu.item(item(format!("New Branch from '{name}'..."), model, move |model, window, cx| {
        new_branch(model, n.clone(), window, cx)
    }));
    if !is_current {
        let n = name.clone();
        menu = menu.item(item(
            format!("Compare with '{current_label}'"),
            model,
            move |model, window, cx| crate::compare::open(model.clone(), n.clone(), window, cx),
        ));
        menu = menu.separator();
        let n = name.clone();
        menu = menu.item(item(
            format!("Merge '{name}' into '{current_label}'"),
            model,
            move |model, _, cx| {
                let n = n.clone();
                model.update(cx, |m, cx| m.run(format!("Merge {n}"), move |repo| repo.merge(&n), cx))
            },
        ));
        let n = name.clone();
        menu = menu.item(item(
            format!("Rebase '{current_label}' onto '{name}'"),
            model,
            move |model, _, cx| {
                let n = n.clone();
                model.update(cx, |m, cx| m.run(format!("Rebase onto {n}"), move |repo| repo.rebase(&n), cx))
            },
        ));
    }
    if branch.kind == RefKind::Local {
        menu = menu.separator();
        let n = name.clone();
        menu = menu.item(item("Rename...", model, move |model, window, cx| {
            let model = model.clone();
            let old = n.clone();
            prompt_text(
                format!("Rename Branch '{old}'"),
                "Rename",
                old.clone(),
                move |new, _, cx| {
                    let old = old.clone();
                    model.update(cx, |m, cx| {
                        m.run(format!("Rename {old}"), move |repo| repo.rename_branch(&old, &new), cx)
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
        menu = menu.item(item("Delete", model, move |model, window, cx| {
            let model = model.clone();
            let n = n.clone();
            let kind = kind.clone();
            let what = match kind {
                RefKind::Remote => "remote branch",
                RefKind::Tag => "tag",
                _ => "branch",
            };
            confirm(
                format!("Delete {what}"),
                format!("Delete {what} '{n}'? This cannot be undone easily."),
                "Delete",
                move |_, cx| {
                    let n = n.clone();
                    let kind = kind.clone();
                    model.update(cx, |m, cx| {
                        m.run(
                            format!("Delete {n}"),
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

pub fn commit_menu(mut menu: PopupMenu, model: &Entity<RepoModel>, id: String, subject: String) -> PopupMenu {
    let short_id = short(&id).to_string();
    let copy_id = id.clone();
    menu = menu.item(PopupMenuItem::new("Copy Revision Number").on_click(move |_, _, cx| {
        cx.write_to_clipboard(ClipboardItem::new_string(copy_id.clone()))
    }));
    menu = menu.item(PopupMenuItem::new("Copy Commit Subject").on_click(move |_, _, cx| {
        cx.write_to_clipboard(ClipboardItem::new_string(subject.clone()))
    }));
    menu = menu.separator();

    let i = id.clone();
    menu = menu.item(item("Checkout Revision", model, move |model, _, cx| {
        let i = i.clone();
        model.update(cx, |m, cx| m.checkout(i, false, cx))
    }));
    let i = id.clone();
    menu = menu.item(item("New Branch...", model, move |model, window, cx| {
        new_branch(model, i.clone(), window, cx)
    }));
    let i = id.clone();
    menu = menu.item(item("New Tag...", model, move |model, window, cx| {
        let model = model.clone();
        let i = i.clone();
        prompt_text(
            format!("Create Tag on {}", short(&i)),
            "Create",
            "",
            move |name, _, cx| {
                let i = i.clone();
                model.update(cx, |m, cx| m.run(format!("Create tag {name}"), move |repo| repo.create_tag(&name, &i), cx));
            },
            window,
            cx,
        );
    }));
    menu = menu.separator();

    let i = id.clone();
    menu = menu.item(item("Cherry-Pick", model, move |model, _, cx| {
        let i = i.clone();
        model.update(cx, |m, cx| m.run(format!("Cherry-pick {}", short(&i)), move |repo| repo.cherry_pick(&i), cx))
    }));
    let i = id.clone();
    menu = menu.item(item("Revert Commit", model, move |model, _, cx| {
        let i = i.clone();
        model.update(cx, |m, cx| m.run(format!("Revert {}", short(&i)), move |repo| repo.revert(&i), cx))
    }));
    let i = id.clone();
    menu = menu.item(item("Interactively Rebase from Here...", model, move |model, window, cx| {
        crate::rebase_dialog::open(model.clone(), i.clone(), window, cx)
    }));
    menu = menu.separator();

    for (label, mode) in [
        ("Reset Current Branch to Here (Soft)", ResetMode::Soft),
        ("Reset Current Branch to Here (Mixed)", ResetMode::Mixed),
        ("Reset Current Branch to Here (Hard)", ResetMode::Hard),
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
                    model.update(cx, |m, cx| m.run(format!("Reset to {}", short(&i)), move |repo| repo.reset(&i, mode), cx));
                }
            };
            if matches!(mode, ResetMode::Hard) {
                confirm(
                    "Hard Reset",
                    format!("Reset the current branch to {short_id} and discard all local changes?"),
                    "Reset",
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

pub fn short(rev: &str) -> &str {
    if rev.len() == 40 && rev.chars().all(|c| c.is_ascii_hexdigit()) {
        &rev[..8]
    } else {
        rev
    }
}
