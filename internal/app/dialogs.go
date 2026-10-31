package app

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Zenyet/banyan/internal/git"
	"github.com/Zenyet/banyan/internal/i18n"
	"github.com/Zenyet/banyan/internal/settings"
)

// label is a field's name above it.
func label(c *ui.Context, text string) { ui.Text(c, text).FontWeight(500) }

// note is muted text under a check box, lined up with its label.
func note(c *ui.Context, text string) { muted(c, text).PaddingStart(24) }

// choiceCard is a bordered option of a choice, the one chosen in the
// accent color.
func choiceCard(c *ui.Context, title, detail string, chosen bool) ui.Element {
	t := c.Theme()
	card := ui.Column(c).Padding(8).Gap(2).Radius(t.Radius).Border(1, t.Border).Cursor(ui.CursorPointer)
	if chosen {
		card.Border(1, t.Accent)
	}
	card.Children(func() {
		ui.Text(c, title)
		muted(c, detail)
	})
	return card
}

// openUpdate is JetBrains' "Update Project" dialog: merge or rebase the
// incoming changes, or only fast-forward.
func openUpdate(ws *Workspace) {
	method := settings.Read(func(s *settings.Settings) git.UpdateMethod { return s.UpdateMethod })
	ws.Shell.Open(&Dialog{Title: i18n.T("Update Project"), Width: 440, Body: func(c *ui.Context, d *Dialog) {
		branch := ws.Model.CurrentBranch
		if branch == "" {
			branch = "HEAD"
		}
		muted(c, i18n.Tf("Fetch and update {} from its tracked branch. Local changes are stashed and restored.", branch))
		for _, m := range git.AllUpdateMethods {
			if choiceCard(c.Key(m), m.Label(), m.Detail(), method == m).Clicked() {
				method = m
			}
		}
		dialogButtons(c, d, i18n.T("Update"), false, false, func() {
			d.Close()
			changeSettings(func(s *settings.Settings) { s.UpdateMethod = method })
			ws.run(i18n.T("Update Project"), func(r *git.Repo) (string, error) { return r.UpdateProject(method) })
		})
	}})
}

// openTagDialog is "Create Tag": a name, an optional message that makes it
// an annotated tag, and whether to push it right away.
func openTagDialog(ws *Workspace, target string) {
	var name, message string
	push := false
	ws.Shell.Open(&Dialog{Title: i18n.Tf("Create Tag on {}", short(target)), Width: 480, Body: func(c *ui.Context, d *Dialog) {
		confirm := func() {
			n := strings.TrimSpace(name)
			if n == "" {
				ws.Model.Fail(errors.New(i18n.T("Name the tag")))
				return
			}
			msg, p := strings.TrimSpace(message), push
			d.Close()
			ws.run(i18n.Tf("Create tag {}", n), func(r *git.Repo) (string, error) {
				out, err := r.CreateTag(n, target, msg)
				if err != nil || !p {
					return out, err
				}
				return r.PushTag(n)
			})
		}
		label(c, i18n.T("Name"))
		if ui.TextInput(c.Key("name"), &name).Placeholder("v1.0.0").AutoFocus().FillWidth().Submitted() {
			confirm()
		}
		label(c, i18n.T("Message"))
		ui.TextArea(c.Key("message"), &message).Placeholder(i18n.T("Optional")).Lines(3, 8).FillWidth()
		muted(c, i18n.T("With a message the tag is annotated: it records who tagged and when."))
		ui.Checkbox(c.Key("push"), &push, i18n.T("Push the tag"))
		dialogButtons(c, d, i18n.T("Create"), false, false, confirm)
	}})
}

// openMergeDialog is "Merge with Options": fast-forwarding, squashing,
// stopping before the commit and the commit's message, like JetBrains'
// Merge dialog.
func openMergeDialog(ws *Workspace, branch string) {
	current := ws.Model.CurrentBranch
	if current == "" {
		current = "HEAD"
	}
	var o git.MergeOptions
	message := ""
	ws.Shell.Open(&Dialog{Title: i18n.Tf("Merge '{}' into '{}'", branch, current), Width: 480, Body: func(c *ui.Context, d *Dialog) {
		if !o.Squash {
			label(c, i18n.T("Fast-forward"))
			segments(c, func() {
				for _, ff := range []struct {
					value git.FastForward
					label string
				}{{git.FastForwardAllow, i18n.T("When possible")}, {git.FastForwardOnly, i18n.T("Only fast-forward")}, {git.FastForwardNever, i18n.T("Always create a merge commit")}} {
					if segment(c.Key(ff.value), ff.label, o.FastForward == ff.value).Clicked() {
						o.FastForward = ff.value
					}
				}
			})
		}
		ui.Checkbox(c.Key("squash"), &o.Squash, i18n.T("Squash commits"))
		note(c, i18n.T("Brings the changes in as one change to commit, without a merge commit"))
		if !o.Squash {
			ui.Checkbox(c.Key("no-commit"), &o.NoCommit, i18n.T("Don't commit"))
			note(c, i18n.T("Stops before the merge commit, to look at or change the result first"))
		}
		if !o.Squash && !o.NoCommit {
			label(c, i18n.T("Commit message"))
			ui.TextInput(c.Key("message"), &message).Placeholder(i18n.Tf("Git's message: Merge branch '{}'", branch)).FillWidth()
		}
		dialogButtons(c, d, i18n.T("Merge"), false, false, func() {
			options := o
			// Squashing never fast-forwards or commits, and git refuses
			// --no-ff with it.
			if o.Squash {
				options.FastForward, options.NoCommit = git.FastForwardAllow, false
			}
			if !o.Squash && !o.NoCommit {
				options.Message = strings.TrimSpace(message)
			}
			d.Close()
			ws.run(i18n.Tf("Merge {}", branch), func(r *git.Repo) (string, error) { return r.MergeWith(branch, &options) })
		})
	}})
}

// openStashDialog is "Stash Changes": the message and what to put aside,
// like JetBrains' stash dialog. checked are the files checked in the
// Commit tab, which the stash can be limited to.
func openStashDialog(ws *Workspace, checked []string) {
	message := "WIP"
	onlyChecked, keepIndex, untracked := false, false, true
	ws.Shell.Open(&Dialog{Title: i18n.T("Stash Changes"), Width: 480, Body: func(c *ui.Context, d *Dialog) {
		confirm := func() {
			o := git.StashOptions{Message: strings.TrimSpace(message), KeepIndex: keepIndex, IncludeUntracked: untracked}
			if onlyChecked {
				o.Paths = checked
			}
			d.Close()
			ws.run(i18n.T("Stash"), func(r *git.Repo) (string, error) { return r.Stash(&o) })
		}
		label(c, i18n.T("Message"))
		if ui.TextInput(c.Key("message"), &message).Placeholder(i18n.T("Message (optional)")).AutoFocus().FillWidth().Submitted() {
			confirm()
		}
		if len(checked) > 0 {
			ui.Checkbox(c.Key("only"), &onlyChecked, i18n.Tf("Only the checked files ({})", len(checked)))
			note(c, i18n.T("Otherwise every local change is stashed"))
		}
		ui.Checkbox(c.Key("untracked"), &untracked, i18n.T("Include unversioned files"))
		ui.Checkbox(c.Key("keep"), &keepIndex, i18n.T("Keep index"))
		note(c, i18n.T("Staged changes stay in the work tree as well as going into the stash"))
		dialogButtons(c, d, i18n.T("Create Stash"), false, false, confirm)
	}})
}

// openCommitOptions is "Commit Options": author, sign-off, signing and
// hooks for the next commit, plus how this repository signs its commits.
// apply receives them when the dialog is confirmed.
func openCommitOptions(ws *Workspace, o git.CommitOptions, apply func(git.CommitOptions)) {
	signing := ws.Model.Repo.Signing()
	author, key := o.Author, signing.Key
	signOff, skipHooks := o.SignOff, o.SkipHooks
	sign := signing.Always
	if o.Sign != nil {
		sign = *o.Sign
	}
	always := signing.Always
	format := map[string]string{"ssh": "SSH", "x509": "X.509"}[signing.Format]
	if format == "" {
		format = "GPG"
	}
	keyNote := i18n.Tf("Signs with your default {} key", format)
	if signing.Key != "" {
		keyNote = i18n.Tf("Signs with {} key {}", format, signing.Key)
	}
	ws.Shell.Open(&Dialog{Title: i18n.T("Commit Options"), Width: 520, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		ui.Text(c, i18n.T("This commit")).FontWeight(600)
		label(c, i18n.T("Author"))
		ui.TextInput(c.Key("author"), &author).Placeholder(i18n.T("Name <email>, or empty for yourself")).FillWidth()
		ui.Checkbox(c.Key("sign-off"), &signOff, i18n.T("Sign-off"))
		note(c, i18n.T("Adds a Signed-off-by line with your name"))
		ui.Checkbox(c.Key("sign"), &sign, i18n.T("Sign the commit"))
		note(c, keyNote)
		ui.Checkbox(c.Key("hooks"), &skipHooks, i18n.T("Skip Git hooks"))
		note(c, i18n.T("Does not run the pre-commit and commit-msg hooks"))
		ui.Box(c).Height(1).FillWidth().Background(t.Border)
		ui.Text(c, i18n.T("This repository")).FontWeight(600)
		if ui.Checkbox(c.Key("always"), &always, i18n.T("Sign every commit")).Changed() {
			sign = always
		}
		label(c, i18n.T("Signing key"))
		ui.TextInput(c.Key("key"), &key).Placeholder(i18n.T("Default key")).FillWidth()
		muted(c, i18n.T("A GPG key ID, or for SSH signing the path to a public key."))
		dialogButtons(c, d, i18n.T("OK"), false, false, func() {
			a := strings.TrimSpace(author)
			if a != "" && !git.IsValidAuthor(a) {
				ws.Model.Fail(errors.New(i18n.T("Write the author as Name <email>")))
				return
			}
			// Repository settings first, so the commit's own choice is
			// judged against them.
			k, al := strings.TrimSpace(key), always
			if al != signing.Always || k != signing.Key {
				ws.run(i18n.T("Change signing settings"), func(r *git.Repo) (string, error) {
					if _, err := r.SetAlwaysSign(al); err != nil {
						return "", err
					}
					return r.SetSigningKey(k)
				})
			}
			options := git.CommitOptions{Author: a, SignOff: signOff, SkipHooks: skipHooks}
			if sign != al {
				s := sign
				options.Sign = &s
			}
			d.Close()
			apply(options)
		})
	}})
}

// openPalette is "Find Action" (⇧⌘A): type part of a command's name and
// run it, like JetBrains' action search.
func openPalette(ws *Workspace) {
	var all []Command
	for _, cmd := range commands() {
		if cmd.ID != "find_action" {
			all = append(all, cmd)
		}
	}
	query := ""
	selected := 0
	ws.Shell.Open(&Dialog{Title: i18n.T("Find Action"), Width: 520, Body: func(c *ui.Context, d *Dialog) {
		t := c.Theme()
		var matches []Command
		words := strings.Fields(strings.ToLower(query))
		for _, cmd := range all {
			label := strings.ToLower(cmd.Label)
			ok := true
			for _, w := range words {
				ok = ok && strings.Contains(label, w)
			}
			if ok {
				matches = append(matches, cmd)
			}
		}
		run := func(row int) {
			if row < 0 || row >= len(matches) {
				return
			}
			id := matches[row].ID
			d.Close()
			runCommand(id, ws.Shell.Win)
		}
		input := ui.TextInput(c.Key("query"), &query).Placeholder(i18n.T("Type a command")).AutoFocus().FillWidth()
		if input.Changed() {
			selected = 0
		}
		if input.Shortcut(0, ui.KeyDown) {
			selected = min(selected+1, len(matches)-1)
		}
		if input.Shortcut(0, ui.KeyUp) {
			selected = max(selected-1, 0)
		}
		if input.Submitted() {
			run(selected)
		}
		ui.Scroll(c.Key("matches")).MaxHeight(360).Children(func() {
			if len(matches) == 0 {
				muted(c, i18n.T("No matching commands")).Padding(8)
			}
			for row, cmd := range matches {
				item := ui.Row(c.Key(cmd.ID)).Height(28).PaddingX(8).Radius(4).Cursor(ui.CursorPointer)
				switch {
				case row == selected:
					item.Background(selectionBG)
				case item.Hovered():
					item.Background(hoverBG(t))
				}
				if item.Clicked() {
					run(row)
				}
				item.Children(func() {
					ui.Text(c, cmd.Label).Grow(1)
					if keys := cmd.Keys(); keys != "" {
						muted(c, displayKeys(keys))
					}
				})
			}
		})
		muted(c, i18n.T("↑ ↓ to choose, Enter to run"))
	}})
}
