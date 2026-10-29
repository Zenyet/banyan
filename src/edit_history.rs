//! Changing the current branch's history from the Log's commit menu, like
//! JetBrains' Edit Commit Message, Squash, Fixup, Drop and Undo Commit. Each
//! runs as an interactive rebase; one that stops on conflicts shows up in
//! the operation banner like any other.

use gpui_kit::component::{WindowExt as _, notification::Notification};
use gpui_kit::*;

use crate::actions::short;
use crate::git::Rewrite;
use crate::model::RepoModel;
use crate::ui::{confirm, prompt_message};
use crate::i18n::tr;

pub fn reword(model: &Entity<RepoModel>, id: String, window: &mut Window, cx: &mut App) {
    let rewrite = Rewrite::Reword { id: id.clone(), message: String::new() };
    with_checks(model, &rewrite, None, window, cx, move |model, window, cx| {
        let current = model.read(cx).repo.commit_message(&id).unwrap_or_default();
        let model = model.clone();
        let id = id.clone();
        prompt_message(
            tr!("Edit Commit Message of {}", short(&id)),
            "",
            tr!("Save"),
            current,
            move |message, _, cx| {
                let rewrite = Rewrite::Reword { id: id.clone(), message };
                run(&model, tr!("Edit message of {}", short(&id)), rewrite, cx);
            },
            window,
            cx,
        );
    });
}

/// Meld `ids` (newest first, as the Log selects them) into the oldest.
pub fn squash(model: &Entity<RepoModel>, ids: Vec<String>, window: &mut Window, cx: &mut App) {
    let rewrite = Rewrite::Squash { ids: ids.clone(), message: String::new() };
    with_checks(model, &rewrite, None, window, cx, move |model, window, cx| {
        let repo = model.read(cx).repo.clone();
        let combined: Vec<String> = ids.iter().rev().filter_map(|id| repo.commit_message(id).ok()).collect();
        let model = model.clone();
        let ids = ids.clone();
        let count = ids.len();
        prompt_message(
            tr!("Squash {} Commits", count),
            tr!("The commits become one, with this message:"),
            tr!("Squash"),
            combined.join("\n\n"),
            move |message, _, cx| {
                let rewrite = Rewrite::Squash { ids: ids.clone(), message };
                run(&model, tr!("Squash {} commits", count), rewrite, cx);
            },
            window,
            cx,
        );
    });
}

pub fn squash_into_parent(model: &Entity<RepoModel>, id: String, window: &mut Window, cx: &mut App) {
    let parent = model.read(cx).repo.git(&["rev-parse", "--verify", "-q", &format!("{id}^")]);
    match parent {
        Ok(parent) => squash(model, vec![id, parent.trim().to_string()], window, cx),
        Err(_) => notify(window, cx, tr!("The first commit has no parent to squash into.")),
    }
}

pub fn fixup_into_parent(model: &Entity<RepoModel>, id: String, window: &mut Window, cx: &mut App) {
    let rewrite = Rewrite::FixupIntoParent { id: id.clone() };
    let label = tr!("Fixup {} into its parent", short(&id));
    let check = rewrite.clone();
    with_checks(model, &check, None, window, cx, move |model, _, cx| run(model, label.clone(), rewrite.clone(), cx));
}

pub fn drop(model: &Entity<RepoModel>, ids: Vec<String>, window: &mut Window, cx: &mut App) {
    let count = ids.len();
    let question = if count == 1 {
        tr!("Remove commit {} from the branch? Its changes are lost.", short(&ids[0]))
    } else {
        tr!("Remove these {} commits from the branch? Their changes are lost.", count)
    };
    let rewrite = Rewrite::Drop { ids };
    let label = if count == 1 { tr!("Drop commit").to_string() } else { tr!("Drop {} commits", count) };
    let check = rewrite.clone();
    with_checks(model, &check, Some(question), window, cx, move |model, _, cx| {
        run(model, label.clone(), rewrite.clone(), cx)
    });
}

/// Undo the last commit; its changes stay staged and its message goes back
/// into the Commit tab.
pub fn undo_commit(model: &Entity<RepoModel>, id: String, window: &mut Window, cx: &mut App) {
    let pushed = model.read(cx).repo.pushed_to(&id);
    let model = model.clone();
    let undo = move |_: &mut Window, cx: &mut App| model.update(cx, |m, cx| m.undo_commit(cx));
    match pushed {
        Some(upstream) => confirm(
            tr!("Undo Pushed Commit"),
            tr!("This commit is already in {}. Undoing it here means force pushing later.", upstream),
            tr!("Undo Commit"),
            undo,
            window,
            cx,
        ),
        None => undo(window, cx),
    }
}

/// Check that `rewrite` can run; then ask `question` (if any) and warn about
/// rewriting pushed commits in one confirmation, and go on with `proceed`.
fn with_checks(
    model: &Entity<RepoModel>,
    rewrite: &Rewrite,
    question: Option<String>,
    window: &mut Window,
    cx: &mut App,
    proceed: impl Fn(&Entity<RepoModel>, &mut Window, &mut App) + 'static,
) {
    let repo = model.read(cx).repo.clone();
    let base = match repo.rewrite_base(rewrite) {
        Ok(base) => base,
        Err(err) => return notify(window, cx, format!("{err:#}")),
    };
    let warning = repo
        .pushed_to(&base)
        .map(|upstream| tr!("These commits are already in {}; rewriting them means force pushing later.", upstream));
    let model = model.clone();
    let dropping = question.is_some();
    match (question, warning) {
        (None, None) => proceed(&model, window, cx),
        (question, warning) => {
            let message = [question, warning].into_iter().flatten().collect::<Vec<_>>().join(" ");
            let title = if dropping { tr!("Drop Commits") } else { tr!("Rewrite Pushed Commits") };
            confirm(title, message, tr!("Continue"), move |window, cx| proceed(&model, window, cx), window, cx);
        }
    }
}

fn run(model: &Entity<RepoModel>, label: String, rewrite: Rewrite, cx: &mut App) {
    model.update(cx, |m, cx| m.run(label, move |repo| repo.rewrite(&rewrite), cx));
}

fn notify(window: &mut Window, cx: &mut App, message: impl Into<SharedString>) {
    window.push_notification(Notification::warning(message), cx);
}
