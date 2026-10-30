package app

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/model"
	"github.com/Zenyet/banyan/internal/settings"
)

func newBranch(ws *Workspace, start string) {
	ws.Shell.PromptText(i18n.Tf("Create New Branch from {}", short(start)), i18n.T("Create"), "", func(name string) {
		ws.run(i18n.Tf("Create branch {}", name), func(r *git.Repo) (string, error) { return r.CreateBranch(name, start, true) })
	})
}

// branchMenu is the menu of a branch or tag, in the toolbar's branch popup
// and the Log's branches tree.
func branchMenu(menu *ui.Menu, ws *Workspace, b git.Branch) {
	m := ws.Model
	name := b.Name
	isCurrent := m.CurrentBranch == name && b.Kind == git.RefLocal
	current := m.CurrentBranch
	if current == "" {
		current = "HEAD"
	}
	remote := b.Kind == git.RefRemote

	if !isCurrent {
		if menu.Item(i18n.T("Checkout")).Chosen() {
			m.Checkout(name, remote)
		}
		// The other repositories of the window with a branch of that name,
		// as JetBrains controls the branches of several roots together.
		var siblings []*model.Model
		for _, other := range siblingModels(m) {
			has := slices.ContainsFunc(other.Branches, func(o git.Branch) bool { return o.Kind == b.Kind && o.Name == name })
			if has && !(b.Kind == git.RefLocal && other.CurrentBranch == name) {
				siblings = append(siblings, other)
			}
		}
		if b.Kind != git.RefTag && len(siblings) > 0 {
			if menu.Item(i18n.Tf("Checkout in All Repositories ({})", len(siblings)+1)).Chosen() {
				for _, target := range append([]*model.Model{m}, siblings...) {
					target.Checkout(name, remote)
				}
			}
		}
	}
	if menu.Item(i18n.Tf("New Branch from '{}'...", name)).Chosen() {
		newBranch(ws, name)
	}
	if b.Kind != git.RefTag {
		favorite := m.IsFavorite(&b)
		label := i18n.T("Add to Favorites")
		if favorite {
			label = i18n.T("Remove from Favorites")
		}
		if menu.Item(label).Chosen() {
			m.SetFavorite(&b, !favorite)
		}
	}
	if b.Kind != git.RefTag && !isCurrent {
		if menu.Item(i18n.Tf("New Worktree from '{}'...", name)).Chosen() {
			// A remote branch is checked out as the local branch of its name.
			branch := name
			if remote {
				if _, local, ok := strings.Cut(name, "/"); ok {
					branch = local
				}
			}
			openWorktrees(ws, branch)
		}
	}
	if b.Kind == git.RefLocal {
		if menu.Item(i18n.Tf("Push '{}'", name)).Chosen() {
			ws.run(i18n.Tf("Push {}", name), func(r *git.Repo) (string, error) { return r.PushBranch(name) })
		}
		if !isCurrent && b.Upstream != "" {
			if menu.Item(i18n.Tf("Update '{}'", name)).Chosen() {
				ws.run(i18n.Tf("Update {}", name), func(r *git.Repo) (string, error) { return r.UpdateBranch(name) })
			}
		}
		if menu.Item(i18n.T("Set Upstream...")).Chosen() {
			def := b.Upstream
			if def == "" {
				def = "origin/" + name
			}
			ws.Shell.PromptText(i18n.Tf("Remote Branch '{}' Tracks", name), i18n.T("Set"), def, func(upstream string) {
				ws.run(i18n.Tf("Set upstream of {}", name), func(r *git.Repo) (string, error) { return r.SetUpstream(name, upstream) })
			})
		}
	}
	if !isCurrent {
		if menu.Item(i18n.T("Show Diff with Working Tree")).Chosen() {
			openWorktreeDiff(ws, name, "", "")
		}
		if menu.Item(i18n.Tf("Compare with '{}'", current)).Chosen() {
			openCompare(ws, name, "")
		}
		if menu.Item(i18n.T("Compare with...")).Chosen() {
			def := "main"
			for _, f := range m.Favorites {
				short := strings.TrimPrefix(strings.TrimPrefix(f, "refs/heads/"), "refs/remotes/")
				if short != f && short != name {
					def = short
					break
				}
			}
			ws.Shell.PromptText(i18n.Tf("Compare {} with Branch", name), i18n.T("Compare"), def, func(base string) {
				openCompare(ws, name, base)
			})
		}
		menu.Separator()
		if menu.Item(i18n.Tf("Merge '{}' into '{}'", name, current)).Chosen() {
			ws.run(i18n.Tf("Merge {}", name), func(r *git.Repo) (string, error) { return r.Merge(name) })
		}
		if menu.Item(i18n.T("Merge with Options...")).Chosen() {
			openMergeDialog(ws, name)
		}
		if menu.Item(i18n.Tf("Rebase '{}' onto '{}'", current, name)).Chosen() {
			ws.run(i18n.Tf("Rebase onto {}", name), func(r *git.Repo) (string, error) { return r.Rebase(name) })
		}
	}
	if b.Kind == git.RefLocal {
		menu.Separator()
		if menu.Item(i18n.T("Rename...")).Chosen() {
			ws.Shell.PromptText(i18n.Tf("Rename Branch '{}'", name), i18n.T("Rename"), name, func(new string) {
				ws.run(i18n.Tf("Rename {}", name), func(r *git.Repo) (string, error) { return r.RenameBranch(name, new) })
			})
		}
	}
	if !isCurrent && b.Kind != git.RefHead {
		if menu.Item(i18n.T("Delete")).Chosen() {
			title, question := i18n.T("Delete Branch"), i18n.Tf("Delete branch '{}'? This cannot be undone easily.", name)
			switch b.Kind {
			case git.RefRemote:
				title, question = i18n.T("Delete Remote Branch"), i18n.Tf("Delete remote branch '{}'? This cannot be undone easily.", name)
			case git.RefTag:
				title, question = i18n.T("Delete Tag"), i18n.Tf("Delete tag '{}'? This cannot be undone easily.", name)
			}
			kind := b.Kind
			ws.Shell.Confirm(title, question, i18n.T("Delete"), func() {
				ws.run(i18n.Tf("Delete {}", name), func(r *git.Repo) (string, error) {
					switch kind {
					case git.RefRemote:
						return r.DeleteRemoteBranch(name)
					case git.RefTag:
						return r.DeleteTag(name)
					}
					return r.DeleteBranch(name, true)
				})
			})
		}
	}
}

// commitMenu is the menu of one commit; isHead adds Undo Commit.
func commitMenu(menu *ui.Menu, ws *Workspace, id, subject string, isHead bool) {
	m := ws.Model
	if menu.Item(i18n.T("Copy Revision Number")).Chosen() {
		mygo.Clipboard.WriteText(id)
	}
	if menu.Item(i18n.T("Copy Commit Subject")).Chosen() {
		mygo.Clipboard.WriteText(subject)
	}
	if m.HasWeb {
		url := m.Web.CommitURL(id)
		if menu.Item(i18n.Tf("Open on {}", m.Web.Name())).Chosen() {
			mygo.Shell.OpenExternal(url)
		}
		if menu.Item(i18n.T("Copy Link to Commit")).Chosen() {
			mygo.Clipboard.WriteText(url)
		}
	}
	menu.Separator()
	if menu.Item(i18n.T("Checkout Revision")).Chosen() {
		m.Checkout(id, false)
	}
	if menu.Item(i18n.T("New Branch...")).Chosen() {
		newBranch(ws, id)
	}
	if menu.Item(i18n.T("New Tag...")).Chosen() {
		openTagDialog(ws, id)
	}
	menu.Separator()
	if menu.Item(i18n.T("Cherry-Pick")).Chosen() {
		ws.run(i18n.Tf("Cherry-pick {}", short(id)), func(r *git.Repo) (string, error) { return r.CherryPick([]string{id}) })
	}
	if menu.Item(i18n.T("Revert Commit")).Chosen() {
		ws.run(i18n.Tf("Revert {}", short(id)), func(r *git.Repo) (string, error) { return r.Revert([]string{id}) })
	}
	if menu.Item(i18n.T("Show Diff with Working Tree")).Chosen() {
		openWorktreeDiff(ws, id, "", "")
	}
	if menu.Item(i18n.T("Create Patch...")).Chosen() {
		savePatch(ws, []string{id}, subject)
	}
	if menu.Item(i18n.T("Interactively Rebase from Here...")).Chosen() {
		openRebase(ws, id)
	}
	menu.Separator()
	if menu.Item(i18n.T("Edit Commit Message...")).Chosen() {
		reword(ws, id)
	}
	if menu.Item(i18n.T("Fixup into Parent")).Chosen() {
		fixupIntoParent(ws, id)
	}
	if menu.Item(i18n.T("Squash into Parent...")).Chosen() {
		squashIntoParent(ws, id)
	}
	if menu.Item(i18n.T("Drop Commit")).Chosen() {
		dropCommits(ws, []string{id})
	}
	if isHead && menu.Item(i18n.T("Undo Commit")).Chosen() {
		undoCommit(ws, id)
	}
	menu.Separator()
	for _, reset := range []struct {
		label string
		mode  git.ResetMode
	}{
		{i18n.T("Reset Current Branch to Here (Soft)"), git.ResetSoft},
		{i18n.T("Reset Current Branch to Here (Mixed)"), git.ResetMixed},
		{i18n.T("Reset Current Branch to Here (Hard)"), git.ResetHard},
	} {
		if menu.Item(reset.label).Chosen() {
			run := func() {
				ws.run(i18n.Tf("Reset to {}", short(id)), func(r *git.Repo) (string, error) { return r.Reset(id, reset.mode) })
			}
			if reset.mode == git.ResetHard {
				ws.Shell.Confirm(i18n.T("Hard Reset"), i18n.Tf("Reset the current branch to {} and discard all local changes?", short(id)), i18n.T("Reset"), run)
			} else {
				run()
			}
		}
	}
}

// commitsMenu is the menu of several selected commits, ids newest first.
func commitsMenu(menu *ui.Menu, ws *Workspace, ids []string) {
	count := len(ids)
	oldestFirst := slices.Clone(ids)
	slices.Reverse(oldestFirst)
	if menu.Item(i18n.T("Copy Revision Numbers")).Chosen() {
		mygo.Clipboard.WriteText(strings.Join(ids, "\n"))
	}
	menu.Separator()
	if menu.Item(i18n.Tf("Cherry-Pick {} Commits", count)).Chosen() {
		ws.run(i18n.Tf("Cherry-pick {} commits", count), func(r *git.Repo) (string, error) { return r.CherryPick(oldestFirst) })
	}
	if menu.Item(i18n.Tf("Revert {} Commits", count)).Chosen() {
		ws.run(i18n.Tf("Revert {} commits", count), func(r *git.Repo) (string, error) { return r.Revert(ids) })
	}
	if menu.Item(i18n.Tf("Create Patch from {} Commits...", count)).Chosen() {
		savePatch(ws, oldestFirst, itoa(count)+" commits")
	}
	if count == 2 && menu.Item(i18n.T("Compare Commits")).Chosen() {
		openWorktreeDiff(ws, ids[1], ids[0], "")
	}
	menu.Separator()
	if menu.Item(i18n.Tf("Squash {} Commits...", count)).Chosen() {
		squashCommits(ws, ids)
	}
	if menu.Item(i18n.Tf("Drop {} Commits", count)).Chosen() {
		dropCommits(ws, ids)
	}
}

// savePatch asks where to save ids (oldest first) as a patch, named after
// title.
func savePatch(ws *Workspace, ids []string, title string) {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(words) < 8 {
			words = append(words, w)
		}
	}
	name := strings.Join(words, "-") + ".patch"
	go func() {
		dest, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: ws.Shell.Win, DefaultPath: filepath.Join(ws.Model.Repo.Root(), name)})
		if err != nil || dest == "" {
			return
		}
		model.Post(func() {
			ws.run(i18n.Tf("Create patch {}", dest), func(r *git.Repo) (string, error) { return "", r.CreatePatch(ids, dest) })
		})
	}()
}

// Changing the current branch's history from the Log's commit menu, like
// JetBrains' Edit Commit Message, Squash, Fixup, Drop and Undo Commit. Each
// runs as an interactive rebase; one that stops on conflicts shows up in the
// operation banner like any other.

func reword(ws *Workspace, id string) {
	rw := &git.Rewrite{Kind: git.RewriteReword, IDs: []string{id}}
	withChecks(ws, rw, "", func() {
		current, _ := ws.Model.Repo.CommitMessage(id)
		ws.Shell.PromptMessage(i18n.Tf("Edit Commit Message of {}", short(id)), "", i18n.T("Save"), current, func(message string) {
			runRewrite(ws, i18n.Tf("Edit message of {}", short(id)), &git.Rewrite{Kind: git.RewriteReword, IDs: []string{id}, Message: message})
		})
	})
}

// squashCommits melds ids (newest first, as the Log selects them) into the
// oldest.
func squashCommits(ws *Workspace, ids []string) {
	withChecks(ws, &git.Rewrite{Kind: git.RewriteSquash, IDs: ids}, "", func() {
		var messages []string
		for i := len(ids) - 1; i >= 0; i-- {
			if msg, err := ws.Model.Repo.CommitMessage(ids[i]); err == nil {
				messages = append(messages, msg)
			}
		}
		count := len(ids)
		ws.Shell.PromptMessage(i18n.Tf("Squash {} Commits", count), i18n.T("The commits become one, with this message:"), i18n.T("Squash"),
			strings.Join(messages, "\n\n"), func(message string) {
				runRewrite(ws, i18n.Tf("Squash {} commits", count), &git.Rewrite{Kind: git.RewriteSquash, IDs: ids, Message: message})
			})
	})
}

func squashIntoParent(ws *Workspace, id string) {
	parent, err := ws.Model.Repo.Git("rev-parse", "--verify", "-q", id+"^")
	if err != nil {
		ws.Shell.Toast("warning", i18n.T("The first commit has no parent to squash into."))
		return
	}
	squashCommits(ws, []string{id, strings.TrimSpace(parent)})
}

func fixupIntoParent(ws *Workspace, id string) {
	rw := &git.Rewrite{Kind: git.RewriteFixupIntoParent, IDs: []string{id}}
	withChecks(ws, rw, "", func() { runRewrite(ws, i18n.Tf("Fixup {} into its parent", short(id)), rw) })
}

func dropCommits(ws *Workspace, ids []string) {
	count := len(ids)
	question := i18n.Tf("Remove these {} commits from the branch? Their changes are lost.", count)
	label := i18n.Tf("Drop {} commits", count)
	if count == 1 {
		question = i18n.Tf("Remove commit {} from the branch? Its changes are lost.", short(ids[0]))
		label = i18n.T("Drop commit")
	}
	rw := &git.Rewrite{Kind: git.RewriteDrop, IDs: ids}
	withChecks(ws, rw, question, func() { runRewrite(ws, label, rw) })
}

// undoCommit undoes the last commit; its changes stay staged and its
// message goes back into the Commit tab.
func undoCommit(ws *Workspace, id string) {
	upstream, pushed := ws.Model.Repo.PushedTo(id)
	if pushed && isProtected(upstream) {
		ws.Shell.Toast("warning", protectedMessage(upstream))
		return
	}
	if !pushed {
		ws.Model.UndoCommit()
		return
	}
	ws.Shell.Confirm(i18n.T("Undo Pushed Commit"),
		i18n.Tf("This commit is already in {}. Undoing it here means force pushing later.", upstream),
		i18n.T("Undo Commit"), ws.Model.UndoCommit)
}

// withChecks checks that rw can run; then asks question (if any) and warns
// about rewriting pushed commits in one confirmation, and goes on with
// proceed.
func withChecks(ws *Workspace, rw *git.Rewrite, question string, proceed func()) {
	repo := ws.Model.Repo
	base, err := repo.RewriteBase(rw)
	if err != nil {
		ws.Shell.Toast("warning", err.Error())
		return
	}
	upstream, pushed := repo.PushedTo(base)
	if pushed && isProtected(upstream) {
		ws.Shell.Toast("warning", protectedMessage(upstream))
		return
	}
	var parts []string
	if question != "" {
		parts = append(parts, question)
	}
	if pushed {
		parts = append(parts, i18n.Tf("These commits are already in {}; rewriting them means force pushing later.", upstream))
	}
	if len(parts) == 0 {
		proceed()
		return
	}
	title := i18n.T("Rewrite Pushed Commits")
	if question != "" {
		title = i18n.T("Drop Commits")
	}
	ws.Shell.Confirm(title, strings.Join(parts, " "), i18n.T("Continue"), proceed)
}

func runRewrite(ws *Workspace, label string, rw *git.Rewrite) {
	ws.run(label, func(r *git.Repo) (string, error) { return r.Rewrite(rw) })
}

func isProtected(upstream string) bool {
	return settings.Read(func(s *settings.Settings) bool { return s.IsProtected(upstream) })
}

// protectedMessage says why pushed commits of a protected branch can't be
// rewritten.
func protectedMessage(upstream string) string {
	return i18n.Tf("These commits are already in {}, a protected branch; its history is not rewritten.", upstream)
}

// refuseIfProtected refuses with a toast when base and later commits are
// pushed to a protected branch, for rewrites that don't go through
// withChecks.
func refuseIfProtected(ws *Workspace, base string) bool {
	if upstream, ok := ws.Model.Repo.PushedTo(base); ok && isProtected(upstream) {
		ws.Shell.Toast("warning", protectedMessage(upstream))
		return true
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }
